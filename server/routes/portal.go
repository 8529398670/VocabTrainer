package routes

import (
	errors "errors"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	db "vocabtrainer/server/db"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

// The owner's ProjectPortal card (portal contract v3): connect the portal
// through a service account of its own, see its keys, and disconnect it.
// The rules are in models/portal.go. Keys are never managed over MCP.

type portalAccountRow struct {
	ID          uint64      `json:"id"`
	DisplayName string      `json:"display_name"`
	Keys        []apiKeyRow `json:"keys" jsonschema_description:"Its live keys, newest first."`
}

func ( portalAccountRow ) APIName() ( name string ) { name = "PortalAccount"; return }

type portalReply struct {
	Accounts []portalAccountRow `json:"accounts" jsonschema_description:"Every service account, oldest first."`
}

// getPortal is the card's status: the service accounts and their live keys.
//
// @route("GET /api/owner/portal", summary="ProjectPortal's accounts",
//     tag="Owner", role=OWNER, feature="api_keys",
//     description="The service accounts ProjectPortal works through, each with its live keys and when they expire.",
// )
func ( handlers *Handlers ) getPortal( c fiber.Ctx , in *apidoc.None ) ( reply *portalReply , err error ) {
	accounts , err := models.PortalAccounts( handlers.Store )
	if err != nil { return }
	reply = &portalReply{ Accounts: []portalAccountRow{} }
	for _ , account := range accounts {
		row := portalAccountRow{ ID: account.User.ID , DisplayName: account.User.DisplayName , Keys: []apiKeyRow{} }
		for _ , key := range account.Keys {
			row.Keys = append( row.Keys , newAPIKeyRow( key , account.User.DisplayName ) )
		}
		reply.Accounts = append( reply.Accounts , row )
	}
	return
}

type portalConnectRequest struct {
	Name string `json:"name,omitempty" jsonschema:"minLength=1,maxLength=80,default=ProjectPortal" jsonschema_description:"The service account's name. Left out: ProjectPortal." error:"name must be 1-80 characters"`
	Days int    `json:"days" jsonschema:"minimum=1,maximum=365" jsonschema_description:"How long the key lasts, in days." error:"days must be 1-365"`
}

// connectPortal makes the service account if there is none by that name, and
// one more admin key for it that may make keys and never sign-in links.
//
// @route("POST /api/owner/portal/connect", summary="Connect ProjectPortal",
//     tag="Owner", role=OWNER, feature="api_keys", status=201,
//     description="Makes the service account named name (ProjectPortal when left out) if there is none, and "
//         "one admin key for it, lasting days (1-365), that may make keys for other accounts and never makes "
//         "sign-in links. Answers with the key and its credential, shown only now: paste it into "
//         "ProjectPortal as this app's admin key.",
// )
func ( handlers *Handlers ) connectPortal( c fiber.Ctx , in *portalConnectRequest ) ( reply *newAPIKeyReply , err error ) {
	name := in.Name
	if name == "" { name = models.DefaultPortalName }
	ttl := time.Duration( in.Days ) * 24 * time.Hour
	credential , key , user , connect_err := models.ConnectPortal( handlers.Store , security.UserFrom( c ).ID , name , ttl )
	switch {
	case errors.Is( connect_err , models.ErrBadPortalName ):
		err = refuse( fiber.StatusBadRequest , connect_err.Error() )
		return
	case errors.Is( connect_err , models.ErrAPIKeyLimit ):
		err = refuse( fiber.StatusBadRequest , "that account already has the maximum number of live API keys" )
		return
	case errors.Is( connect_err , models.ErrOwnerRequired ):
		err = refuse( fiber.StatusForbidden , "only the owner connects ProjectPortal" ).coded( apidoc.CodeOwnerRequired )
		return
	case connect_err != nil:
		err = connect_err
		return
	}
	reply = &newAPIKeyReply{ apiKeyRow: newAPIKeyRow( key , user.DisplayName ) , Credential: credential }
	handlers.audit( c , logs.KindAdmin , "portal connected" , fiber.Map{ "user_id": user.ID , "key_id": key.ID , "expires_at": key.ExpiresAt } )
	return
}

type portalDisconnectRequest struct {
	UserID uint64 `json:"user_id" jsonschema_description:"The service account."`
}

type portalDisconnectReply struct {
	Ok      bool `json:"ok"`
	Revoked int  `json:"revoked" jsonschema_description:"Its keys revoked. The keys and links they made went with them."`
}

// disconnectPortal revokes every key of a service account.
//
// @route("POST /api/owner/portal/disconnect", summary="Disconnect ProjectPortal",
//     tag="Owner", role=OWNER, feature="api_keys", errors=(404,),
//     description="Revokes every key of the service account user_id, and with them every key and link those "
//         "keys made. The account stays, with no way in, until Connect gives it a key again.",
// )
func ( handlers *Handlers ) disconnectPortal( c fiber.Ctx , in *portalDisconnectRequest ) ( reply *portalDisconnectReply , err error ) {
	revoked , disconnect_err := models.DisconnectPortal( handlers.Store , security.UserFrom( c ).ID , in.UserID )
	switch {
	case errors.Is( disconnect_err , db.ErrNotFound ) || errors.Is( disconnect_err , models.ErrNotService ):
		err = refuse( fiber.StatusNotFound , "no such service account" )
		return
	case disconnect_err != nil:
		err = actOnRefusal( disconnect_err , "disconnect" )
		return
	}
	handlers.audit( c , logs.KindAdmin , "portal disconnected" , fiber.Map{ "user_id": in.UserID , "revoked": revoked } )
	reply = &portalDisconnectReply{ Ok: true , Revoked: revoked }
	return
}
