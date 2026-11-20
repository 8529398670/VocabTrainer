// Package routes contains one file per group of endpoints, plus this file,
// which is the only place that knows the full URL map.
//
// The API here stays deliberately small: it covers logging in, managing who
// can log in, issuing the API keys that let non-browser callers in, and
// reading the UI's text. Everything else an app does belongs in a new file in
// this package (server side) and in static/js/ (browser side) -- see
// references/architecture.md for where the line sits and why.
package routes

import (
	json "encoding/json"
	errors "errors"
	log "log"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	config "vocabtrainer/server/config"
	corpus "vocabtrainer/server/corpus"
	db "vocabtrainer/server/db"
	livereload "vocabtrainer/server/livereload"
	errorpage "vocabtrainer/server/errorpage"
	jobs "vocabtrainer/server/jobs"
	language "vocabtrainer/server/language"
	logs "vocabtrainer/server/logs"
	middleware "vocabtrainer/server/middleware"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
	static "vocabtrainer/server/static"
	stream "vocabtrainer/server/stream"
)

// Handlers carries the dependencies every route needs. Passing this in
// rather than reaching for package-level globals is what makes the handlers
// testable and keeps the wiring visible in main.go.
type Handlers struct {
	Store *db.Store
	// Settings is the running configuration: read it through cfg(), once per
	// request, since config.yaml can change while the server runs.
	Settings *config.Holder
	Guard    *security.Guard
	Language *language.Language
	Static   *static.Server

	// Corpus is the word list, loaded once at startup and read-only
	// thereafter, so every request shares it without locking.
	Corpus *corpus.Corpus
	// Version is reported to MCP clients as serverInfo.version, and to the
	// page on every API reply (X-App-Version) so it can notice an upgrade.
	Version string
	// LiveReload is set by main.go when server.live_reload says open pages
	// hear about frontend changes. Nil, the route does not exist.
	LiveReload *livereload.Watcher
	// Logs is the activity log (server/logs), set by main.go after New. Nil
	// or switched off, nothing is logged and its routes answer 404
	// (RequireLogs).
	Logs *logs.Logger
	// Jobs is the background job registry (server/jobs), set by main.go.
	Jobs *jobs.Registry
	// LogStream is who has the Logs screen open with Live on, set by main.go.
	LogStream *stream.Hub
	// ErrorPages is what a browser gets while the app is in maintenance mode,
	// set by main.go. Nil is fine: everyone gets the JSON answer.
	ErrorPages *errorpage.Pages
	// OpenAPI is config/api.json, set by main.go.
	OpenAPI []byte
	// APIDoc is config/api.md, set by main.go: the /docs/api page reads it.
	APIDoc []byte

	uiEventBudget eventBudget
	readiness     readiness
}

func New( store *db.Store , settings *config.Holder , guard *security.Guard , lang *language.Language , static_server *static.Server , version string ) ( handlers *Handlers ) {
	handlers = &Handlers{
		Store:    store,
		Settings: settings,
		Guard:    guard,
		Language: lang,
		Static:   static_server,
		Version:  version,
	}
	return
}

//go:generate go run ../apidoc/gen

// Register wires every route. The API routes are declared beside their
// handlers, each with its // @route(...) declaration, and mountDeclared
// (routes_generated.go, written by go generate) mounts them, the most
// specific path first, each behind the guards its declaration says:
//
//   - Anyone signed in, by a browser session or an API key, is the default.
//     role=Admin is the admins, and role=Owner the owner from a browser
//     session; everyone else gets the 404 an unknown route gets.
//   - access=SessionOnly turns an API key away. It guards what a key must
//     not reach: minting and managing API keys, your own password, PIN and
//     two-factor, other people's files. A key that could do those could
//     turn itself into a session or carry off every account's files.
//   - An admin-scoped key may manage accounts and hand out login and invite
//     links. Every link records the key that made it, and revoking or
//     deleting that key voids the links it made that can still be used
//     (models.RevokeAPIKey), so revoking a leaked key still ends what it
//     could hand out. Who may delete or demote whom is in the models.
//   - access=Public needs no sign-in: logging in, the UI's text, health.
//   - A feature that can be switched off while the server runs has a switch
//     in featureGuards: its routes answer 404 while it is off, before
//     anything else is asked.
//
// Read routes_generated.go to see the whole surface of this server. Only the
// pages a link opens and the catch-alls are written out here.
func ( handlers *Handlers ) Register( app *fiber.App ) {
	// Resolve the session once, before anything else looks at the request.
	app.Use( handlers.Guard.LoadUser )
	apidoc.CheckCSRF = handlers.Guard.CheckCSRF
	apidoc.LogFailure = bulkFailure

	// Every API reply says which build answered it, so a page left open
	// across a deploy finds out on its next call (static/js/fresh.js).
	app.Use( "/api" , func( c fiber.Ctx ) ( err error ) {
		c.Set( "X-App-Version" , handlers.Version )
		err = c.Next()
		return
	} )

	// Next, because it needs to know who is asking and has to come before
	// every route below. See maintenance.go.
	app.Use( handlers.Maintenance )

	// The pages a login link and an invite link open. Opening one only shows
	// a page; the link is spent by the POST that page sends when the person
	// confirms. See auth.go and join.go.
	app.Get( "/login/*" , handlers.ShowLoginPage )
	// The app's own invite links (invite.go). Like a login link, opening one
	// only shows the join page; a place is taken by the POST it sends.
	app.Get( "/join/*" , handlers.JoinPage )
	// The readable API and MCP references, for an app with no app.repo to
	// link to (api_docs_md.go).
	app.Get( "/docs/*" , handlers.ShowDocsPage )


	handlers.mountDeclared( app )

	// Open pages wait here to hear about a change to the frontend
	// (server.live_reload; server/livereload, static/js/live.js). Not part of
	// the API.
	if handlers.LiveReload != nil {
		app.Get( "/api/live" , handlers.LiveReload.Handler )
	}

	// Any other /api/ path is a JSON 404, not a static page.
	app.Get( "/api/*" , func( c fiber.Ctx ) ( err error ) {
		err = c.Status( fiber.StatusNotFound ).JSON( fiber.Map{ "error": "not found" } )
		return
	} )

	// Catch-all static serving. Must be registered last -- it matches every
	// remaining path, so anything added after this line never runs.
	app.Get( "/*" , handlers.Static.Handler )
}

