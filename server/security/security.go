// Package security is the only place that decides who a request belongs to
// and whether it is allowed to change anything. Route handlers read the
// answer; they never re-derive it. One implementation means one place to get
// right, and one place to look when auditing.
//
// There are two ways to prove who you are, and they meet here rather than
// anywhere downstream: a session cookie (a browser) or an API key in an
// Authorization header (a script, a cron job, another service). Everything
// past this package sees one Actor and one effective role, so a route does not
// know or care which door the request came through.
package security

import (
	strings "strings"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
)

// Typed, unexported keys so nothing else in the process can overwrite what we
// stashed on the request by accident.
type contextKey string

const keyActor contextKey = "current_actor"

// Auth method names, as reported by /api/me. Worth exposing so a client can
// tell a browser session from a key without guessing.
const (
	MethodSession = "session"
	MethodAPIKey  = "api_key"
)

// Actor is who a request belongs to, plus how they proved it.
//
// Role is the *effective* role, not the account's: a user-scoped API key held
// by an admin authenticates as a user. Every permission check reads this
// field, so the narrowing cannot be forgotten at a call site.
type Actor struct {
	User    *models.User
	Session *models.Session
	APIKey  *models.APIKey
	Role    string
}

func ( actor *Actor ) IsAdmin() ( result bool ) {
	result = actor.Role == models.RoleAdmin
	return
}

// ViaAPIKey distinguishes the two doors. It decides the CSRF question below,
// and it is what an endpoint reads if it genuinely must be browser-only.
func ( actor *Actor ) ViaAPIKey() ( result bool ) {
	result = actor.APIKey != nil
	return
}

// KeyID is the id of the API key this request came with, or "" for a browser
// session. A login link or invite link a key makes records it, so revoking the
// key voids them (see models.VoidLinksMintedByKeyTx).
func ( actor *Actor ) KeyID() ( result string ) {
	if actor.APIKey != nil { result = actor.APIKey.ID }
	return
}

func ( actor *Actor ) Method() ( result string ) {
	result = MethodSession
	if actor.ViaAPIKey() {
		result = MethodAPIKey
	}
	return
}

type Guard struct {
	Store *db.Store
	// Settings is the running configuration. Cookie lifetime and the
	// Secure flag, and whether API keys are believed, can change while the
	// server runs, so they are read from it per request.
	Settings *config.Holder
}

func New( store *db.Store , settings *config.Holder ) ( guard *Guard ) {
	guard = &Guard{ Store: store , Settings: settings }
	return
}

func ( guard *Guard ) cfg() ( cfg *config.Config ) {
	cfg = guard.Settings.Get()
	return
}

// SetSessionCookie stores the session credential encrypted with the server
// key. The server already checks the credential against the database, so the
// encryption is not what makes the session unforgeable -- it means the cookie
// is opaque to anyone reading it out of a browser profile or a proxy log, and
// that a tampered cookie fails to decrypt instead of reaching the lookup.
func ( guard *Guard ) SetSessionCookie( c fiber.Ctx , credential string ) ( err error ) {
	sealed , err := encryption.ChaChaEncryptString( encryption.SubKey( guard.cfg().SecretKey , encryption.PurposeCookie ) , credential )
	if err != nil { return }
	guard.writeSessionCookie( c , sealed , guard.cfg().SessionTTL )
	return
}

// writeSessionCookie is the one place the cookie's attributes are spelled, so
// the copy sent when a session extends cannot drift from the one sent at
// sign-in. A later cookie of the same name on this response replaces it, so
// logging out or switching account on a request that extended still wins.
func ( guard *Guard ) writeSessionCookie( c fiber.Ctx , sealed string , lifetime time.Duration ) {
	c.Cookie( &fiber.Cookie{
		Name:     guard.cfg().SessionCookieName(),
		Value:    sealed,
		Path:     "/",
		MaxAge:   int( lifetime.Seconds() ),
		Secure:   guard.cfg().SecureCookies,
		HTTPOnly: true,
		SameSite: "Strict",
	} )
}

func ( guard *Guard ) ClearSessionCookie( c fiber.Ctx ) {
	// Overwrite before clearing: some browsers hold on to a cookie whose
	// attributes do not match exactly, and an empty value is harmless either way.
	c.Cookie( &fiber.Cookie{
		Name:     guard.cfg().SessionCookieName(),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   guard.cfg().SecureCookies,
		HTTPOnly: true,
		SameSite: "Strict",
	} )
	c.ClearCookie( guard.cfg().SessionCookieName() )
	// A cookie from before the __Host- prefix goes too.
	if guard.cfg().SecureCookies { c.ClearCookie( guard.cfg().CookieName ) }
}

