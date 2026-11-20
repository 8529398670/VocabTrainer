// Invite links: the shareable, seat-limited counterpart to the one-time
// login links in admin.go. An admin mints one, posts it wherever the group
// already talks, and the first N people to open it get accounts.
//
// # Why opening a link and taking a seat are different requests
//
// The whole feature turns on this, so it is worth stating plainly: GET
// /join/<credential> and POST /api/invite/info never change anything. A seat
// is only spent by POST /api/invite/claim.
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

	apidoc "vocabtrainer/server/apidoc"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

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
	// The credential is in this page's URL; no request the page makes should
	// carry it onward in a Referer header.
	c.Set( fiber.HeaderReferrerPolicy , "no-referrer" )
	err = handlers.Static.SendNamed( c , "join.html" )
	return
}

// joinElsewhere is what the join routes say to a request that did not come
// from this site's own join page.
const joinElsewhere = "joining must be done from this site's own join page"

type inviteCredential struct {
	Credential string `json:"credential" jsonschema:"minLength=1,maxLength=200" jsonschema_description:"The part of the invite link after /join/. A credential: treat it like a password." error:"credential is required"`
}

type inviteInfoReply struct {
	Usable           bool   `json:"usable"`
	Reason           string `json:"reason,omitempty" jsonschema:"enum=invalid,enum=expired,enum=revoked,enum=full" jsonschema_description:"Why it will not work, when it will not."`
	SeatsLeft        *int   `json:"seats_left,omitempty"`
	MaxUses          *int   `json:"max_uses,omitempty"`
	ExpiresInSeconds *int   `json:"expires_in_seconds,omitempty"`
}

// inviteInfo reports what the join page needs to render itself: whether the
// link works, how many seats are left, and why not if not. It spends nothing
// -- see the file header. A POST only so the credential is not in a URL.
//
// @route("POST /api/invite/info", summary="What an invite link offers",
//     tag="Sign-in", access=PUBLIC, use=[sameOrigin( joinElsewhere )],
// )
func ( handlers *Handlers ) inviteInfo( c fiber.Ctx , in *inviteCredential ) ( reply *inviteInfoReply , err error ) {
	credential := strings.Trim( strings.TrimSpace( in.Credential ) , "/" )
	invite , inspect_err := models.InspectInvite( handlers.Store , credential )
	if invite == nil {
		// The secret did not check out, so there is nothing to describe and
		// no seat count to leak. Every wrong guess gets this same answer.
		reply = &inviteInfoReply{ Usable: false , Reason: "invalid" }
		return
	}

	// Past here the caller holds the real link, so the detail is theirs to
	// see -- including the reason it will not work, which is the difference
	// between "ask for a new link" and "try again, you typed it wrong".
	seats_left := invite.SeatsLeft()
	max_uses := invite.MaxUses
	expires_in := int( time.Until( invite.ExpiresAt ).Seconds() )
	reply = &inviteInfoReply{
		Usable:           inspect_err == nil,
		Reason:           inviteReason( inspect_err ),
		SeatsLeft:        &seats_left,
		MaxUses:          &max_uses,
		ExpiresInSeconds: &expires_in,
	}
	return
}

type claimInviteRequest struct {
	inviteCredential
	DisplayName string `json:"display_name" jsonschema:"minLength=1,maxLength=80" error:"display name must be 1-80 characters"`
}

type claimInviteReply struct {
	Ok          bool   `json:"ok"`
	DisplayName string `json:"display_name"`
}

// claimInvite is the signup this application otherwise does not have: it
// creates the account, spends the seat, and signs the person in, all from one
// request they made themselves.
//
// There is no CSRF token, and there is nothing to put one in: whoever is
// claiming has no session yet. It is guarded the way the login route is:
// sameOrigin wants a JSON body from this site's own page, which a form or a
// cross-origin fetch cannot send. The credential in the body is the
// capability.
//
// A refusal carries reason, which join.js looks up in language.yaml:
// invalid, expired, revoked, full, or signed_in.
//
// @route("POST /api/invite/claim", summary="Join through an invite link",
//     tag="Sign-in", access=PUBLIC, status=201, errors=(409,), use=[sameOrigin( joinElsewhere )],
//     description="Makes an account with this name, takes one place on the link, and signs this browser in.",
// )
func ( handlers *Handlers ) claimInvite( c fiber.Ctx , in *claimInviteRequest ) ( reply *claimInviteReply , err error ) {
	// Someone who is already signed in clicking the link in the group chat
	// is the common accident, not an attack. Spending a seat to give them a
	// second, empty account is the wrong answer to it -- the page tells them
	// they are already in.
	if security.UserFrom( c ) != nil {
		err = refuse( fiber.StatusConflict , "already signed in" , fiber.Map{ "reason": "signed_in" } )
		return
	}
	if models.ValidDisplayName( in.DisplayName ) == false {
		err = refuse( fiber.StatusBadRequest , "display name must be 1-80 characters" )
		return
	}

	credential := strings.Trim( strings.TrimSpace( in.Credential ) , "/" )
	user , signed , claim_err := models.ClaimInvite( handlers.Store , credential , in.DisplayName , handlers.signInRules() )
	if claim_err != nil {
		reason := inviteReason( claim_err )
		if reason == "invalid" && errors.Is( claim_err , models.ErrInviteInvalid ) == false {
			// A real failure (a write that did not land), not a bad link.
			// Reporting it as an invalid invite would send the person off to
			// ask for a new link that was never the problem.
			err = claim_err
			return
		}
		handlers.auditAs( c , logs.Anonymous() , logs.KindAuth , logs.LevelWarn , "invite link refused" , fiber.Map{ "reason": reason } )
		err = refuse( fiber.StatusConflict , claim_err.Error() , fiber.Map{ "reason": reason } )
		return
	}

	handlers.auditAs( c , handlers.logUser( user.ID , security.MethodSession ) , logs.KindAuth , logs.LevelInfo , "joined through an invite link" , fiber.Map{ "name": user.DisplayName , "role": user.Role } )
	if _ , err = handlers.signIn( c , signed ); err != nil { return }
	reply = &claimInviteReply{ Ok: true , DisplayName: user.DisplayName }
	return
}

