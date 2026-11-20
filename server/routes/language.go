package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
)

// getLanguage hands the browser the whole language.yaml tree as JSON. It is
// deliberately public: the signed-out page has text too, and none of it is
// secret. Do not put anything sensitive in language.yaml -- it is, by design,
// readable by anyone who can reach the server.
//
// @route("GET /api/language", summary="The UI's text",
//     tag="Account", access=PUBLIC, reply_type="application/json",
// )
func ( handlers *Handlers ) getLanguage( c fiber.Ctx , in *apidoc.None ) ( err error ) {
	c.Set( fiber.HeaderContentType , fiber.MIMEApplicationJSON )
	c.Set( fiber.HeaderCacheControl , "no-store" )
	err = c.Send( handlers.Language.JSON() )
	return
}

// getHealth is what the Dockerfile HEALTHCHECK hits. It stays trivial on
// purpose -- if it touched the database, a slow query would get the container
// killed and restarted, turning a small problem into an outage.
//
// @route("GET /api/health", summary="Is the process up", tag="Monitoring", access=PUBLIC)
func ( handlers *Handlers ) getHealth( c fiber.Ctx , in *apidoc.None ) ( reply *okReply , err error ) {
	reply = &okReply{ Ok: true }
	return
}