// DecryptCookie turns the stored cookie back into a session credential.
// Exposed because logging out has to destroy the server-side record, not just
// drop the cookie -- otherwise a copy of the cookie taken earlier still works.
func ( guard *Guard ) DecryptCookie( raw string ) ( credential string , err error ) {
	credential , err = encryption.ChaChaDecryptBase64String( encryption.SubKey( guard.cfg().SecretKey , encryption.PurposeCookie ) , raw )
	return
}

// LoadUser runs on every request. It resolves the caller at most once and
// stashes the result, so a handler and a middleware asking "who is this" do
// not cost two database reads. It never rejects anything -- that is
// RequireLogin's job -- because plenty of routes are fine for anonymous
// visitors.
//
// An Authorization: Bearer header decides it: the request is that key or no
// one, and the cookie is not read. Otherwise the cookie. The CSRF rule below
// stays sound: a header is never ambient -- a page on another site cannot add
// one without a CORS preflight this server does not answer -- and a request
// that skips the token check by carrying a key needed that key to begin with.
func ( guard *Guard ) LoadUser( c fiber.Ctx ) ( err error ) {
	var actor *Actor
	// A request that names a key in its Authorization header acts as that key
	// and nothing else, even from a browser that is signed in: a script using
	// a narrow key must not quietly run as the full account whose cookie
	// happens to be in the jar. A bad key is no one, not the cookie.
	if hasBearer( c ) {
		actor = guard.resolveAPIKey( c )
	} else {
		actor = guard.resolveSession( c )
	}
	if actor != nil {
		c.Locals( keyActor , actor )
	}
	err = c.Next()
	return
}

func ( guard *Guard ) resolveSession( c fiber.Ctx ) ( actor *Actor ) {
	raw := c.Cookies( guard.cfg().SessionCookieName() )
	if raw == "" { return }

	credential , decrypt_err := encryption.ChaChaDecryptBase64String( encryption.SubKey( guard.cfg().SecretKey , encryption.PurposeCookie ) , raw )
	if decrypt_err != nil { return }

	session , user := models.LoadSession( guard.Store , credential )
	if user == nil { return }

	// The browser drops the cookie at its own expiry, so extending only the
	// record would still sign this person out when that arrived.
	if models.TouchSession( guard.Store , session , guard.cfg().SessionTTL , guard.cfg().SessionMaxAge ) {
		guard.writeSessionCookie( c , raw , time.Until( session.ExpiresAt ) )
	}
	actor = &Actor{ User: user , Session: session , Role: user.Role }
	return
}

// hasBearer is true when the request carries any Authorization header. Any
// at all is an attempt at a key, and one that does not hold a good key is
// nobody (resolveAPIKey): it never falls back to the cookie. Splitting only on
// a space let "Bearer<TAB>key" fall back and run as the signed-in account
// (S16).
func hasBearer( c fiber.Ctx ) ( result bool ) {
	result = strings.TrimSpace( c.Get( fiber.HeaderAuthorization ) ) != ""
	return
}

// resolveAPIKey reads "Authorization: Bearer <id>.<secret>".
//
// One header and one scheme, deliberately: a second accepted spelling (an
// X-API-Key, a query parameter) is a second code path to audit, and a
// credential in a query string ends up in access logs and browser history.
//
// APIKeysEnabled is checked here rather than only on the routes that mint
// keys, so turning the feature off actually closes the door: the header stops
// being believed at all, and existing keys stop authenticating.
func ( guard *Guard ) resolveAPIKey( c fiber.Ctx ) ( actor *Actor ) {
	if guard.cfg().APIKeysEnabled == false { return }

	// The scheme and the key, split on any whitespace, and nothing else.
	parts := strings.Fields( c.Get( fiber.HeaderAuthorization ) )
	if len( parts ) != 2 || strings.EqualFold( parts[ 0 ] , "bearer" ) == false { return }
	credential := parts[ 1 ]

	key , user := models.LoadAPIKey( guard.Store , credential )
	if user == nil { return }

	actor = &Actor{
		User:   user,
		APIKey: key,
		// The ceiling, applied per request. See models.NarrowRole: the stored
		// role is what was asked for, this is what is granted.
		Role: models.NarrowRole( user.Role , key.Role ),
	}
	return
}

// RequireLogin, RequireAdmin, RequireSession and RequireAPIKey are middleware rather than
// helpers called at the top of each handler, so a new protected route is
// protected by where it is registered -- forgetting the check is not something
// you can do silently.
func ( guard *Guard ) RequireLogin( c fiber.Ctx ) ( err error ) {
	if ActorFrom( c ) == nil {
		err = c.Status( fiber.StatusUnauthorized ).JSON( fiber.Map{ "error": "not authenticated" } )
		return
	}
	err = c.Next()
	return
}

