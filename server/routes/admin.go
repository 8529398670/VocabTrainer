package routes

import (
	errors "errors"
	strings "strings"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	db "vocabtrainer/server/db"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

// userIDPath is the account a route under /api/admin/users/{user_id} acts on.
type userIDPath struct {
	UserID uint64 `json:"user_id" path:"user_id" jsonschema:"minimum=1" jsonschema_description:"The account's id, from GET /api/admin/users (admin_list_users over MCP)." error:"invalid user id"`
}

// userRow is an account as the admin screen lists it. ListUsers marks the
// owner on its row, so the UI can tell which delete, disable, and revoke
// buttons the server would refuse -- see models/owner.go.
type userRow struct {
	ID              uint64                `json:"id"`
	DisplayName     string                `json:"display_name"`
	Role            string                `json:"role" jsonschema:"enum=admin,enum=user,enum=viewer"`
	CreatedAt       time.Time             `json:"created_at"`
	Disabled        bool                  `json:"disabled" jsonschema_description:"Switched off, or its expiry has passed."`
	ExpiresAt       *time.Time            `json:"expires_at" jsonschema_description:"When the account stops working, or null for never."`
	Expired         bool                  `json:"expired" jsonschema_description:"Whether expires_at has passed."`
	Owner           bool                  `json:"owner"`
	LoginLink       *models.LoginLinkInfo `json:"login_link" jsonschema_description:"The account's live login link, or null."`
}

func ( userRow ) APIName() ( name string ) { name = "User"; return }

type listUsersRequest struct {
	Page
	Q    string `json:"q,omitempty" query:"q" jsonschema_description:"Keep accounts whose name or username contains this, in any case."`
	Sort string `json:"sort,omitempty" query:"sort" jsonschema:"enum=display_name,enum=role,enum=disabled,enum=created_at" jsonschema_description:"Field to sort the whole list by. Rows without it go last."`
	Direction
}

type listUsersReply struct {
	Users []userRow `json:"users"`
	Paged
}

// listUsers is every account, a page at a time. ?q= keeps the accounts whose
// name or username contains it, in any case. Here and not in the browser,
// which only ever holds one page of them.
//
// @route("GET /api/admin/users", summary="List users",
//     tag="Admin", role=ADMIN,
//     description="Accounts, a page at a time, including disabled ones, with each one's role and which is "
//         "the owner. next is the offset of the following page, or null on the last one.",
// )
func ( handlers *Handlers ) listUsers( c fiber.Ctx , in *listUsersRequest ) ( reply *listUsersReply , err error ) {
	rows , err := handlers.userRowsMatching( in.Q )
	if err != nil { return }
	reply = &listUsersReply{}
	reply.Users , reply.Paged = sortedPage( rows , in.Sort , in.Direction , in.Page )
	return
}

func ( handlers *Handlers ) userRowsMatching( query string ) ( rows []userRow , err error ) {
	users , err := models.ListUsers( handlers.Store )
	if err != nil { return }
	owner_id , err := models.OwnerID( handlers.Store )
	if err != nil { return }
	links , err := models.LiveLoginLinks( handlers.Store )
	if err != nil { return }
	query = strings.ToLower( strings.TrimSpace( query ) )
	rows = []userRow{}
	for _ , user := range users {
		matched := strings.Contains( strings.ToLower( user.DisplayName ) , query )
		if matched == false { continue }
		var login_link *models.LoginLinkInfo
		if found , has := links[ user.ID ]; has { login_link = &found }
		rows = append( rows , userRow{
			ID:              user.ID,
			DisplayName:     user.DisplayName,
			Role:            user.Role,
			CreatedAt:       user.CreatedAt,
			Disabled:        user.Disabled(),
			ExpiresAt:       user.ExpiresAt,
			Expired:         user.Expired(),
			Owner:           user.ID == owner_id,
			LoginLink:       login_link,
		} )
	}
	return
}

// loginLinkOptions fills a request's gaps from config and checks it, and notes
// the API key asking, if one is, so revoking that key voids the link.
func ( handlers *Handlers ) loginLinkOptions( c fiber.Ctx , request models.LoginLinkRequest ) ( options models.LoginLinkOptions , refused *refusal ) {
	options , resolve_err := models.ResolveLoginLink( request , handlers.cfg().LoginTokenTTL , handlers.cfg().MultiUseLoginTokenTTL )
	if resolve_err != nil {
		refused = refuse( fiber.StatusBadRequest , resolve_err.Error() )
		return
	}
	options.MintedByKey = security.ActorFrom( c ).KeyID()
	return
}

// loginLinkReply is what every route that mints a link answers. The link is
// returned as a path, not a full URL (see CreateUser); the MCP tools add
// login_url from the address the agent reached /mcp at.
type loginLinkReply struct {
	LoginPath        string     `json:"login_path" jsonschema_description:"Path of the login link. A credential: treat it like a password."`
	ExpiresInSeconds int        `json:"expires_in_seconds" jsonschema_description:"0 when it never expires."`
	ExpiresAt        *time.Time `json:"expires_at" jsonschema_description:"null when it never expires."`
	MaxUses          int        `json:"max_uses" jsonschema_description:"0 when unlimited."`
	Unlimited        bool       `json:"unlimited"`
}

func newLoginLinkReply( credential string , info models.LoginLinkInfo ) ( reply loginLinkReply ) {
	expires_in := 0
	if info.ExpiresAt != nil { expires_in = int( time.Until( *info.ExpiresAt ).Seconds() + 0.5 ) }
	reply = loginLinkReply{
		LoginPath:        "/login/" + credential,
		ExpiresInSeconds: expires_in,
		ExpiresAt:        info.ExpiresAt,
		MaxUses:          info.MaxUses,
		Unlimited:        info.Unlimited,
	}
	return
}

// loginLinkRequest is the body of every route that mints a login link for
// an existing account: how many times it may be used, and for how long. Both
// optional (see models.LoginLinkRequest).
type loginLinkRequest struct {
	userIDPath
	models.LoginLinkRequest
}

type createUserRequest struct {
	DisplayName string `json:"display_name" jsonschema:"minLength=1,maxLength=80" error:"display name must be 1-80 characters"`
	Role        string `json:"role" jsonschema:"enum=admin,enum=user,enum=viewer" error:"role must be admin, user or viewer"`
	// AccountExpiresAt is when the account stops working. expires_in_minutes
	// is the login link's expiry, not the account's.
	AccountExpiresAt *time.Time `json:"account_expires_at,omitempty" jsonschema_description:"When the account stops working (RFC 3339). Left out: never. expires_in_minutes is the login link's expiry, not this." error:"account_expires_at must be an RFC 3339 time"`
	models.LoginLinkRequest
}

type createUserReply struct {
	UserID uint64 `json:"user_id"`
	loginLinkReply
}

// createUser is the signup process: an admin names someone, and gets back a
// login link to hand them -- once by default, or reusable (see
// models.LoginLinkRequest). The link is returned as a path, not a full URL
// -- the browser knows the origin it is talking to, whereas the server behind
// a reverse proxy often does not, and a login link with the wrong host is
// worse than useless.
//
// @route("POST /api/admin/users", summary="Create an account",
//     tag="Admin", role=ADMIN, status=201,
//     description="Make a new account and its first login link, to hand to the person. role is admin, "
//         "user or viewer (reads only). account_expires_at, when given, is when the account stops working. "
//         "Answers with user_id and the link.",
// )
func ( handlers *Handlers ) createUser( c fiber.Ctx , in *createUserRequest ) ( reply *createUserReply , err error ) {
	if models.ValidDisplayName( in.DisplayName ) == false {
		err = refuse( fiber.StatusBadRequest , "display name must be 1-80 characters" )
		return
	}
	options , refused := handlers.loginLinkOptions( c , in.LoginLinkRequest )
	if refused != nil {
		err = refused
		return
	}
	user , create_err := models.CreateUserExpiring( handlers.Store , in.DisplayName , in.Role , in.AccountExpiresAt )
	if errors.Is( create_err , models.ErrExpiryPast ) {
		err = refuse( fiber.StatusBadRequest , "account_expires_at must be in the future" )
		return
	}
	if create_err != nil {
		err = create_err
		return
	}
	credential , info , err := models.IssueLoginLink( handlers.Store , user.ID , options )
	if err != nil { return }
	handlers.audit( c , logs.KindAdmin , "user created" , fiber.Map{ "user_id": user.ID , "name": user.DisplayName , "role": user.Role , "expires_at": user.ExpiresAt } )
	reply = &createUserReply{ UserID: user.ID , loginLinkReply: newLoginLinkReply( credential , info ) }
	return
}

// reissueLogin covers the only real failure mode of this auth model: someone
// lost their cookie or their device, and there is no password reset to fall
// back on. An admin mints a fresh link instead. Any link the account could
// still sign in with stops working (see models.IssueLoginLink).
//
// It is not a reset: sessions the account already has stay signed in. When
// the worry is that someone else holds the account's credentials, use
// RevokeAccess first, then reissue.
//
// @route("POST /api/admin/users/{user_id}/reissue-login", summary="A new login link for an account",
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="For someone who lost their device or their link: a fresh login link for their account.",
// )
func ( handlers *Handlers ) reissueLogin( c fiber.Ctx , in *loginLinkRequest ) ( reply *loginLinkReply , err error ) {
	if _ , get_err := models.GetUser( handlers.Store , in.UserID ); get_err != nil {
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	}
	options , refused := handlers.loginLinkOptions( c , in.LoginLinkRequest )
	if refused != nil {
		err = refused
		return
	}
	options.ActorID = security.UserFrom( c ).ID
	credential , info , token_err := models.IssueLoginLink( handlers.Store , in.UserID , options )
	if refused = actOnRefusal( token_err , "reissue a login link for" ); refused != nil {
		err = refused
		return
	}
	handlers.audit( c , logs.KindAdmin , "login link reissued" , fiber.Map{ "user_id": in.UserID , "max_uses": info.MaxUses , "expires_at": info.ExpiresAt } )
	made := newLoginLinkReply( credential , info )
	reply = &made
	return
}

type cancelLoginLinkReply struct {
	Ok      bool `json:"ok"`
	Removed int  `json:"removed" jsonschema_description:"Links removed: 0 or 1."`
}

// cancelLoginLink stops an account's live login link working, without
// making a new one: a reusable link that went somewhere it should not have.
// Sessions it already made stay signed in; RevokeAccess ends those too.
//
// An API key may do this, like RevokeAccess: it only takes a way in away.
//
// @route("POST /api/admin/users/{user_id}/cancel-login-link", summary="Stop an account's login link working",
//     bulk=True,
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="Void an account's live login link without making a new one: a link that went somewhere "
//         "it should not have.",
// )
func ( handlers *Handlers ) cancelLoginLink( c fiber.Ctx , in *userIDPath ) ( reply *cancelLoginLinkReply , err error ) {
	if _ , get_err := models.GetUser( handlers.Store , in.UserID ); get_err != nil {
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	}
	removed , cancel_err := models.CancelLoginLink( handlers.Store , in.UserID , security.UserFrom( c ).ID )
	if refused := actOnRefusal( cancel_err , "cancel the login link of" ); refused != nil {
		err = refused
		return
	}
	handlers.audit( c , logs.KindAdmin , "login link cancelled" , fiber.Map{ "user_id": in.UserID , "removed": removed } )
	reply = &cancelLoginLinkReply{ Ok: true , Removed: removed }
	return
}

type renameUserRequest struct {
	userIDPath
	DisplayName string  `json:"display_name" jsonschema:"minLength=1,maxLength=80" jsonschema_description:"1-80 characters." error:"display name must be 1-80 characters"`
	Expect      *string `json:"expect,omitempty" jsonschema_description:"The name you think they have. Answers 409 if it is another."`
}

type renameUserReply struct {
	Ok          bool   `json:"ok"`
	DisplayName string `json:"display_name"`
}

// renameUser is an admin renaming someone else. Who may rename whom lives in
// models.RenameUserAs.
//
// @route("POST /api/admin/users/{user_id}/rename", summary="Rename someone",
//     tag="Admin", role=ADMIN, errors=(404, 409),
//     description="Change another account's display name (1-80 characters). Only the owner renames an "
//         "admin. expect, when given, is the name you think it has now; if it changed meanwhile the rename "
//         "is refused with the current name.",
// )
func ( handlers *Handlers ) renameUser( c fiber.Ctx , in *renameUserRequest ) ( reply *renameUserReply , err error ) {
	if models.ValidDisplayName( in.DisplayName ) == false {
		err = refuse( fiber.StatusBadRequest , "display name must be 1-80 characters" )
		return
	}
	rename_err := models.RenameUserAs( handlers.Store , in.UserID , security.UserFrom( c ).ID , in.DisplayName , in.Expect )
	var stale *models.RenameConflictError
	switch {
	case errors.As( rename_err , &stale ):
		err = refuse( fiber.StatusConflict , stale.Error() , fiber.Map{ "display_name": stale.Current } )
		return
	case errors.Is( rename_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	case errors.Is( rename_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can rename an admin" )
		return
	case rename_err != nil:
		err = rename_err
		return
	}
	name := strings.TrimSpace( in.DisplayName )
	handlers.audit( c , logs.KindAdmin , "user renamed" , fiber.Map{ "user_id": in.UserID , "name": name } )
	reply = &renameUserReply{ Ok: true , DisplayName: name }
	return
}

type setUserRoleRequest struct {
	userIDPath
	Role string `json:"role" jsonschema:"enum=admin,enum=user,enum=viewer" error:"role must be admin, user or viewer"`
}

type setUserRoleReply struct {
	Ok   bool   `json:"ok"`
	Role string `json:"role"`
}

// setUserRole makes an account an admin, an ordinary user or a viewer. Who may
// do what lives in models.SetUserRole.
//
// @route("POST /api/admin/users/{user_id}/role", summary="Change an account's role",
//     bulk=True,
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="Make an account an admin, an ordinary user, or a viewer, who may read but change "
//         "nothing. Only the owner takes an admin's rights away; the owner is always an admin, and nobody "
//         "changes their own role. Becoming an admin ends the account's sessions and live login link.",
// )
func ( handlers *Handlers ) setUserRole( c fiber.Ctx , in *setUserRoleRequest ) ( reply *setUserRoleReply , err error ) {
	changed , role_err := models.SetUserRole( handlers.Store , in.UserID , security.UserFrom( c ).ID , in.Role )
	switch {
	case errors.Is( role_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	case errors.Is( role_err , models.ErrRoleSelf ):
		err = refuse( fiber.StatusBadRequest , role_err.Error() )
		return
	case errors.Is( role_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "the owner is always an admin" )
		return
	case errors.Is( role_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can take away an admin's rights" )
		return
	case role_err != nil:
		err = role_err
		return
	}
	if changed {
		handlers.audit( c , logs.KindAdmin , "user role changed" , fiber.Map{ "user_id": in.UserID , "role": in.Role } )
	}
	reply = &setUserRoleReply{ Ok: true , Role: in.Role }
	return
}

type setUserDisabledRequest struct {
	userIDPath
	Disabled bool `json:"disabled" error:"disabled must be true or false"`
}

type setUserExpiryRequest struct {
	userIDPath
	ExpiresAt *time.Time `json:"expires_at" jsonschema_description:"When the account stops working (RFC 3339), or null for never." error:"expires_at must be an RFC 3339 time or null"`
}

type setUserExpiryReply struct {
	Ok        bool       `json:"ok"`
	ExpiresAt *time.Time `json:"expires_at" jsonschema_description:"null when it never expires."`
}

// setUserExpiry sets when an account stops working. Who may do it lives in
// models.SetUserExpiry.
//
// @route("POST /api/admin/users/{user_id}/expiry", summary="Set when an account expires",
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="expires_at is when the account stops working (RFC 3339), or null for never. Once it "
//         "passes the account counts as disabled: sign-in, its sessions and its keys stop, and housekeeping "
//         "disables it and revokes its access. A later expiry does not re-enable an account housekeeping "
//         "already disabled. It must be in the future. The owner's account never expires; only the owner "
//         "sets an admin's.",
// )
func ( handlers *Handlers ) setUserExpiry( c fiber.Ctx , in *setUserExpiryRequest ) ( reply *setUserExpiryReply , err error ) {
	user , set_err := models.SetUserExpiry( handlers.Store , in.UserID , security.UserFrom( c ).ID , in.ExpiresAt )
	switch {
	case errors.Is( set_err , models.ErrExpiryPast ):
		err = refuse( fiber.StatusBadRequest , set_err.Error() )
		return
	case errors.Is( set_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "the owner's account cannot expire" )
		return
	case set_err != nil:
		err = actOnRefusal( set_err , "set the expiry of" )
		return
	}
	handlers.audit( c , logs.KindAdmin , "user expiry set" , fiber.Map{ "user_id": in.UserID , "expires_at": user.ExpiresAt } )
	reply = &setUserExpiryReply{ Ok: true , ExpiresAt: user.ExpiresAt }
	return
}

// okReply is the answer of a route that has nothing to say but yes.
type okReply struct {
	Ok bool `json:"ok"`
}

// setUserDisabled switches an account off, or back on.
//
// @route("POST /api/admin/users/{user_id}/disabled", summary="Disable or enable a user",
//     bulk=True,
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="Switch an account off (disabled: true) or back on (false). Disabling signs it out "
//         "everywhere and voids its live login link; its API keys stop working while it is off. "
//         "Reversible. The owner and your own account cannot be disabled.",
// )
func ( handlers *Handlers ) setUserDisabled( c fiber.Ctx , in *setUserDisabledRequest ) ( reply *okReply , err error ) {
	if refused := handlers.applyUserDisabled( security.UserFrom( c ).ID , in.UserID , in.Disabled ); refused != nil {
		err = refused
		return
	}
	handlers.audit( c , logs.KindAdmin , "user disabled set" , fiber.Map{ "user_id": in.UserID , "disabled": in.Disabled } )
	reply = &okReply{ Ok: true }
	return
}

type revokeAccessReply struct {
	Ok      bool           `json:"ok"`
	Revoked models.Revoked `json:"revoked"`
}

// revokeAccess is the response to "this account may be compromised": every
// session, live login link, and live API key it holds stops working at
// once. The account itself stays enabled; reissue a login link to let its
// owner back in. See models.RevokeAllCredentials for why this is separate
// from both disabling and reissuing.
//
// @route("POST /api/admin/users/{user_id}/revoke-access", summary="Revoke a user's access",
//     bulk=True,
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="For an account that may be compromised: ends every session and voids its live login "
//         "link and every API key it holds, at once.",
// )
func ( handlers *Handlers ) revokeAccess( c fiber.Ctx , in *userIDPath ) ( reply *revokeAccessReply , err error ) {
	revoked , refused := handlers.revokeCredentials( security.UserFrom( c ).ID , in.UserID )
	if refused != nil {
		err = refused
		return
	}
	handlers.audit( c , logs.KindAdmin , "access revoked" , fiber.Map{ "user_id": in.UserID } )
	reply = &revokeAccessReply{ Ok: true , Revoked: revoked }
	return
}


type deleteUserReply struct {
	Ok      bool           `json:"ok"`
	Removed models.Revoked `json:"removed"`
}

// deleteUser removes an account for good, together with every session, login
// link, and API key it held. Disabling is the reversible version; this is for
// an account that should not exist at all.
//
// Any admin may delete an ordinary user. Deleting an admin takes the owner,
// and nobody deletes the owner or themselves -- those rules live in
// models.DeleteUser, checked inside its transaction. An admin-scoped API key
// may do it too, and the MCP tool is marked destructive so a client asks
// first.
//
// @route("POST /api/admin/users/{user_id}/delete", summary="Delete an account",
//     bulk=True,
//     tag="Admin", role=ADMIN, errors=(404,),
//     description="Remove an account for good, with its sessions, login link and API keys.",
// )
func ( handlers *Handlers ) deleteUser( c fiber.Ctx , in *userIDPath ) ( reply *deleteUserReply , err error ) {
	removed , delete_err := models.DeleteUser( handlers.Store , in.UserID , security.UserFrom( c ).ID )
	switch {
	case delete_err == nil:
		handlers.audit( c , logs.KindAdmin , "user deleted" , fiber.Map{ "user_id": in.UserID } )
		reply = &deleteUserReply{ Ok: true , Removed: removed }
	case errors.Is( delete_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such user" )
	case errors.Is( delete_err , models.ErrDeleteSelf ):
		err = refuse( fiber.StatusBadRequest , "you cannot delete your own account" )
	case errors.Is( delete_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "the owner account cannot be deleted" )
	case errors.Is( delete_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can delete an admin account" )
	default:
		err = delete_err
	}
	return
}

// refusal is a rule saying no, kept apart from how it is delivered: the HTTP
// handlers send it as a status and an error body, the MCP tools as a tool
// error with the same words. Writing the rule once is what keeps the two
// doors from drifting apart.
type refusal struct {
	status  int
	message string
	// cause is why a 5xx happened. It goes to the log, never to the caller.
	cause error
	// extra is more for the caller beside the message, such as the current
	// value a 409 was about.
	extra fiber.Map
}

// Error, Send and Refused make a refusal what a declared route's handler
// returns (see apidoc.Sender).
func ( refused *refusal ) Error() ( text string ) {
	text = refused.message
	if refused.cause != nil { text = refused.cause.Error() }
	return
}

func ( refused *refusal ) Send( c fiber.Ctx ) ( err error ) {
	err = refused.send( c )
	return
}

func ( refused *refusal ) Refused() ( status int , message string ) {
	status , message = refused.status , refused.message
	return
}

func ( refused *refusal ) send( c fiber.Ctx ) ( err error ) {
	if refused.status >= 500 {
		err = serverError( c , refused.cause )
		return
	}
	body := fiber.Map{ "error": refused.message }
	for name , value := range refused.extra {
		body[ name ] = value
	}
	err = c.Status( refused.status ).JSON( body )
	return
}

// refuse is a refusal with the caller's mistake in it. internalFailure is
// ours: a 500 whose cause goes to the log.
func refuse( status int , message string , extra ...fiber.Map ) ( refused *refusal ) {
	refused = &refusal{ status: status , message: message }
	if len( extra ) > 0 { refused.extra = extra[ 0 ] }
	return
}

func internalFailure( cause error ) ( refused *refusal ) {
	refused = &refusal{ status: fiber.StatusInternalServerError , message: "internal error" , cause: cause }
	return
}

// actOnRefusal turns an error from a model that checked models.mayActOnTx
// into the answer every account route gives: 404 for a missing account, 403
// with the same wording for the owner rule, 500 for anything else. what is
// the action, worded to follow "cannot": "reissue a login link for".
func actOnRefusal( err error , what string ) ( refused *refusal ) {
	switch {
	case err == nil:
	case errors.Is( err , db.ErrNotFound ):
		refused = refuse( fiber.StatusNotFound , "no such user" )
	case errors.Is( err , models.ErrOwnerProtected ):
		refused = refuse( fiber.StatusForbidden , "nobody can " + what + " the owner account" )
	case errors.Is( err , models.ErrOwnerRequired ):
		refused = refuse( fiber.StatusForbidden , "only the owner can " + what + " an admin account" )
	default:
		refused = internalFailure( err )
	}
	return
}

// applyUserDisabled carries every rule for switching an account off or on, for
// SetUserDisabled and the MCP admin_set_user_disabled tool.
func ( handlers *Handlers ) applyUserDisabled( self_id uint64 , user_id uint64 , disabled bool ) ( refused *refusal ) {
	if _ , get_err := models.GetUser( handlers.Store , user_id ); get_err != nil {
		refused = &refusal{ status: fiber.StatusNotFound , message: "no such user" }
		return
	}

	// Locking yourself out is unrecoverable without shell access, so it is
	// worth one guard rail even though an admin could still disable a peer.
	if disabled && user_id == self_id {
		refused = &refusal{ status: fiber.StatusBadRequest , message: "you cannot disable your own account" }
		return
	}

	// Disabling also deletes the account's sessions and live login links,
	// in the same transaction -- see models.SetUserDisabled. The owner cannot
	// be disabled, and only the owner switches another admin off or on; those
	// checks are in the model so every route gets them.
	if disable_err := models.SetUserDisabledAs( handlers.Store , user_id , self_id , disabled ); disable_err != nil {
		if errors.Is( disable_err , models.ErrOwnerProtected ) {
			refused = &refusal{ status: fiber.StatusForbidden , message: "the owner account cannot be disabled" }
			return
		}
		refused = actOnRefusal( disable_err , "disable or enable" )
		return
	}
	return
}

// revokeCredentials carries every rule for cutting an account's credentials off,
// for RevokeAccess and the MCP admin_revoke_access tool.
func ( handlers *Handlers ) revokeCredentials( self_id uint64 , user_id uint64 ) ( revoked models.Revoked , refused *refusal ) {
	if _ , get_err := models.GetUser( handlers.Store , user_id ); get_err != nil {
		refused = &refusal{ status: fiber.StatusNotFound , message: "no such user" }
		return
	}
	// Same guard rail as disabling: revoking your own access signs you out
	// with no link to get back in.
	if user_id == self_id {
		refused = &refusal{ status: fiber.StatusBadRequest , message: "you cannot revoke your own access" }
		return
	}

	// The owner is refused here, and only the owner revokes another admin.
	// Only an operator with shell access may cut the owner off -- see
	// models.RevokeAllCredentials.
	revoked , revoke_err := models.RevokeAllCredentialsAs( handlers.Store , user_id , self_id )
	if revoke_err != nil {
		if errors.Is( revoke_err , models.ErrOwnerProtected ) {
			refused = &refusal{ status: fiber.StatusForbidden , message: "the owner account's access cannot be revoked" }
			return
		}
		refused = actOnRefusal( revoke_err , "revoke the access of" )
	}
	return
}