// featureGuards are the switches of the features that can be off while the
// server runs, by feature name: a route whose op says feature=x answers 404
// while x is off, exactly what an unregistered route would say. The route
// table puts the switch in front of every other guard. apidoc/gen reads
// this map's keys.
func ( handlers *Handlers ) featureGuards() ( guards map[string]fiber.Handler ) {
	guards = map[string]fiber.Handler{
		"api_keys":     handlers.RequireAPIKeys,
		"logs":         handlers.RequireLogs,
	}
	return
}

func ( handlers *Handlers ) cfg() ( cfg *config.Config ) {
	cfg = handlers.Settings.Get()
	return
}


// notFound keeps an error body the shape every refusal has, so the frontend
// has one shape to handle and no guard accidentally leaks internal detail.
func notFound( c fiber.Ctx , message string ) ( err error ) {
	err = c.Status( fiber.StatusNotFound ).JSON( fiber.Map{ "error": message } )
	return
}

func forbidden( c fiber.Ctx , message string ) ( err error ) {
	err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": message } )
	return
}

// restoreRefusal answers each way a restore can be refused.
func restoreRefusal( cause error ) ( refused *refusal ) {
	switch {
	case errors.Is( cause , models.ErrRestoreGone ):
		refused = refuse( fiber.StatusNotFound , cause.Error() )
	case errors.Is( cause , models.ErrRestoreForbidden ):
		refused = refuse( fiber.StatusForbidden , cause.Error() )
	case errors.Is( cause , models.ErrRestoreOutranked ):
		refused = refuse( fiber.StatusForbidden , cause.Error() )
	case errors.Is( cause , models.ErrRestoreExpired ):
		refused = refuse( fiber.StatusConflict , cause.Error() )
	case errors.Is( cause , models.ErrAPIKeyLimit ):
		refused = refuse( fiber.StatusConflict , "its owner already has the maximum number of live API keys" )
	case errors.Is( cause , models.ErrOwnerProtected ):
		refused = refuse( fiber.StatusForbidden , "nobody can restore the owner account's keys" )
	case errors.Is( cause , models.ErrOwnerRequired ):
		refused = refuse( fiber.StatusForbidden , "only the owner can restore an admin account's keys" )
	default:
		refused = internalFailure( cause )
	}
	return
}

// serverError ends a request that failed on our side. It writes nothing:
// handing cause back up is what gets it to the log, and to the ErrorHandler
// (server/middleware/errors.go), which answers with the generic message and a
// reference the person can quote. The cause itself never reaches them.
func serverError( _ fiber.Ctx , cause error ) ( err error ) {
	err = cause
	if err == nil {
		err = errors.New( "internal error" )
	}
	return
}

// bulkFailure logs an item of a bulk request that failed on our side, the
// way the ErrorHandler logs a request, and answers the reference to it.
func bulkFailure( c fiber.Ctx , cause error ) ( request_id string ) {
	request_id = middleware.RequestIDFrom( c )
	log.Printf( "[error] %s %s %s (bulk item): %v" , request_id , c.Method() , c.Path() , cause )
	return
}

// jsonObjects is rows as the JSON objects they encode to, for a reply that
// passes on what another package keeps without saying its shape.
func jsonObjects[ T any ]( rows []T ) ( objects []map[string]any , err error ) {
	objects = []map[string]any{}
	encoded , err := json.Marshal( rows )
	if err != nil { return }
	err = json.Unmarshal( encoded , &objects )
	if objects == nil { objects = []map[string]any{} }
	return
}
