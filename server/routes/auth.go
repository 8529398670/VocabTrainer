package routes

import (
	errors "errors"
	strconv "strconv"
	strings "strings"

	fiber "github.com/gofiber/fiber/v3"

	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

type loginRequest struct {
	Credential string `json:"credential"`
}

// signInReply is how a sign-in answers: done, or a two-factor code is needed
// first (see signIn).
type signInReply struct {
	Ok        bool   `json:"ok,omitempty"`
	TwoFactor bool   `json:"two_factor,omitempty"`
	Challenge string `json:"challenge,omitempty"`
}

type signedIn struct {
	Ok bool `json:"ok"`
}

type twoFactorNeeded struct {
	TwoFactor bool   `json:"two_factor"`
	Challenge string `json:"challenge"`
}

func ( signInReply ) APIName() ( name string ) { name = "SignIn"; return }

func ( signInReply ) APIOneOf() ( shapes []any ) {
	shapes = []any{ signedIn{} , twoFactorNeeded{} }
	return
}

func ( signInReply ) APIDoc() ( text string ) { text = "Either the sign-in finished, or a two-factor code is needed: send it with the challenge to /api/login/two-factor."; return }

// newSignInReply is signInReply for a sign-in signIn has ended.
func newSignInReply( challenge string ) ( reply *signInReply ) {
	reply = &signInReply{ Ok: true }
	if challenge != "" { reply = &signInReply{ TwoFactor: true , Challenge: challenge } }
	return
}

// ShowLoginPage answers a visit to a login link with the ordinary app page,
// which sees the /login/ path and asks the person to confirm. Nothing is
// redeemed here.
//
// That is the point of the two steps. Link previews in chat apps, mail
// scanners, and browser prefetch all fetch a URL without anyone asking them
// to; if the GET were the login, they would spend the link before its owner
// ever clicked it. And someone already signed in gets told that continuing
// switches accounts, rather than being silently signed in as whoever the link
// belongs to.
//
// The route is registered as /login/* rather than /login/:credential because
// the credential contains a "." and route parameters should not have to care
// about the shape of a secret.
func ( handlers *Handlers ) ShowLoginPage( c fiber.Ctx ) ( err error ) {
	// The credential is in this page's URL; make sure no request the page
	// makes carries it onward in a Referer header.
	c.Set( fiber.HeaderReferrerPolicy , "no-referrer" )
	err = handlers.Static.ServeIndex( c )
	return
}

// signInRules is what decides how a sign-in ends, from the running
// configuration.
func ( handlers *Handlers ) signInRules() ( rules models.SignInRules ) {
	cfg := handlers.cfg()
	rules = models.SignInRules{
		SessionTTL: cfg.SessionTTL,
	}
	return
}

// signIn is how every way of signing in ends. A challenge goes back to the
// page, which asks for the code, and nothing changes in this browser yet: it
// answers the challenge. Otherwise this browser switches to the new session:
// the one it had is ended server-side, not merely replaced in the cookie jar.
func ( handlers *Handlers ) signIn( c fiber.Ctx , signed *models.SignIn ) ( challenge string , err error ) {
	handlers.destroyCookieSession( c )
	if err = handlers.Guard.SetSessionCookie( c , signed.SessionCredential ); err != nil { return }
	models.RecordSessionDevice( handlers.Store , signed.SessionCredential , c.IP() , logs.Truncate( c.Get( fiber.HeaderUserAgent ) , models.SessionUserAgentMax ) )
	return
}

// redeemLoginLink signs in with a login link. Redeeming a valid link is the
// login; there is nothing to phish out of a user beyond the link itself, which
// dies on first use. An account with two-factor on goes on to give a code (see
// two_factor.go).
//
// It is the one state-changing route with no CSRF token -- there is no
// session yet to hold one -- so it is guarded differently. A forged login
// signs a victim's browser in to the *attacker's* account, after which
// anything the victim does lands where the attacker can read it. Requiring
// a JSON body is what blocks that: an HTML form cannot send one, and a
// cross-origin fetch that tries needs a CORS preflight this server never
// approves. Sec-Fetch-Site, which every current browser sends, refuses the
// rest outright. Both are sameOrigin's, in front of it in routes.go, so they
// hold before the body is read.
//
// @route("POST /api/login", summary="Redeem a login link",
//     tag="Sign-in", access=PUBLIC, use=[sameOrigin( loginElsewhere )],
// )
func ( handlers *Handlers ) redeemLoginLink( c fiber.Ctx , in *loginRequest ) ( reply *signInReply , err error ) {
	credential := strings.Trim( strings.TrimSpace( in.Credential ) , "/" )
	signed , redeem_err := models.RedeemLoginToken( handlers.Store , credential , handlers.signInRules() )
	if redeem_err != nil {
		// Expired, already spent, never existed, wrong secret, or a disabled
		// account all get the same answer. Telling them apart would let
		// someone probe which links are real.
		if errors.Is( redeem_err , models.ErrLoginTokenInvalid ) {
			handlers.auditAs( c , logs.Anonymous() , logs.KindAuth , logs.LevelWarn , "login link refused" , nil )
			err = refuse( fiber.StatusBadRequest , "invalid login link" )
			return
		}
		err = redeem_err
		return
	}

	message := "signed in with a login link"
	issuer , detail := handlers.issuer( signed.IssuedBy )
	if issuer != "" { message = "signed in with a link issued by " + issuer }
	handlers.auditAs( c , handlers.logUser( signed.UserID , security.MethodSession ) , logs.KindAuth , logs.LevelInfo , message , detail )
	challenge , err := handlers.signIn( c , signed )
	if err != nil { return }
	reply = newSignInReply( challenge )
	return
}

// issuer names the admin who reissued the link a sign-in came through
// (models.LoginToken.IssuedBy), for its log entry, and the detail that goes
// with it; "" and nil when nobody else did. An admin can sign in as anyone
// below them that way, so the account's own log says so.
func ( handlers *Handlers ) issuer( admin_id uint64 ) ( name string , detail fiber.Map ) {
	if admin_id == 0 { return }
	name = "#" + strconv.FormatUint( admin_id , 10 )
	if admin , err := models.GetUser( handlers.Store , admin_id ); err == nil { name = admin.DisplayName }
	detail = fiber.Map{ "issued_by": admin_id }
	return
}

// loginElsewhere is what a sign-in route says to a request that did not come
// from this site's own page.
const loginElsewhere = "login must be submitted from this site's own login page"

// sameOrigin refuses, with message, a request that is not JSON from one of
// this site's own pages (sameOriginJSON). It goes in front of each public
// route a browser posts to before it has a session to hold a CSRF token.
func sameOrigin( message string ) ( handler fiber.Handler ) {
	handler = func( c fiber.Ctx ) ( err error ) {
		if sameOriginJSON( c ) == false {
			err = forbidden( c , message )
			return
		}
		err = c.Next()
		return
	}
	return
}

// sameOriginJSON is true for a request with a JSON content type that, if it
// came from a browser, came from one of this site's own pages.
func sameOriginJSON( c fiber.Ctx ) ( ok bool ) {
	media_type , _ , _ := strings.Cut( c.Get( fiber.HeaderContentType ) , ";" )
	if strings.EqualFold( strings.TrimSpace( media_type ) , fiber.MIMEApplicationJSON ) == false { return }
	site := c.Get( "Sec-Fetch-Site" )
	ok = site == "" || site == "same-origin"
	return
}

// destroyCookieSession deletes the session named by this request's cookie,
// if any. Deleting the record rather than only the cookie is what matters: a
// copy of the cookie taken earlier is then dead too.
func ( handlers *Handlers ) destroyCookieSession( c fiber.Ctx ) {
	raw := c.Cookies( handlers.cfg().SessionCookieName() )
	if raw == "" { return }
	if credential , decrypt_err := handlers.Guard.DecryptCookie( raw ); decrypt_err == nil {
		models.DestroySession( handlers.Store , credential )
	}
}
