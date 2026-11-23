package routes

import (
	errors "errors"
	strings "strings"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	db "vocabtrainer/server/db"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

// API keys are how anything that is not a browser calls this API: a script, a
// cron job, a CI pipeline, another service. A key carries a role from the same
// set users do, so there is one permission model rather than two -- see
// server/models/api_key.go and references/architecture.md.
//
// Every signed-in user can mint their own keys, and revoke or delete them. An
// admin can additionally see, revoke, and delete everyone's, which is what
// makes "who has standing access to this app" an answerable question, and can
// mint one that acts as someone else (CreateAPIKeyForUser).
//
// Revoking keeps the row, marked dead, so the listing still shows the key
// existed and when it stopped working; the `purge` job drops it later.
// Deleting removes the row now. Either one stops the key immediately.
//
// Minting, revoking, and deleting require a browser session (the group in routes.go adds
// Guard.RequireSession), so a leaked key cannot quietly mint its replacement.
// A key may hand out login links and invite links, but revoking or deleting
// it voids the ones still usable -- see Guard.RequireSession.
//
// The one exception is key minting (portal contract v2): an admin key the
// owner allowed to (MayMintKeys) may make, list and revoke other accounts'
// keys through /api/admin/users/{user_id}/keys and /api/admin/keys/{key_id}/revoke.
// Each key it makes records it, and dies with it.

// keyIDPath is the key a route under /api/keys/{key_id} or
// /api/admin/keys/{key_id} acts on.
type keyIDPath struct {
	KeyID string `json:"key_id" path:"key_id" jsonschema_description:"The key's id, from GET /api/keys (GET /api/admin/keys for anyone's)."`
}

type createAPIKeyRequest struct {
	Name string `json:"name" jsonschema:"minLength=1,maxLength=80" error:"name must be 1-80 characters"`
	Role string `json:"role,omitempty" jsonschema:"enum=admin,enum=user,enum=viewer,default=user" error:"role must be admin, user or viewer"`
	// ExpiresInDays is a pointer so that three cases stay distinguishable:
	// absent means "use the configured default", 0 means "never expires", and
	// a number means that many days. With a plain int, "never" and "unset"
	// would be the same request.
	ExpiresInDays *int `json:"expires_in_days,omitempty" jsonschema:"minimum=0,maximum=3650" jsonschema_description:"Absent or null: the configured default. 0: never expires." error:"expires_in_days must be 0 (never) or up to 3650"`
}

// createUserAPIKeyRequest is createAPIKeyRequest plus whose key it is, asked
// about first.
type createUserAPIKeyRequest struct {
	UserID uint64 `json:"user_id" jsonschema:"minimum=1" error:"user_id is required"`
	createAPIKeyRequest
}

// renameAPIKeyRequest's Expect is the name the caller thinks it is replacing. The
// frontend sends it, so an undo cannot overwrite a rename made elsewhere.
type renameAPIKeyRequest struct {
	keyIDPath
	Name   string  `json:"name" jsonschema:"minLength=1,maxLength=80" jsonschema_description:"1-80 characters." error:"name must be 1-80 characters"`
	Expect *string `json:"expect,omitempty" jsonschema_description:"The name you think it has. Answers 409 if it has another."`
}

type renameAPIKeyReply struct {
	Ok   bool   `json:"ok"`
	Name string `json:"name"`
}

// apiKeyExpiryRequest's ExpiresInDays counts from now, and 0 means never, as when
// minting. Here it is required: there is no default to fall back on.
type apiKeyExpiryRequest struct {
	keyIDPath
	ExpiresInDays int `json:"expires_in_days" jsonschema:"minimum=0,maximum=3650" jsonschema_description:"Counted from now. 0 means it never expires." error:"expires_in_days must be 0 (never) or up to 3650"`
}

type apiKeySignInRequest struct {
	keyIDPath
	MaySignIn bool `json:"may_sign_in" jsonschema_description:"true lets the key make a login link for your account: one that works once, for at most 15 minutes."`
}

type apiKeyMintingRequest struct {
	keyIDPath
	MayMintKeys bool `json:"may_mint_keys" jsonschema_description:"true lets this key make, list and revoke other accounts' keys, each recording this key and revoked with it."`
}

// mintKeyRequest is what POST /api/admin/users/{user_id}/keys takes. The
// expiry is required and never "never": a key made by a key always dies.
type mintKeyRequest struct {
	userIDPath
	Name          string `json:"name" jsonschema:"minLength=1,maxLength=80" error:"name must be 1-80 characters"`
	Role          string `json:"role" jsonschema:"enum=admin,enum=user,enum=viewer" jsonschema_description:"Capped at the account's own role." error:"role must be admin, user or viewer"`
	ExpiresInDays int    `json:"expires_in_days" jsonschema:"minimum=1,maximum=365" jsonschema_description:"Required: 1-365. A minted key always expires." error:"expires_in_days must be 1-365"`
}


type listMyAPIKeysRequest struct {
	Page
	Q    string `json:"q,omitempty" query:"q" jsonschema_description:"Keep keys whose name contains this, in any case."`
	Live string `json:"live,omitempty" query:"live" jsonschema:"enum=true" jsonschema_description:"Leave out revoked and expired keys."`
	Sort string `json:"sort,omitempty" query:"sort" jsonschema:"enum=name,enum=role,enum=last_used_at,enum=expires_at,enum=created_at" jsonschema_description:"Field to sort the whole list by. Rows without it go last."`
	Direction
}

type listAllAPIKeysRequest struct {
	Page
	Q    string `json:"q,omitempty" query:"q" jsonschema_description:"Keep keys whose name or owner contains this, in any case."`
	Live string `json:"live,omitempty" query:"live" jsonschema:"enum=true" jsonschema_description:"Leave out revoked and expired keys."`
	Sort string `json:"sort,omitempty" query:"sort" jsonschema:"enum=display_name,enum=name,enum=role,enum=last_used_at,enum=expires_at,enum=created_at" jsonschema_description:"Field to sort the whole list by. Rows without it go last."`
	Direction
}

// userAPIKeysReply is one account's keys, whole: an account holds at most
// a hundred rows (models.apiKeyMaxRowsPerUser), so it is never paged.
type userAPIKeysReply struct {
	Keys []apiKeyRow `json:"keys"`
}

type apiKeysReply struct {
	Keys []apiKeyRow `json:"keys"`
	Paged
}

// RequireAPIKeys hides the key-management endpoints when the feature is
// switched off, so an app with no programmatic callers can close the surface
// entirely. The Authorization header is ignored in that state too -- that part
// is enforced in server/security, not here, because it has to hold for every
// route rather than only these.
func ( handlers *Handlers ) RequireAPIKeys( c fiber.Ctx ) ( err error ) {
	if handlers.cfg().APIKeysEnabled == false {
		err = notFound( c , "api keys are disabled on this server" )
		return
	}
	err = c.Next()
	return
}

// apiKeyRow is the only shape a key is ever returned in. The secret hash is
// not in it, and nor is the credential -- that exists in one response, from
// CreateAPIKey, and is never recoverable afterwards.
type apiKeyRow struct {
	ID            string    `json:"id"`
	UserID        uint64    `json:"user_id"`
	Name          string    `json:"name"`
	Role          string    `json:"role" jsonschema:"enum=admin,enum=user,enum=viewer"`
	CreatedAt     time.Time `json:"created_at"`
	Revoked       bool      `json:"revoked"`
	Expired       bool      `json:"expired"`
	Live          bool      `json:"live"`
	ExpiresAt     time.Time `json:"expires_at,omitzero"`
	LastUsedAt    time.Time `json:"last_used_at,omitzero"`
	RevokedAt     time.Time `json:"revoked_at,omitzero"`
	DisplayName   string    `json:"display_name,omitempty" jsonschema_description:"The key's owner. On the admin list only."`
	CreatedBy     uint64    `json:"created_by,omitempty" jsonschema_description:"The admin who made this key for its owner. Absent if the owner made it."`
	CreatedByName string    `json:"created_by_name,omitempty"`
	MaySignIn     bool      `json:"may_sign_in" jsonschema_description:"Whether this key may make a login link for its own account."`
	MayMintKeys   bool      `json:"may_mint_keys" jsonschema_description:"Whether this key may make keys for other accounts. Only the owner switches it on."`
	MintedByKey   string    `json:"minted_by_key" jsonschema_description:"The key that made this one, or empty. Revoking that key revokes this one."`
}

func ( apiKeyRow ) APIName() ( name string ) { name = "APIKey"; return }

func newAPIKeyRow( key *models.APIKey , display_name string ) ( row apiKeyRow ) {
	row = apiKeyRow{
		ID:          key.ID,
		UserID:      key.UserID,
		Name:        key.Name,
		Role:        key.Role,
		CreatedAt:   key.CreatedAt,
		Revoked:     key.Revoked(),
		Expired:     key.Expired(),
		Live:        key.Live(),
		CreatedBy:   key.CreatedBy,
		DisplayName: display_name,
		MaySignIn:   key.MaySignIn,
		MayMintKeys: key.MayMintKeys,
		MintedByKey: key.MintedByKey,
	}
	if key.ExpiresAt != nil { row.ExpiresAt = *key.ExpiresAt }
	if key.LastUsedAt != nil { row.LastUsedAt = *key.LastUsedAt }
	if key.RevokedAt != nil { row.RevokedAt = *key.RevokedAt }
	return
}

// newAPIKeyReply is a key as the one response that carries its credential
// shows it.
type newAPIKeyReply struct {
	apiKeyRow
	Credential string `json:"credential" jsonschema_description:"Shown in this response only, and never recoverable."`
}

func ( newAPIKeyReply ) APIName() ( name string ) { name = "NewAPIKey"; return }

// onlyLiveKeys is ?live=true, for hiding keys that no longer work. The lists
// keep them otherwise: a key that used to exist is part of the answer.
func onlyLiveKeys( live string , rows []apiKeyRow ) ( kept []apiKeyRow ) {
	kept = rows
	if live != "true" { return }
	kept = []apiKeyRow{}
	for _ , row := range rows {
		if row.Live { kept = append( kept , row ) }
	}
	return
}

// listMyAPIKeys answers "what has standing access as me". Revoked and expired
// keys are included, marked as such, because a key that used to exist is part
// of that answer.
//
// @route("GET /api/keys", summary="Your API keys", tag="API keys", access=SESSION_ONLY, feature="api_keys")
func ( handlers *Handlers ) listMyAPIKeys( c fiber.Ctx , in *listMyAPIKeysRequest ) ( reply *apiKeysReply , err error ) {
	user := security.UserFrom( c )
	keys , err := models.ListAPIKeysForUser( handlers.Store , user.ID )
	if err != nil { return }
	// Most people have no key someone else made, so the names are only loaded
	// when one is there to be named.
	names := map[ uint64 ]string{}
	for _ , key := range keys {
		if key.CreatedBy == 0 { continue }
		if names , err = handlers.displayNames(); err != nil { return }
		break
	}
	rows := []apiKeyRow{}
	for _ , key := range keys {
		row := newAPIKeyRow( key , "" )
		nameCreator( &row , key , names )
		rows = append( rows , row )
	}
	rows = onlyLiveKeys( in.Live , matchRows( rows , in.Q , "name" ) )
	reply = &apiKeysReply{}
	reply.Keys , reply.Paged = sortedPage( rows , in.Sort , in.Direction , in.Page )
	return
}

// displayNames is every account's name by id: one pass rather than a GetUser
// per key, since a bucket read per row is the shape that quietly becomes slow.
func ( handlers *Handlers ) displayNames() ( names map[ uint64 ]string , err error ) {
	users , err := models.ListUsers( handlers.Store )
	if err != nil { return }
	names = map[ uint64 ]string{}
	for _ , user := range users {
		names[ user.ID ] = user.DisplayName
	}
	return
}

// nameCreator says who made a key on its owner's behalf. An admin deleted
// since has no name, and the page shows their id instead.
func nameCreator( row *apiKeyRow , key *models.APIKey , names map[ uint64 ]string ) {
	if name := names[ key.CreatedBy ]; key.CreatedBy != 0 && name != "" {
		row.CreatedByName = name
	}
}

// keyRole is the role a new key asks for: "user" when it names none.
func keyRole( role string ) ( result string ) {
	result = role
	if result == "" { result = models.RoleUser }
	return
}

// createMyAPIKey mints a key for the signed-in user and returns the credential
// once. The default role is "user" even for an admin: an admin-scoped key can
// list and disable accounts and revoke anyone's access, so it should be asked
// for rather than handed out by omission.
//
// The ceiling that a key cannot outrank its owner lives in
// models.IssueAPIKey, not here, so the control socket and the CLI get it too.
//
// @route("POST /api/keys", summary="Create an API key",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys", status=201,
// )
func ( handlers *Handlers ) createMyAPIKey( c fiber.Ctx , in *createAPIKeyRequest ) ( reply *newAPIKeyReply , err error ) {
	if models.ValidAPIKeyName( in.Name ) == false {
		err = refuse( fiber.StatusBadRequest , "name must be 1-80 characters" )
		return
	}
	ttl , ok := models.APIKeyTTLFromDays( in.ExpiresInDays , handlers.cfg().APIKeyTTL )
	if ok == false {
		err = refuse( fiber.StatusBadRequest , "expires_in_days must be 0 (never) or up to 3650" )
		return
	}

	// Through IssueAPIKeyFor for the lineage: a key made in a session a key's
	// login link opened answers to that key, and dies with it.
	actor := security.ActorFrom( c )
	credential , key , issue_err := models.IssueAPIKeyFor( handlers.Store , actor.User.ID , actor.LineageKey() , actor.User.ID , in.Name , keyRole( in.Role ) , ttl )
	switch {
	case errors.Is( issue_err , models.ErrParentKeyDead ):
		err = refuse( fiber.StatusForbidden , issue_err.Error() )
		return
	case errors.Is( issue_err , models.ErrAPIKeyRole ):
		err = refuse( fiber.StatusForbidden , "you cannot create an API key with more permission than your own account" )
		return
	case errors.Is( issue_err , models.ErrAPIKeyLimit ):
		err = refuse( fiber.StatusBadRequest , "you already have the maximum number of live API keys -- revoke one first" )
		return
	case errors.Is( issue_err , models.ErrAPIKeyDisabled ):
		// Not reachable through a live session today, since a disabled
		// account cannot hold one. Mapped anyway so that a future path
		// into this handler fails as a refusal rather than a 500.
		err = refuse( fiber.StatusForbidden , "this account is disabled" )
		return
	case issue_err != nil:
		err = issue_err
		return
	}

	// The credential appears here and nowhere else, ever.
	reply = &newAPIKeyReply{ apiKeyRow: newAPIKeyRow( key , "" ) , Credential: credential }
	handlers.audit( c , logs.KindAdmin , "api key created" , fiber.Map{ "key_id": key.ID , "name": key.Name , "role": key.Role } )
	return
}

// createAPIKeyForUser mints a key that acts as another account, for setting up
// someone's integration for them. It needs a browser session, like every route
// that makes a credential: the admin_keys group in routes.go adds it, so a
// leaked admin key cannot mint itself a way in as somebody else.
//
// Who may mint for whom is models.IssueAPIKeyFor's to say. The key records who
// made it, and shows that to the person it acts as.
//
// @route("POST /api/admin/keys", summary="Create a key that acts as someone else",
//     tag="API keys", access=SESSION_ONLY, role=ADMIN, feature="api_keys", status=201,
//     description="Any admin may do this for an ordinary user, and only the owner for another admin. "
//         "Nobody may do it for the owner. The key records who made it and shows that to the person it "
//         "acts as.",
// )
func ( handlers *Handlers ) createAPIKeyForUser( c fiber.Ctx , in *createUserAPIKeyRequest ) ( reply *newAPIKeyReply , err error ) {
	if models.ValidAPIKeyName( in.Name ) == false {
		err = refuse( fiber.StatusBadRequest , "name must be 1-80 characters" )
		return
	}
	ttl , ok := models.APIKeyTTLFromDays( in.ExpiresInDays , handlers.cfg().APIKeyTTL )
	if ok == false {
		err = refuse( fiber.StatusBadRequest , "expires_in_days must be 0 (never) or up to 3650" )
		return
	}

	actor := security.UserFrom( c )
	credential , key , issue_err := models.IssueAPIKeyFor( handlers.Store , actor.ID , security.ActorFrom( c ).LineageKey() , in.UserID , in.Name , keyRole( in.Role ) , ttl )
	switch {
	case errors.Is( issue_err , models.ErrParentKeyDead ):
		err = refuse( fiber.StatusForbidden , issue_err.Error() )
		return
	case errors.Is( issue_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	case errors.Is( issue_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "a key for the owner can only be made by the owner, or from a shell" ).coded( apidoc.CodeOwnerProtected )
		return
	case errors.Is( issue_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can create a key for an admin" ).coded( apidoc.CodeOwnerRequired )
		return
	case errors.Is( issue_err , models.ErrAPIKeyRole ):
		err = refuse( fiber.StatusForbidden , "a key cannot have more permission than the account it belongs to" )
		return
	case errors.Is( issue_err , models.ErrAPIKeyLimit ):
		err = refuse( fiber.StatusBadRequest , "that account already has the maximum number of live API keys" )
		return
	case errors.Is( issue_err , models.ErrAPIKeyDisabled ):
		err = refuse( fiber.StatusForbidden , "that account is disabled" )
		return
	case issue_err != nil:
		err = issue_err
		return
	}

	// The credential appears here and nowhere else, ever.
	reply = &newAPIKeyReply{ apiKeyRow: newAPIKeyRow( key , "" ) , Credential: credential }
	handlers.audit( c , logs.KindAdmin , "api key created" , fiber.Map{ "key_id": key.ID , "name": key.Name , "role": key.Role , "user_id": key.UserID , "created_by": actor.ID } )
	return
}

// renameMyAPIKey renames one of your own keys. Someone else's is "no such api
// key", for the same reason as in RevokeMyAPIKey.
//
// @route("POST /api/keys/{key_id}/rename", summary="Rename one of your keys",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys", errors=(409,),
// )
func ( handlers *Handlers ) renameMyAPIKey( c fiber.Ctx , in *renameAPIKeyRequest ) ( reply *renameAPIKeyReply , err error ) {
	reply , err = handlers.renameKey( c , in , true )
	return
}

// renameAnyAPIKey is an admin renaming anyone's key. A name changes nothing about
// what a key can do, but it is what the owner goes by when choosing a key to
// revoke, so it follows the same ladder as the key's expiry: nobody renames the
// owner's keys, and only the owner renames another admin's.
//
// @route("POST /api/admin/keys/{key_id}/rename", summary="Rename anyone's key",
//     tag="API keys", access=SESSION_ONLY, role=ADMIN, feature="api_keys", errors=(409,),
// )
func ( handlers *Handlers ) renameAnyAPIKey( c fiber.Ctx , in *renameAPIKeyRequest ) ( reply *renameAPIKeyReply , err error ) {
	reply , err = handlers.renameKey( c , in , false )
	return
}

func ( handlers *Handlers ) renameKey( c fiber.Ctx , in *renameAPIKeyRequest , own_only bool ) ( reply *renameAPIKeyReply , err error ) {
	if models.ValidAPIKeyName( in.Name ) == false {
		err = refuse( fiber.StatusBadRequest , "name must be 1-80 characters" )
		return
	}
	key , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil || ( own_only && key.UserID != security.UserFrom( c ).ID ) {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	rename_err := models.RenameAPIKey( handlers.Store , key.ID , security.UserFrom( c ).ID , in.Name , in.Expect )
	var stale *models.RenameConflictError
	switch {
	case errors.As( rename_err , &stale ):
		err = refuse( fiber.StatusConflict , stale.Error() , fiber.Map{ "name": stale.Current } )
		return
	case errors.Is( rename_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	case errors.Is( rename_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "only the owner can rename the owner's keys" ).coded( apidoc.CodeOwnerProtected )
		return
	case errors.Is( rename_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can rename an admin's keys" ).coded( apidoc.CodeOwnerRequired )
		return
	case rename_err != nil:
		err = rename_err
		return
	}
	name := strings.TrimSpace( in.Name )
	handlers.audit( c , logs.KindAdmin , "api key renamed" , fiber.Map{ "key_id": key.ID , "name": name } )
	reply = &renameAPIKeyReply{ Ok: true , Name: name }
	return
}

// setMyAPIKeyExpiry changes when one of your own keys expires. The session this
// group requires is what stops a leaked key from extending itself.
//
// @route("POST /api/keys/{key_id}/expiry", summary="Change when one of your keys expires",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys",
// )
func ( handlers *Handlers ) setMyAPIKeyExpiry( c fiber.Ctx , in *apiKeyExpiryRequest ) ( reply *apiKeyRow , err error ) {
	reply , err = handlers.setKeyExpiry( c , in , true )
	return
}

// setAnyAPIKeyExpiry is the same for an admin. Whose keys they may change is
// models.SetAPIKeyExpiry's to say.
//
// @route("POST /api/admin/keys/{key_id}/expiry", summary="Change when anyone's key expires",
//     tag="API keys", access=SESSION_ONLY, role=ADMIN, feature="api_keys",
// )
func ( handlers *Handlers ) setAnyAPIKeyExpiry( c fiber.Ctx , in *apiKeyExpiryRequest ) ( reply *apiKeyRow , err error ) {
	reply , err = handlers.setKeyExpiry( c , in , false )
	return
}

func ( handlers *Handlers ) setKeyExpiry( c fiber.Ctx , in *apiKeyExpiryRequest , own_only bool ) ( reply *apiKeyRow , err error ) {
	ttl , ok := models.APIKeyTTLFromDays( &in.ExpiresInDays , 0 )
	if ok == false {
		err = refuse( fiber.StatusBadRequest , "expires_in_days must be 0 (never) or up to 3650" )
		return
	}
	actor := security.UserFrom( c )
	found , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil || ( own_only && found.UserID != actor.ID ) {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	key , set_err := models.SetAPIKeyExpiry( handlers.Store , found.ID , actor.ID , ttl )
	switch {
	case errors.Is( set_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	case errors.Is( set_err , models.ErrAPIKeyDead ):
		err = refuse( fiber.StatusBadRequest , "only a live key can have its expiry changed -- create a new one instead" )
		return
	case errors.Is( set_err , models.ErrParentKeyDead ):
		err = refuse( fiber.StatusBadRequest , "the key that made this one no longer works, so neither can this one -- create a new one instead" )
		return
	case errors.Is( set_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "only the owner can change when the owner's keys expire" ).coded( apidoc.CodeOwnerProtected )
		return
	case errors.Is( set_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can change when an admin's keys expire" ).coded( apidoc.CodeOwnerRequired )
		return
	case set_err != nil:
		err = set_err
		return
	}
	handlers.audit( c , logs.KindAdmin , "api key expiry changed" , fiber.Map{ "key_id": key.ID , "expires_in_days": in.ExpiresInDays } )
	row := newAPIKeyRow( key , "" )
	reply = &row
	return
}

// setMyAPIKeySignIn lets one of your own keys make a login link for your
// account, or stops it. Session-only, so a key cannot give itself the right:
// a link becomes a browser session, which opens everything kept from keys.
// There is no admin version. Whether a key may sign in as you is yours alone
// to decide.
//
// @route("POST /api/keys/{key_id}/sign-in", summary="Let one of your keys make sign-in links",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys",
//     description="Off by default. When on, the key may ask POST /api/account/login-link for a link that "
//         "signs your account in on a browser, which then can do everything you can, including what keys "
//         "may not. The link works once, for at most 15 minutes.",
// )
func ( handlers *Handlers ) setMyAPIKeySignIn( c fiber.Ctx , in *apiKeySignInRequest ) ( reply *apiKeyRow , err error ) {
	key , set_err := models.SetAPIKeySignIn( handlers.Store , in.KeyID , security.UserFrom( c ).ID , in.MaySignIn )
	switch {
	case errors.Is( set_err , db.ErrNotFound ) , errors.Is( set_err , models.ErrAPIKeyNotYours ):
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	case errors.Is( set_err , models.ErrAPIKeyDead ):
		err = refuse( fiber.StatusBadRequest , "only a live key can be allowed to make sign-in links" )
		return
	case errors.Is( set_err , models.ErrAPIKeyMinted ):
		err = refuse( fiber.StatusBadRequest , set_err.Error() )
		return
	case set_err != nil:
		err = set_err
		return
	}
	handlers.audit( c , logs.KindAuth , "api key sign-in links changed" , fiber.Map{ "key_id": key.ID , "may_sign_in": key.MaySignIn } )
	row := newAPIKeyRow( key , "" )
	reply = &row
	return
}

// setMyAPIKeyMinting lets one of the owner's own admin-role keys make keys
// for other accounts, or stops it. Session-only and owner-only: it is what an
// outside admin hub such as ProjectPortal needs to hand its people keys, and
// nobody else's to give.
//
// @route("POST /api/keys/{key_id}/mint-keys", summary="Let one of your keys make keys",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys",
//     description="Owner only, on one of your own admin-role keys. When on, the key may make keys for "
//         "other accounts (never the owner's) through POST /api/admin/users/{user_id}/keys, list them, and "
//         "revoke them. Each key it makes records it, and revoking or deleting this key revokes them all.",
// )
func ( handlers *Handlers ) setMyAPIKeyMinting( c fiber.Ctx , in *apiKeyMintingRequest ) ( reply *apiKeyRow , err error ) {
	key , set_err := models.SetAPIKeyMinting( handlers.Store , in.KeyID , security.UserFrom( c ).ID , in.MayMintKeys )
	switch {
	case errors.Is( set_err , db.ErrNotFound ) , errors.Is( set_err , models.ErrAPIKeyNotYours ):
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	case errors.Is( set_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can let a key mint keys" ).coded( apidoc.CodeOwnerRequired )
		return
	case errors.Is( set_err , models.ErrAPIKeyNotAdmin ) , errors.Is( set_err , models.ErrAPIKeyMintsNot ) , errors.Is( set_err , models.ErrAPIKeyDead ):
		err = refuse( fiber.StatusBadRequest , set_err.Error() )
		return
	case set_err != nil:
		err = set_err
		return
	}
	handlers.audit( c , logs.KindAuth , "api key minting changed" , fiber.Map{ "key_id": key.ID , "may_mint_keys": key.MayMintKeys } )
	row := newAPIKeyRow( key , "" )
	reply = &row
	return
}

// keyMintingOff is the refusal for a key without MayMintKeys on the minting
// routes. A browser session (an admin's, as role=ADMIN makes sure) passes.
func keyMintingOff( c fiber.Ctx ) ( refused *refusal ) {
	actor := security.ActorFrom( c )
	if actor.ViaAPIKey() && actor.APIKey.MayMintKeys == false {
		refused = refuse( fiber.StatusForbidden , "key_minting_off" ).coded( apidoc.CodeKeyMintingOff )
	}
	return
}

// mintKeyForUser is a key making a key for an account: portal contract v2's
// key minting. Rules in models.MintAPIKey; the expiry is required.
//
// @route("POST /api/admin/users/{user_id}/keys", summary="Make a key for an account",
//     tag="API keys", role=ADMIN, feature="api_keys", status=201, errors=(404,),
//     description="Needs a browser session or a key the owner allowed to make keys (may_mint_keys); any "
//         "other key gets 403 key_minting_off. The role is capped at the account's own; expires_in_days "
//         "is required (1-365). Never for the owner. The new key records the key that made it, can never "
//         "make keys or sign-in links itself, and is revoked when that key is revoked or deleted. Answers "
//         "with the key and its credential, shown only now.",
//     mcp_http="Makes an API key, which no MCP tool does.",
// )
func ( handlers *Handlers ) mintKeyForUser( c fiber.Ctx , in *mintKeyRequest ) ( reply *newAPIKeyReply , err error ) {
	if refused := keyMintingOff( c ); refused != nil {
		err = refused
		return
	}
	if models.ValidAPIKeyName( in.Name ) == false {
		err = refuse( fiber.StatusBadRequest , "name must be 1-80 characters" )
		return
	}
	if in.ExpiresInDays < 1 || in.ExpiresInDays > 365 {
		err = refuse( fiber.StatusBadRequest , "expires_in_days must be 1-365" )
		return
	}
	if models.ValidRole( in.Role ) == false {
		err = refuse( fiber.StatusBadRequest , "role must be admin, user or viewer" )
		return
	}
	actor := security.ActorFrom( c )
	ttl := time.Duration( in.ExpiresInDays ) * 24 * time.Hour
	credential , key , issue_err := models.MintAPIKey( handlers.Store , actor.User.ID , actor.LineageKey() , in.UserID , in.Name , in.Role , ttl )
	switch {
	case errors.Is( issue_err , models.ErrParentKeyDead ):
		err = refuse( fiber.StatusForbidden , issue_err.Error() )
		return
	case errors.Is( issue_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	case errors.Is( issue_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "the owner's keys cannot be minted or revoked this way" ).coded( apidoc.CodeOwnerProtected )
		return
	case errors.Is( issue_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can make a key for an admin" ).coded( apidoc.CodeOwnerRequired )
		return
	case errors.Is( issue_err , models.ErrAPIKeyLimit ):
		err = refuse( fiber.StatusBadRequest , "that account already has the maximum number of live API keys" )
		return
	case errors.Is( issue_err , models.ErrAPIKeyDisabled ):
		err = refuse( fiber.StatusForbidden , "that account is disabled" )
		return
	case issue_err != nil:
		err = issue_err
		return
	}
	reply = &newAPIKeyReply{ apiKeyRow: newAPIKeyRow( key , "" ) , Credential: credential }
	handlers.audit( c , logs.KindAdmin , "api key minted" , fiber.Map{ "key_id": key.ID , "name": key.Name , "role": key.Role , "user_id": key.UserID , "minted_by_key": key.MintedByKey } )
	return
}

// listUserAPIKeys is one account's keys, for a key that makes them.
//
// @route("GET /api/admin/users/{user_id}/keys", summary="An account's keys",
//     tag="API keys", role=ADMIN, feature="api_keys", errors=(404,),
//     description="One account's keys, revoked and expired ones included, newest first. Each row says "
//         "which key minted it (minted_by_key, empty when a person made it). Needs a browser session or a "
//         "key with may_mint_keys; any other key gets 403 key_minting_off. Follows the owner ladder: "
//         "nobody lists the owner's keys this way, and only the owner lists another admin's.",
//     mcp_http="Manages API keys, which no MCP tool does.",
// )
func ( handlers *Handlers ) listUserAPIKeys( c fiber.Ctx , in *userIDPath ) ( reply *userAPIKeysReply , err error ) {
	if refused := keyMintingOff( c ); refused != nil {
		err = refused
		return
	}
	actor := security.ActorFrom( c )
	if refused := actOnRefusal( models.MayActOn( handlers.Store , actor.User.ID , in.UserID ) , "list the keys of" ); refused != nil {
		err = refused
		return
	}
	user , get_err := models.GetUser( handlers.Store , in.UserID )
	if get_err != nil {
		err = refuse( fiber.StatusNotFound , "no such user" )
		return
	}
	keys , err := models.ListAPIKeysForUser( handlers.Store , user.ID )
	if err != nil { return }
	rows := []apiKeyRow{}
	for _ , key := range keys {
		rows = append( rows , newAPIKeyRow( key , user.DisplayName ) )
	}
	reply = &userAPIKeysReply{ Keys: rows }
	return
}

// revokeMyAPIKey takes effect immediately -- the next request carrying that
// key fails. Someone else's key is reported as "no such api key" rather than
// "forbidden", so the endpoint does not confirm which key ids exist.
//
// @route("POST /api/keys/{key_id}/revoke", summary="Revoke one of your keys",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys",
// )
func ( handlers *Handlers ) revokeMyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *okReply , err error ) {
	user := security.UserFrom( c )
	key , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil || key.Deleted() || key.UserID != user.ID {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	if err = models.RevokeAPIKey( handlers.Store , key.ID ); err != nil { return }
	handlers.audit( c , logs.KindAdmin , "api key revoked" , fiber.Map{ "key_id": in.KeyID } )
	reply = &okReply{ Ok: true }
	return
}

// deleteMyAPIKey deletes one of your own keys -- live or already dead. It
// stops working at once, and RestoreMyAPIKey can bring it back for a few
// minutes. Someone else's key is "no such api key", for the same reason as in
// RevokeMyAPIKey.
//
// @route("POST /api/keys/{key_id}/delete", summary="Delete one of your keys",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys",
// )
func ( handlers *Handlers ) deleteMyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *okReply , err error ) {
	user := security.UserFrom( c )
	key , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil || key.UserID != user.ID {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	if delete_err := models.DeleteAPIKey( handlers.Store , key.ID , user.ID ); delete_err != nil {
		err = deleteKeyRefusal( delete_err )
		return
	}
	handlers.audit( c , logs.KindAdmin , "api key deleted" , fiber.Map{ "key_id": in.KeyID } )
	reply = &okReply{ Ok: true }
	return
}

// restoreMyAPIKey undoes DeleteMyAPIKey, within models.RestoreWindow. A key
// an admin deleted is the admin's to restore, not its owner's.
//
// @route("POST /api/keys/{key_id}/restore", summary="Undo deleting one of your keys",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys", errors=(409,),
// )
func ( handlers *Handlers ) restoreMyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *apiKeyRow , err error ) {
	user := security.UserFrom( c )
	key , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil || key.UserID != user.ID {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	reply , err = handlers.restoreKey( c , key.ID , user )
	return
}

// restoreAnyAPIKey undoes a delete, any key's, within models.RestoreWindow.
//
// @route("POST /api/admin/keys/{key_id}/restore", summary="Undo deleting anyone's key",
//     tag="API keys", access=SESSION_ONLY, role=ADMIN, feature="api_keys", errors=(409,),
// )
func ( handlers *Handlers ) restoreAnyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *apiKeyRow , err error ) {
	reply , err = handlers.restoreKey( c , in.KeyID , security.UserFrom( c ) )
	return
}

func ( handlers *Handlers ) restoreKey( c fiber.Ctx , key_id string , actor *models.User ) ( reply *apiKeyRow , err error ) {
	key , restore_err := models.RestoreAPIKey( handlers.Store , key_id , actor.ID , actor.IsAdmin() )
	if restore_err != nil {
		err = restoreRefusal( restore_err )
		return
	}
	handlers.audit( c , logs.KindAdmin , "api key restored" , fiber.Map{ "key_id": key_id } )
	row := newAPIKeyRow( key , "" )
	reply = &row
	return
}

// deleteKeyRefusal maps a DeleteAPIKey failure. A key that is already deleted,
// or vanished between the lookup and the delete -- deleted by its owner and
// an admin at once -- is reported as missing, not as a server fault.
//
// The owner rule (models.mayActOnTx) is a 403 worded like the other routes'.
func deleteKeyRefusal( delete_err error ) ( err error ) {
	switch {
	case errors.Is( delete_err , db.ErrNotFound ):
		err = refuse( fiber.StatusNotFound , "no such api key" )
	case errors.Is( delete_err , models.ErrOwnerProtected ):
		err = refuse( fiber.StatusForbidden , "only the owner can change the owner's keys" ).coded( apidoc.CodeOwnerProtected )
	case errors.Is( delete_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner can change an admin's keys" ).coded( apidoc.CodeOwnerRequired )
	default:
		err = delete_err
	}
	return
}

// listAllAPIKeys is the admin view: every key in the app, with whose it is.
// This is the "who and what has standing access" question, and it is the
// reason admins can see keys they did not create.
//
// @route("GET /api/admin/keys", summary="Every key in the app",
//     tag="API keys", access=SESSION_ONLY, role=ADMIN, feature="api_keys",
// )
func ( handlers *Handlers ) listAllAPIKeys( c fiber.Ctx , in *listAllAPIKeysRequest ) ( reply *apiKeysReply , err error ) {
	keys , err := models.ListAPIKeys( handlers.Store )
	if err != nil { return }
	names , err := handlers.displayNames()
	if err != nil { return }
	rows := []apiKeyRow{}
	for _ , key := range keys {
		row := newAPIKeyRow( key , names[ key.UserID ] )
		nameCreator( &row , key , names )
		rows = append( rows , row )
	}
	rows = onlyLiveKeys( in.Live , matchRows( rows , in.Q , "name" , "display_name" ) )
	reply = &apiKeysReply{}
	reply.Keys , reply.Paged = sortedPage( rows , in.Sort , in.Direction , in.Page )
	return
}

// revokeAnyAPIKey is how an admin kills a credential belonging to someone
// else -- a laptop that went missing, a CI system being decommissioned, a
// contractor who has finished. Every key it minted goes with it, down the
// chain (models.VoidLinksMintedByKeyTx).
//
// A key the owner allowed to make keys may call it too (key minting), for
// any key but the owner's; any other key gets 403 key_minting_off.
//
// @route("POST /api/admin/keys/{key_id}/revoke", summary="Revoke anyone's key",
//     tag="API keys", role=ADMIN, feature="api_keys", errors=(404,),
//     description="Stops the key at once, and every key it made, down the chain. Needs a browser session "
//         "or a key with may_mint_keys (any other key gets 403 key_minting_off), which may not revoke the "
//         "owner's keys.",
//     mcp_http="Manages API keys, which no MCP tool does.",
// )
func ( handlers *Handlers ) revokeAnyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *okReply , err error ) {
	if refused := keyMintingOff( c ); refused != nil {
		err = refused
		return
	}
	key , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	if security.ActorFrom( c ).ViaAPIKey() {
		if owner_id , owner_err := models.OwnerID( handlers.Store ); owner_err != nil || owner_id == key.UserID {
			err = refuse( fiber.StatusForbidden , "the owner's keys cannot be minted or revoked this way" ).coded( apidoc.CodeOwnerProtected )
			return
		}
	}
	if revoke_err := models.RevokeAPIKeyAs( handlers.Store , key.ID , security.UserFrom( c ).ID ); revoke_err != nil {
		err = deleteKeyRefusal( revoke_err )
		return
	}
	handlers.audit( c , logs.KindAdmin , "api key revoked" , fiber.Map{ "key_id": in.KeyID } )
	reply = &okReply{ Ok: true }
	return
}

// deleteAnyAPIKey stops the key working and takes it off every listing, its
// owner's included. Unlike RevokeAnyAPIKey it can be undone, by
// RestoreAnyAPIKey.
//
// @route("POST /api/admin/keys/{key_id}/delete", summary="Delete anyone's key",
//     tag="API keys", access=SESSION_ONLY, role=ADMIN, feature="api_keys",
// )
func ( handlers *Handlers ) deleteAnyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *okReply , err error ) {
	if delete_err := models.DeleteAPIKey( handlers.Store , in.KeyID , security.UserFrom( c ).ID ); delete_err != nil {
		err = deleteKeyRefusal( delete_err )
		return
	}
	handlers.audit( c , logs.KindAdmin , "api key deleted" , fiber.Map{ "key_id": in.KeyID } )
	reply = &okReply{ Ok: true }
	return
}

type rotatedKeyReply struct {
	Credential string `json:"credential" jsonschema_description:"The key's new credential, shown in this response only. The old one stopped working."`
}

func ( rotatedKeyReply ) APIName() ( name string ) { name = "RotatedKey"; return }

// rotateSelf is a key giving itself a new secret (portal contract v3): an
// outside tool rotates its own key on a schedule, with nobody involved.
//
// @route("POST /api/keys/self/rotate", summary="Rotate the calling key",
//     tag="API keys", access=KEY_ONLY, feature="api_keys",
//     description="Gives the calling key a new secret and answers its new credential, shown only now. The old "
//         "secret stops working at once. The key's id, name, role, grants, lineage and expiry stay as they are, so "
//         "the keys and links it made keep working; rotating never extends the expiry. A viewer key may call it.",
//     mcp_http="Makes an API key's credential, which no MCP tool does.",
// )
func ( handlers *Handlers ) rotateSelf( c fiber.Ctx , in *apidoc.None ) ( reply *rotatedKeyReply , err error ) {
	actor := security.ActorFrom( c )
	credential , key , rotate_err := models.RotateAPIKey( handlers.Store , actor.KeyID() )
	if rotate_err != nil {
		err = refuse( fiber.StatusBadRequest , rotate_err.Error() )
		return
	}
	handlers.audit( c , logs.KindAuth , "api key rotated" , fiber.Map{ "key_id": key.ID } )
	reply = &rotatedKeyReply{ Credential: credential }
	return
}

// rotateMyAPIKey is the owner rotating one of the owner's own keys from a
// browser session. Someone else's key is "no such api key", as everywhere.
//
// @route("POST /api/keys/{key_id}/rotate", summary="Rotate one of your keys",
//     tag="API keys", access=SESSION_ONLY, feature="api_keys", errors=(404,),
//     description="The owner only: gives one of the owner's own keys a new secret and answers its new credential, "
//         "shown only now. The old secret stops working at once; nothing else about the key changes.",
// )
func ( handlers *Handlers ) rotateMyAPIKey( c fiber.Ctx , in *keyIDPath ) ( reply *rotatedKeyReply , err error ) {
	if refused := keyBornRefusal( c ); refused != nil {
		err = refused
		return
	}
	user := security.UserFrom( c )
	key , get_err := models.GetAPIKey( handlers.Store , in.KeyID )
	if get_err != nil || key.Deleted() || key.UserID != user.ID {
		err = refuse( fiber.StatusNotFound , "no such api key" )
		return
	}
	if owner_id , owner_err := models.OwnerID( handlers.Store ); owner_err != nil || owner_id != user.ID {
		err = refuse( fiber.StatusForbidden , "only the owner rotates a key from here; any key rotates itself" ).coded( apidoc.CodeOwnerRequired )
		return
	}
	credential , _ , rotate_err := models.RotateAPIKey( handlers.Store , key.ID )
	if rotate_err != nil {
		err = refuse( fiber.StatusBadRequest , rotate_err.Error() )
		return
	}
	handlers.audit( c , logs.KindAuth , "api key rotated" , fiber.Map{ "key_id": key.ID } )
	reply = &rotatedKeyReply{ Credential: credential }
	return
}