func ( guard *Guard ) RequireAdmin( c fiber.Ctx ) ( err error ) {
	actor := ActorFrom( c )
	if actor == nil {
		err = c.Status( fiber.StatusUnauthorized ).JSON( fiber.Map{ "error": "not authenticated" } )
		return
	}
	if actor.IsAdmin() == false {
		err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": "forbidden" } )
		return
	}
	err = c.Next()
	return
}

// RequireSession refuses a request that authenticated with an API key, even a
// correctly scoped one. It guards what a key must not reach (see routes.go):
// managing your own API keys (making, renaming, expiring, revoking, and the
// Sign-in links and Make keys switches), your own password, PIN and
// two-factor, other people's files, and the owner's screens.
//
// Accounts and ways in are not on that list. An admin key may create and
// delete accounts, change roles and expiry, and hand out login links and
// invite links. A key makes its own account a login link only once that
// account switched it on for the key, from a session (models.APIKey.MaySignIn),
// and then one that works once, for at most 15 minutes.
//
// A key may make keys only one way, with lineage. The owner switches
// MayMintKeys on, from a session, on one of the owner's own admin-role keys;
// that key may then make keys for other accounts
// (POST /api/admin/users/{user_id}/keys), never the owner's, each capped at
// its account's role and recording the key that made it
// (models.APIKey.MintedByKey). A minted key never gets MayMintKeys or
// MaySignIn, so it cannot mint or turn into a session in turn. Every other
// door to minting stays browser-only.
//
// What keeps revoking a leaked key meaningful is that everything it made
// records it. Revoking or deleting the key voids its login links still
// usable, ends every session one of them signed in, switches off its invite
// links, and revokes every key it minted, down the whole chain
// (models.VoidLinksMintedByKeyTx). Disabling or demoting the key's account
// does the same for all its keys. What it does not undo is anything those
// did meanwhile, or the accounts made through an invite link it handed out:
// those show in the activity log and the user list.
func ( guard *Guard ) RequireSession( c fiber.Ctx ) ( err error ) {
	actor := ActorFrom( c )
	if actor == nil {
		err = c.Status( fiber.StatusUnauthorized ).JSON( fiber.Map{ "error": "not authenticated" } )
		return
	}
	if actor.ViaAPIKey() {
		err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": "this endpoint requires a signed-in session, not an API key" } )
		return
	}
	err = c.Next()
	return
}

// ViewerMayCall is what a viewer may send besides reads (portal contract v2,
// role viewer): signing out, ending its own sessions, and cancelling its own
// login link. Everything else that is not GET, HEAD or OPTIONS answers 403 view_only
// (ViewOnly). path may be a real path or a route's template; both match.
func ViewerMayCall( method string , path string ) ( ok bool ) {
	switch {
	case method == fiber.MethodGet || method == fiber.MethodHead || method == fiber.MethodOptions:
		ok = true
	case method != fiber.MethodPost:
	case path == "/api/logout" || path == "/api/account/login-link/cancel":
		ok = true
	case path == "/api/account/sessions/revoke-others":
		ok = true
	case strings.HasPrefix( path , "/api/account/sessions/" ) && strings.HasSuffix( path , "/revoke" ):
		ok = true
	}
	return
}

// signingIn is true for the public sign-in routes. A viewer's browser posting
// to one is not acting as the viewer, it is becoming someone (possibly
// someone else), so the view-only rule does not apply there.
func signingIn( path string ) ( result bool ) {
	result = path == "/api/login" || strings.HasPrefix( path , "/api/login/" ) || strings.HasPrefix( path , "/api/join/" )
	return
}

// ViewOnly is the viewer role's one guard, in front of every route: a
// viewer's session or key may read, and may send only what ViewerMayCall
// lets through. Anything else answers 403 {"error":"view_only"}, whatever
// the route, so a route an app adds later is covered without remembering to.
// /mcp is let through and holds each tool to the same rule (server/mcp).
func ( guard *Guard ) ViewOnly( c fiber.Ctx ) ( err error ) {
	actor := ActorFrom( c )
	path := c.Path()
	if actor == nil || actor.Role != models.RoleViewer || path == "/mcp" || signingIn( path ) || ViewerMayCall( c.Method() , path ) {
		err = c.Next()
		return
	}
	err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": "view_only" } )
	return
}

