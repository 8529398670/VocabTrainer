// Package routes contains one file per group of endpoints, plus this file,
// which is the only place that knows the full URL map.
//
// The API here stays deliberately small: it covers logging in, managing who
// can log in, and reading the UI's text. Everything else an app does belongs
// in a new file in this package (server side) and in static/js/ (browser
// side) -- see references/architecture.md for where the line sits and why.
package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	corpus "vocabtrainer/server/corpus"
	db "vocabtrainer/server/db"
	language "vocabtrainer/server/language"
	security "vocabtrainer/server/security"
	static "vocabtrainer/server/static"
)

// Handlers carries the dependencies every route group needs. Passing this in
// rather than reaching for package-level globals is what makes the handlers
// testable and keeps the wiring visible in main.go.
type Handlers struct {
	Store    *db.Store
	Config   *config.Config
	Guard    *security.Guard
	Language *language.Language
	Static   *static.Server

	// Corpus is the word list, loaded once at startup and read-only
	// thereafter, so every request shares it without locking.
	Corpus *corpus.Corpus
}

func New( store *db.Store , cfg *config.Config , guard *security.Guard , lang *language.Language , static_server *static.Server , words *corpus.Corpus ) ( handlers *Handlers ) {
	handlers = &Handlers{
		Store:    store,
		Config:   cfg,
		Guard:    guard,
		Language: lang,
		Static:   static_server,
		Corpus:   words,
	}
	return
}

// Register wires every route. Read it top to bottom to see the whole surface
// of this server.
func ( handlers *Handlers ) Register( app *fiber.App ) {
	// Resolve the session once, before anything else looks at the request.
	app.Use( handlers.Guard.LoadUser )

	// Public: the login link itself, and the UI's text (needed to render the
	// signed-out page at all).
	app.Get( "/login/*" , handlers.RedeemLoginLink )
	app.Get( "/api/language" , handlers.GetLanguage )
	app.Get( "/api/health" , handlers.GetHealth )

	// Public: joining through an invite link. Both GETs are deliberately
	// free of side effects -- a seat is spent only by the POST. See
	// routes/invite.go, which is mostly about why.
	app.Get( "/join/*" , handlers.JoinPage )
	app.Get( "/api/invite/*" , handlers.GetInvite )
	app.Post( "/api/invite/claim" , handlers.ClaimInvite )

	// Any signed-in user.
	account := app.Group( "/api" , handlers.Guard.RequireLogin )
	account.Get( "/me" , handlers.GetMe )
	account.Post( "/account/rename" , handlers.RenameAccount )
	account.Post( "/logout" , handlers.Logout )
	account.Get( "/team" , handlers.ListTeam )

	// Training. Everything a signed-in user does with words: getting a deck,
	// swiping, and the three lists the swipes build up.
	account.Get( "/deck" , handlers.GetDeck )
	account.Post( "/review" , handlers.PostReview )
	account.Get( "/cards" , handlers.GetCards )
	account.Post( "/cards/status" , handlers.PostCardStatus )
	account.Post( "/cards/unskip" , handlers.PostUnskip )
	account.Post( "/cards/forget" , handlers.PostForget )
	account.Get( "/search" , handlers.GetSearch )

	// Preferences and progress.
	account.Get( "/settings" , handlers.GetSettings )
	account.Post( "/settings" , handlers.PostSettings )
	account.Get( "/levels" , handlers.GetLevels )
	account.Get( "/stats" , handlers.GetStats )
	account.Post( "/progress/reset" , handlers.PostResetProgress )

	// The three word lists as a spreadsheet. A GET rather than a POST
	// because it changes nothing and because a link is the only download
	// that behaves itself on a phone -- see routes/export.go.
	account.Get( "/export/words.xlsx" , handlers.GetWordsExport )

	// Admin only: deciding who gets in. Note this is the *only* thing admin
	// rights gate in the base template -- see architecture.md on defaulting
	// new features to "any signed-in user".
	admin := app.Group( "/api/admin" , handlers.Guard.RequireAdmin )
	admin.Get( "/users" , handlers.ListUsers )
	admin.Post( "/users" , handlers.CreateUser )
	admin.Post( "/users/:user_id/reissue-login" , handlers.ReissueLogin )
	admin.Post( "/users/:user_id/disabled" , handlers.SetUserDisabled )
	admin.Get( "/invites" , handlers.ListInvites )
	admin.Post( "/invites" , handlers.CreateInvite )
	admin.Post( "/invites/:invite_id/revoke" , handlers.RevokeInvite )

	// Catch-all static serving. Must be registered last -- it matches every
	// remaining path, so anything added after this line never runs.
	app.Get( "/*" , handlers.Static.Handler )
}

// badRequest / notFound keep error bodies uniform, so the frontend has one
// shape to handle and no handler accidentally leaks internal detail.
func badRequest( c fiber.Ctx , message string ) ( err error ) {
	err = c.Status( fiber.StatusBadRequest ).JSON( fiber.Map{ "error": message } )
	return
}

func notFound( c fiber.Ctx , message string ) ( err error ) {
	err = c.Status( fiber.StatusNotFound ).JSON( fiber.Map{ "error": message } )
	return
}

func forbidden( c fiber.Ctx , message string ) ( err error ) {
	err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": message } )
	return
}

func serverError( c fiber.Ctx ) ( err error ) {
	err = c.Status( fiber.StatusInternalServerError ).JSON( fiber.Map{ "error": "internal error" } )
	return
}
