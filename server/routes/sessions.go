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

// sessionIDPath is the device a route under .../sessions/{session_id} acts on.
type sessionIDPath struct {
	SessionID string `json:"session_id" path:"session_id" jsonschema_description:"The device's id, from GET /api/account/sessions or GET /api/admin/users/{user_id}/sessions (list_my_sessions or admin_list_user_sessions over MCP)."`
}

// sessionRow picks out what the devices list shows. It names each field,
// rather than returning the record, so the secret's hash and the CSRF token
// cannot reach a response by way of a change to the model.
type sessionRow struct {
	ID         string    `json:"id"`
	Current    bool      `json:"current"`
	CreatedAt  time.Time `json:"created_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	LastSeenAt time.Time `json:"last_seen_at,omitzero"`
	IP         string    `json:"ip,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"`
	Label      string    `json:"label,omitempty" jsonschema_description:"The name you gave this device."`
}

func ( sessionRow ) APIName() ( name string ) { name = "Session"; return }

func newSessionRow( session *models.Session , current_id string ) ( row sessionRow ) {
	row = sessionRow{
		ID:        session.ID,
		Current:   session.ID == current_id,
		CreatedAt: session.CreatedAt,
		ExpiresAt: session.ExpiresAt,
		IP:        session.IP,
		UserAgent: session.UserAgent,
		Label:     session.Label,
	}
	if session.LastSeenAt != nil { row.LastSeenAt = *session.LastSeenAt }
	return
}

type sessionsReply struct {
	Sessions []sessionRow `json:"sessions"`
	Paged
}

// currentSessionID is the session this request came with, or "" through an
// API key, which has none: nothing in a list is then "current", and nothing
// is refused as "this device".
func currentSessionID( c fiber.Ctx ) ( result string ) {
	if actor := security.ActorFrom( c ); actor.Session != nil { result = actor.Session.ID }
	return
}

// listMySessions answers "where am I signed in". Only ever the caller's own:
// an admin looks at someone else's through ListUserSessions.
//
// @route("GET /api/account/sessions", summary="Your signed-in devices",
//     tag="Account", feature="sessions", errors=(403,),
//     description="Where your account is signed in, a page at a time: id, when it started and was last "
//         "seen, the browser, and the name you gave it. Through an API key no device is current. next is "
//         "the offset of the following page, or null on the last one.",
// )
func ( handlers *Handlers ) listMySessions( c fiber.Ctx , in *Page ) ( reply *sessionsReply , err error ) {
	actor := security.ActorFrom( c )
	sessions , err := models.ListSessionsForUser( handlers.Store , actor.User.ID )
	if err != nil { return }
	rows := []sessionRow{}
	for _ , session := range sessions {
		rows = append( rows , newSessionRow( session , currentSessionID( c ) ) )
	}
	reply = &sessionsReply{}
	reply.Sessions , reply.Paged = cutPage( rows , *in )
	return
}

// sessionsTarget reads whose sessions an admin route is asking about. Nobody but
// the owner reaches the owner's, the same rule as revoking the owner's access.
func ( handlers *Handlers ) sessionsTarget( c fiber.Ctx , user_id uint64 ) ( by_owner bool , refused *refusal ) {
	if _ , get_err := models.GetUser( handlers.Store , user_id ); get_err != nil {
		refused = refuse( fiber.StatusNotFound , "no such user" )
		return
	}
	owner_id , owner_err := models.OwnerID( handlers.Store )
	if owner_err != nil {
		refused = internalFailure( owner_err )
		return
	}
	by_owner = security.UserFrom( c ).ID == owner_id
	if user_id == owner_id && by_owner == false {
		refused = refuse( fiber.StatusForbidden , "the owner account's sessions are the owner's own" )
	}
	return
}

type userSessionsRequest struct {
	userIDPath
	Page
}

// listUserSessions shows an admin where an account is signed in, for ending one
// device rather than everything with revoke-access. Addresses are for the owner
// only, as in the activity log, which keeps them from other admins too.
//
// @route("GET /api/admin/users/{user_id}/sessions", summary="An account's signed-in devices",
//     tag="Admin", role=ADMIN, feature="sessions", errors=(404,),
//     description="Where an account is signed in: every session, with when it started and was last seen. "
//         "Addresses are shown to the owner only, and only the owner sees the owner's.",
// )
func ( handlers *Handlers ) listUserSessions( c fiber.Ctx , in *userSessionsRequest ) ( reply *sessionsReply , err error ) {
	by_owner , refused := handlers.sessionsTarget( c , in.UserID )
	if refused != nil {
		err = refused
		return
	}
	sessions , err := models.ListSessionsForUser( handlers.Store , in.UserID )
	if err != nil { return }
	rows := []sessionRow{}
	for _ , session := range sessions {
		row := newSessionRow( session , currentSessionID( c ) )
		if by_owner == false { row.IP = "" }
		rows = append( rows , row )
	}
	reply = &sessionsReply{}
	reply.Sessions , reply.Paged = cutPage( rows , in.Page )
	return
}

type userSessionRequest struct {
	userIDPath
	sessionIDPath
}

// revokeUserSession signs one of an account's devices out, and only that.
//
// @route("POST /api/admin/users/{user_id}/sessions/{session_id}/revoke", summary="Sign out one of an account's devices",
//     bulk=True,
//     tag="Admin", role=ADMIN, feature="sessions", errors=(404,),
// )
func ( handlers *Handlers ) revokeUserSession( c fiber.Ctx , in *userSessionRequest ) ( reply *okReply , err error ) {
	if _ , refused := handlers.sessionsTarget( c , in.UserID ); refused != nil {
		err = refused
		return
	}
	if in.SessionID != "" && in.SessionID == currentSessionID( c ) {
		err = refuse( fiber.StatusBadRequest , "use sign out for this device" )
		return
	}
	revoke_err := models.RevokeSessionAs( handlers.Store , in.UserID , security.UserFrom( c ).ID , in.SessionID )
	switch {
	case errors.Is( revoke_err , models.ErrSessionNotFound ):
		err = refuse( fiber.StatusNotFound , "not found" )
	case revoke_err != nil:
		err = actOnRefusal( revoke_err , "end a session of" )
	default:
		handlers.audit( c , logs.KindAdmin , "session revoked" , fiber.Map{ "user_id": in.UserID } )
		reply = &okReply{ Ok: true }
	}
	return
}

type labelSessionRequest struct {
	sessionIDPath
	Label  string  `json:"label" jsonschema:"maxLength=80" jsonschema_description:"At most 80 characters." error:"a device name can be at most 80 characters"`
	Expect *string `json:"expect,omitempty" jsonschema_description:"The name you think it has. Answers 409 if it has another."`
}

type labelSessionReply struct {
	Ok    bool   `json:"ok"`
	Label string `json:"label"`
}

// labelMySession names one of your own devices, this one included. An empty
// label clears the name. Admins see a label but cannot set one: it is what the
// person calls their own device.
//
// @route("POST /api/account/sessions/{session_id}/label", summary="Name one of your devices",
//     tag="Account", feature="sessions", errors=(404, 409),
//     description="Give one of your devices a name (at most 80 characters; empty clears it). expect, when "
//         "given, is the name you think it has now.",
// )
func ( handlers *Handlers ) labelMySession( c fiber.Ctx , in *labelSessionRequest ) ( reply *labelSessionReply , err error ) {
	if models.ValidSessionLabel( in.Label ) == false {
		err = refuse( fiber.StatusBadRequest , "a device name can be at most 80 characters" )
		return
	}
	label_err := models.LabelSession( handlers.Store , security.UserFrom( c ).ID , in.SessionID , in.Label , in.Expect )
	var stale *models.RenameConflictError
	switch {
	case errors.As( label_err , &stale ):
		err = refuse( fiber.StatusConflict , stale.Error() , fiber.Map{ "label": stale.Current } )
	case errors.Is( label_err , models.ErrSessionNotFound ):
		err = refuse( fiber.StatusNotFound , "not found" )
	case label_err != nil:
		err = label_err
	default:
		// Not the label: it is the person's own words about where they are.
		handlers.audit( c , logs.KindAuth , "session_labelled" , nil )
		reply = &labelSessionReply{ Ok: true , Label: strings.TrimSpace( in.Label ) }
	}
	return
}

// revokeMySession signs out one other device. This device is refused: ending
// it here would leave the browser holding a dead cookie, and signing out is
// what the sign-out button is for. Through an API key there is no "this
// device", so any of them may go.
//
// @route("POST /api/account/sessions/{session_id}/revoke", summary="Sign out one of your devices",
//     bulk=True,
//     tag="Account", feature="sessions", errors=(404,),
//     description="End one of your signed-in sessions; that browser has to sign in again.",
// )
func ( handlers *Handlers ) revokeMySession( c fiber.Ctx , in *sessionIDPath ) ( reply *okReply , err error ) {
	if in.SessionID != "" && in.SessionID == currentSessionID( c ) {
		err = refuse( fiber.StatusBadRequest , "use sign out for this device" )
		return
	}
	revoke_err := models.RevokeSession( handlers.Store , security.UserFrom( c ).ID , in.SessionID )
	switch {
	case errors.Is( revoke_err , models.ErrSessionNotFound ):
		err = refuse( fiber.StatusNotFound , "not found" )
	case revoke_err != nil:
		err = revoke_err
	default:
		handlers.audit( c , logs.KindAuth , "session_revoked" , nil )
		reply = &okReply{ Ok: true }
	}
	return
}

type removedReply struct {
	Ok      bool `json:"ok"`
	Removed int  `json:"removed"`
}

// revokeMyOtherSessions is the button for a lost phone or a shared computer:
// everything but this device. Through an API key, which is no device, that is
// every session the account has.
//
// @route("POST /api/account/sessions/revoke-others", summary="Sign out every other device",
//     tag="Account", feature="sessions",
// )
func ( handlers *Handlers ) revokeMyOtherSessions( c fiber.Ctx , in *apidoc.None ) ( reply *removedReply , err error ) {
	removed , err := models.RevokeOtherSessions( handlers.Store , security.UserFrom( c ).ID , currentSessionID( c ) )
	if err != nil { return }
	handlers.audit( c , logs.KindAuth , "sessions_revoked" , fiber.Map{ "removed": removed } )
	reply = &removedReply{ Ok: true , Removed: removed }
	return
}