// RequireAPIKey is RequireSession's mirror: it refuses a browser session and
// lets only a key through. It guards /mcp, which is for agents and scripts.
// Taking only a header credential there means no ambient cookie can ride
// along on a request some other page caused, so the endpoint needs no CSRF
// token and the no-credentials rule for keys covers everything it can do.
func ( guard *Guard ) RequireAPIKey( c fiber.Ctx ) ( err error ) {
	actor := ActorFrom( c )
	if actor == nil {
		c.Set( fiber.HeaderWWWAuthenticate , "Bearer" )
		err = c.Status( fiber.StatusUnauthorized ).JSON( fiber.Map{ "error": "not authenticated" } )
		return
	}
	if actor.ViaAPIKey() == false {
		err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": "this endpoint requires an API key, not a signed-in session" } )
		return
	}
	err = c.Next()
	return
}

// RequireOwner lets through only the owner, signed in from a browser. It
// guards the activity log (routes/logs.go), which shows what every account
// did -- including the other admins, which is exactly why they cannot read
// it. Everyone else, any API key included, gets the 404 an unknown route
// gets: the Logs screen is not something to advertise.
//
// A session, not a key: the log holds every account's IP addresses and
// activity, and a key is a credential that lives in files. An app that wants
// the log readable by a script should add a narrow export, knowingly.
func ( guard *Guard ) RequireOwner( c fiber.Ctx ) ( err error ) {
	actor := ActorFrom( c )
	allowed := actor != nil && actor.ViaAPIKey() == false && actor.IsAdmin()
	if allowed {
		owner_id , owner_err := models.OwnerID( guard.Store )
		allowed = owner_err == nil && owner_id != 0 && actor.User.ID == owner_id
	}
	if allowed == false {
		err = c.Status( fiber.StatusNotFound ).JSON( fiber.Map{ "error": "not_found" } )
		return
	}
	err = c.Next()
	return
}

// StillOwner is RequireOwner for a stream, which is only checked by middleware
// when it connects. credential is the session it connected with. It does not
// count as activity: an open page re-checking is not the person being there.
func ( guard *Guard ) StillOwner( credential string ) ( ok bool ) {
	_ , user := models.LoadSession( guard.Store , credential )
	if user == nil || user.Role != models.RoleAdmin { return }
	owner_id , err := models.OwnerID( guard.Store )
	ok = err == nil && owner_id != 0 && user.ID == owner_id
	return
}

// LogActor is who an activity-log entry belongs to: the account, the role it
// acted with, and which door it came through (a key by its public id only).
func LogActor( actor *Actor ) ( result *logs.Actor ) {
	if actor == nil || actor.User == nil {
		result = logs.Anonymous()
		return
	}
	result = &logs.Actor{
		UserID: actor.User.ID,
		Name:   actor.User.DisplayName,
		Role:   actor.Role,
		Method: actor.Method(),
	}
	if actor.APIKey != nil {
		result.KeyID = actor.APIKey.ID
	}
	return
}

func ActorFrom( c fiber.Ctx ) ( actor *Actor ) {
	actor , _ = c.Locals( keyActor ).( *Actor )
	return
}

// UserFrom, SessionFrom, and APIKeyFrom are the narrow reads a handler
// usually wants. UserFrom is the account; ActorFrom is the account plus how
// it authenticated and what it may do, which is what a permission decision
// needs.
func UserFrom( c fiber.Ctx ) ( user *models.User ) {
	if actor := ActorFrom( c ); actor != nil {
		user = actor.User
	}
	return
}

func SessionFrom( c fiber.Ctx ) ( session *models.Session ) {
	if actor := ActorFrom( c ); actor != nil {
		session = actor.Session
	}
	return
}

func APIKeyFrom( c fiber.Ctx ) ( key *models.APIKey ) {
	if actor := ActorFrom( c ); actor != nil {
		key = actor.APIKey
	}
	return
}

// CheckCSRF guards state-changing requests. SameSite=Strict already blocks
// the common cross-site POST, but it is a browser-side promise: older
// browsers, odd cross-scheme cases, and non-browser clients do not all honour
// it. A per-session token echoed back in the JSON body costs nothing and does
// not depend on the browser behaving.
//
// A request authenticated by an API key is exempt, and that is not a hole.
// CSRF exists because a cookie is *ambient* -- the browser attaches it to a
// request some other site caused. Nothing attaches an Authorization header on
// its own; a forged cross-site request from an attacker's page carries no key,
// so there is nothing to forge with. Requiring a token here would instead mean
// every script had to fetch one first, for no gain.
func ( guard *Guard ) CheckCSRF( c fiber.Ctx , submitted string ) ( ok bool ) {
	actor := ActorFrom( c )
	if actor == nil { return }
	if actor.ViaAPIKey() {
		ok = true
		return
	}
	if actor.Session == nil || submitted == "" { return }
	ok = encryption.ConstantTimeEqual( actor.Session.CSRFToken , submitted )
	return
}
