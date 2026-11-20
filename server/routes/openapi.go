package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
)

// getOpenAPI hands out config/api.json, which scripts/api_docs.sh writes
// from the routes as each declares itself (the // @route(...) of its
// handler); api_docs_test.go fails while it is out of date. Signed-in callers
// only: it maps the admin surface, and nobody who cannot sign in has a use for
// it.
//
// @route("GET /api/api.json", summary="This document",
//     tag="Monitoring", feature="openapi", errors=(404,), reply_type="application/json",
//     mcp_http="The API reference itself; GET /api/mcp.json is the MCP one.",
// )
func ( handlers *Handlers ) getOpenAPI( c fiber.Ctx , in *apidoc.None ) ( err error ) {
	if len( handlers.OpenAPI ) == 0 {
		err = notFound( c , "not found" )
		return
	}
	c.Set( fiber.HeaderContentType , fiber.MIMEApplicationJSON )
	err = c.Send( handlers.OpenAPI )
	return
}
