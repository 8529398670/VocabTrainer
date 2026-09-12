// Invite links: the shareable, seat-limited counterpart to the one-time
// login links in admin.go. An admin mints one, posts it wherever the group
// already talks, and the first N people to open it get accounts.
//
// # Why opening a link and taking a seat are different requests
//
// The whole feature turns on this, so it is worth stating plainly: GET
// /join/<credential> and GET /api/invite/<credential> never change anything.
// A seat is only spent by POST /api/invite/claim.
//
// The reason is the use case. A link posted to a group chat is fetched, before
// any human sees it, by whatever is generating the preview card -- WhatsApp,
// Telegram, Slack, Discord, iMessage all do this, some from several IPs at
// once -- and again by browsers that prefetch links, and again by mail and
// security scanners. If opening the URL claimed a seat, a three-seat invite
// would frequently be empty by the time the first person tapped it, and the
// accounts it created would belong to nobody. Making the read safe and the
// write explicit is also just what the methods mean.
//
// It buys the join page too, which is where the person types the name they
// want. The existing login flow has no such step because an admin has
// already named the user; an invite has nobody to do that.
package routes

import (
	errors "errors"
	strings "strings"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

type createInviteRequest struct {
	Label     string `json:"label"`
	Role      string `json:"role"`
	MaxUses   int    `json:"max_uses"`
	CSRFToken string `json:"csrf_token"`
}

type claimInviteRequest struct {
	Credential  string `json:"credential"`
	DisplayName string `json:"display_name"`
}

// inviteReason turns a model error into a short code for the client, which
// looks the message up in language.yaml like every other string. The server
// deliberately does not send prose: text lives in one file, and this way the
// join page can word "this link is full" however the operator wants.
func inviteReason( err error ) ( reason string ) {
	switch {
	case err == nil:
		reason = ""
	case errors.Is( err , models.ErrInviteExpired ):
		reason = "expired"
	case errors.Is( err , models.ErrInviteRevoked ):
		reason = "revoked"
	case errors.Is( err , models.ErrInviteFull ):
		reason = "full"
	default:
		reason = "invalid"
	}
	return
}

// JoinPage serves the join screen for /join/<credential>.
//
// It needs its own route because the credential contains a ".", which the
// catch-all static handler would read as a file extension and answer 404 for.
// The page is the same static HTML for every invite, valid or not -- it asks
// the API about the credential once it is running, so nothing about the
// invite is baked into a cacheable response.
func ( handlers *Handlers ) JoinPage( c fiber.Ctx ) ( err error ) {
	err = handlers.Static.SendNamed( c , "join.html" )
	return
}

// GetInvite reports what the join page needs to render itself: whether the
// link works, how many seats are left, and why not if not. It spends nothing
// -- see the file header.
func ( handlers *Handlers ) GetInvite( c fiber.Ctx ) ( err error ) {
	credential := strings.Trim( c.Params( "*" ) , "/" )

	invite , inspect_err := models.InspectInvite( handlers.Store , credential )
	if invite == nil {
		// The secret did not check out, so there is nothing to describe and
		// no seat count to leak. Every wrong guess gets this same answer.
		err = c.JSON( fiber.Map{ "usable": false , "reason": "invalid" } )
		return
	}

	// Past here the caller holds the real link, so the detail is theirs to
	// see -- including the reason it will not work, which is the difference
	// between "ask for a new link" and "try again, you typed it wrong".
	err = c.JSON( fiber.Map{
		"usable":             inspect_err == nil,
		"reason":             inviteReason( inspect_err ),
		"seats_left":         invite.SeatsLeft(),
		"max_uses":           invite.MaxUses,
		"expires_in_seconds": int( time.Until( invite.ExpiresAt ).Seconds() ),
	} )
	return
}

// ClaimInvite is the signup this application otherwise does not have: it
// creates the account, spends the seat, and signs the person in, all from one
// request they made themselves.
//
// There is no CSRF token, and there is nothing to put one in: whoever is
// claiming has no session yet, so there is no per-session token to echo back.
// The credential in the body is the capability -- anyone able to forge this
// request from another origin would have to know the invite link already, at
// which point they can simply open it. What the check would protect against
// is a page making a visitor's browser burn a seat, and the honest answer is
// that the seat cap plus revocation is the mitigation there.
func ( handlers *Handlers ) ClaimInvite( c fiber.Ctx ) ( err error ) {
	var body claimInviteRequest
	if c.Bind().Body( &body ) != nil {
		err = badRequest( c , "malformed request body" )
		return
	}

	// Someone who is already signed in clicking the link in the group chat
	// is the common accident, not an attack. Spending a seat to give them a
	// second, empty account is the wrong answer to it -- the page tells them
	// they are already in.
	if security.UserFrom( c ) != nil {
		err = c.Status( fiber.StatusConflict ).JSON( fiber.Map{
			"error":  "already signed in",
			"reason": "signed_in",
		} )
		return
	}

	if models.ValidDisplayName( body.DisplayName ) == false {
		err = badRequest( c , "display name must be 1-80 characters" )
		return
	}

	user , claim_err := models.ClaimInvite( handlers.Store , body.Credential , body.DisplayName )
	if claim_err != nil {
		reason := inviteReason( claim_err )
		if reason == "invalid" && errors.Is( claim_err , models.ErrInviteInvalid ) == false {
			// A real failure (a write that did not land), not a bad link.
			// Reporting it as an invalid invite would send the person off to
			// ask for a new link that was never the problem.
			err = serverError( c )
			return
		}
		err = c.Status( fiber.StatusConflict ).JSON( fiber.Map{
			"error":  claim_err.Error(),
			"reason": reason,
		} )
		return
	}

	// From here it is the login flow verbatim -- see RedeemLoginLink. An
	// invite decides *whether* to let someone in; what a session is has one
	// implementation either way.
	credential_value , _ , session_err := models.CreateSession( handlers.Store , user.ID , handlers.Config.SessionTTL )
	if session_err != nil {
		err = serverError( c )
		return
	}
	if cookie_err := handlers.Guard.SetSessionCookie( c , credential_value ); cookie_err != nil {
		err = serverError( c )
		return
	}

	err = c.Status( fiber.StatusCreated ).JSON( fiber.Map{
		"ok":           true,
		"display_name": user.DisplayName,
	} )
	return
}

// ---------------------------------------------------------------------------
// admin
// ---------------------------------------------------------------------------

// CreateInvite mints the link. Like CreateUser it returns a path rather than a
// full URL, because behind a reverse proxy the server does not reliably know
// its own public origin and the browser always does.
func ( handlers *Handlers ) CreateInvite( c fiber.Ctx ) ( err error ) {
	var body createInviteRequest
	if c.Bind().Body( &body ) != nil {
		err = badRequest( c , "malformed request body" )
		return
	}
	if handlers.Guard.CheckCSRF( c , body.CSRFToken ) == false {
		err = forbidden( c , "invalid csrf token" )
		return
	}
	if models.ValidRole( body.Role ) == false {
		err = badRequest( c , "role must be admin or user" )
		return
	}
	if models.ValidInviteUses( body.MaxUses ) == false {
		err = badRequest( c , "number of uses must be between 1 and 100" )
		return
	}
	if models.ValidInviteLabel( body.Label ) == false {
		err = badRequest( c , "label must be 80 characters or fewer" )
		return
	}

	admin := security.UserFrom( c )
	_ , credential , issue_err := models.IssueInvite(
		handlers.Store , admin.ID , body.Role , body.Label , body.MaxUses , handlers.Config.InviteTTL )
	if issue_err != nil {
		err = serverError( c )
		return
	}

	err = c.Status( fiber.StatusCreated ).JSON( fiber.Map{
		"join_path":          "/join/" + credential,
		"max_uses":           body.MaxUses,
		"expires_in_seconds": int( handlers.Config.InviteTTL.Seconds() ),
	} )
	return
}

// ListInvites shows the live links and what has become of them. The
// credential is not among the fields and cannot be: only its hash was ever
// stored. An admin who has lost a link revokes it and mints another.
func ( handlers *Handlers ) ListInvites( c fiber.Ctx ) ( err error ) {
	records , list_err := models.ListInvites( handlers.Store )
	if list_err != nil {
		err = serverError( c )
		return
	}

	rows := []fiber.Map{}
	for _ , record := range records {
		usable_err := record.Invite.Usable()
		rows = append( rows , fiber.Map{
			"id":          record.ID,
			"label":       record.Invite.Label,
			"role":        record.Invite.Role,
			"max_uses":    record.Invite.MaxUses,
			"used_count":  record.Invite.UsedCount,
			"seats_left":  record.Invite.SeatsLeft(),
			"usable":      usable_err == nil,
			"reason":      inviteReason( usable_err ),
			"created_at":  record.Invite.CreatedAt,
			"expires_at":  record.Invite.ExpiresAt,
			"joined":      len( record.Invite.JoinedUserIDs ),
		} )
	}
	err = c.JSON( rows )
	return
}

// RevokeInvite is the one control a shareable link needs and a one-time link
// does not: the link is out there, and the only way to take it back is to
// stop honouring it.
func ( handlers *Handlers ) RevokeInvite( c fiber.Ctx ) ( err error ) {
	var body csrfOnlyRequest
	if c.Bind().Body( &body ) != nil {
		err = badRequest( c , "malformed request body" )
		return
	}
	if handlers.Guard.CheckCSRF( c , body.CSRFToken ) == false {
		err = forbidden( c , "invalid csrf token" )
		return
	}

	invite_id := c.Params( "invite_id" )
	if _ , get_err := models.GetInvite( handlers.Store , invite_id ); get_err != nil {
		err = notFound( c , "no such invite" )
		return
	}
	if models.RevokeInvite( handlers.Store , invite_id ) != nil {
		err = serverError( c )
		return
	}
	err = c.JSON( fiber.Map{ "ok": true } )
	return
}