// ---------------------------------------------------------------------------
// admin
// ---------------------------------------------------------------------------

type createInviteRequest struct {
	Label   string `json:"label,omitempty" jsonschema:"maxLength=80" jsonschema_description:"A note to tell this link apart later." error:"label must be 80 characters or fewer"`
	Role    string `json:"role" jsonschema:"enum=admin,enum=user" error:"role must be admin or user"`
	MaxUses int    `json:"max_uses" jsonschema:"minimum=1,maximum=100" jsonschema_description:"How many people may join through it." error:"number of uses must be between 1 and 100"`
}

type createInviteReply struct {
	JoinPath         string `json:"join_path" jsonschema_description:"Path of the invite link. A credential: treat it like a password."`
	MaxUses          int    `json:"max_uses"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

// createInvite mints the link. Like createUser it returns a path rather than a
// full URL, because behind a reverse proxy the server does not reliably know
// its own public origin and the browser always does.
//
// @route("POST /api/admin/invites", summary="Make an invite link",
//     tag="Admin", role=ADMIN, status=201,
//     description="One link several people can join through, each making their own account. It lasts "
//         "auth.invite_ttl_seconds.",
// )
func ( handlers *Handlers ) createInvite( c fiber.Ctx , in *createInviteRequest ) ( reply *createInviteReply , err error ) {
	if models.ValidInviteUses( in.MaxUses ) == false {
		err = refuse( fiber.StatusBadRequest , "number of uses must be between 1 and 100" )
		return
	}
	if models.ValidInviteLabel( in.Label ) == false {
		err = refuse( fiber.StatusBadRequest , "label must be 80 characters or fewer" )
		return
	}
	admin := security.UserFrom( c )
	ttl := handlers.cfg().InviteTTL
	invite_id , credential , err := models.IssueInvite( handlers.Store , admin.ID , in.Role , in.Label , in.MaxUses , ttl )
	if err != nil { return }
	handlers.audit( c , logs.KindAdmin , "invite link created" , fiber.Map{ "invite_id": invite_id , "role": in.Role , "max_uses": in.MaxUses } )
	reply = &createInviteReply{
		JoinPath:         "/join/" + credential,
		MaxUses:          in.MaxUses,
		ExpiresInSeconds: int( ttl.Seconds() ),
	}
	return
}

type inviteRow struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Role      string    `json:"role"`
	MaxUses   int       `json:"max_uses"`
	UsedCount int       `json:"used_count"`
	SeatsLeft int       `json:"seats_left"`
	Usable    bool      `json:"usable"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Joined    int       `json:"joined"`
}

func ( inviteRow ) APIName() ( name string ) { name = "Invite"; return }

type invitesReply struct {
	Invites []inviteRow `json:"invites"`
}

// listInvites shows the links and what has become of them. The credential is
// not among the fields and cannot be: only its hash was ever stored. An admin
// who has lost a link revokes it and mints another.
//
// @route("GET /api/admin/invites", summary="The invite links",
//     tag="Admin", role=ADMIN,
// )
func ( handlers *Handlers ) listInvites( c fiber.Ctx , in *apidoc.None ) ( reply *invitesReply , err error ) {
	records , err := models.ListInvites( handlers.Store )
	if err != nil { return }
	reply = &invitesReply{ Invites: []inviteRow{} }
	for _ , record := range records {
		usable_err := record.Invite.Usable()
		reply.Invites = append( reply.Invites , inviteRow{
			ID:        record.ID,
			Label:     record.Invite.Label,
			Role:      record.Invite.Role,
			MaxUses:   record.Invite.MaxUses,
			UsedCount: record.Invite.UsedCount,
			SeatsLeft: record.Invite.SeatsLeft(),
			Usable:    usable_err == nil,
			Reason:    inviteReason( usable_err ),
			CreatedAt: record.Invite.CreatedAt,
			ExpiresAt: record.Invite.ExpiresAt,
			Joined:    len( record.Invite.JoinedUserIDs ),
		} )
	}
	return
}

type inviteIDPath struct {
	InviteID string `json:"invite_id" path:"invite_id" jsonschema:"minLength=1,maxLength=64" jsonschema_description:"The link's id, from GET /api/admin/invites." error:"invalid invite id"`
}

// revokeInvite is the one control a shareable link needs and a one-time link
// does not: the link is out there, and the only way to take it back is to
// stop honouring it.
//
// @route("POST /api/admin/invites/{invite_id}/revoke", summary="Switch an invite link off",
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="Nobody else can join through it. People who already joined keep their accounts.",
// )
func ( handlers *Handlers ) revokeInvite( c fiber.Ctx , in *inviteIDPath ) ( reply *okReply , err error ) {
	if _ , get_err := models.GetInvite( handlers.Store , in.InviteID ); get_err != nil {
		err = refuse( fiber.StatusNotFound , "no such invite" )
		return
	}
	if err = models.RevokeInvite( handlers.Store , in.InviteID ); err != nil { return }
	handlers.audit( c , logs.KindAdmin , "invite link revoked" , fiber.Map{ "invite_id": in.InviteID } )
	reply = &okReply{ Ok: true }
	return
}
