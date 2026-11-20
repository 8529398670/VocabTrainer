package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
)

// The readable references: config/api.md and config/mcp.md, the same as
// api.json and mcp.json but as tables for people. With app.repo set, the
// cards on Account link to the repository's own rendering of them; without
// it, to this server's /docs/api and /docs/mcp pages (static/docs.html),
// which fetch the Markdown from the routes below and render it.

// getAPIDoc hands out config/api.md, which scripts/api_docs.sh writes beside
// api.json. Signed-in callers only, like api.json.
//
// @route("GET /api/api.md", summary="The API reference as Markdown tables for people",
//     tag="Monitoring", feature="openapi", errors=(404,), reply_type="text/markdown",
//     mcp_http="The API reference itself, for people; GET /api/api.json is the same as JSON.",
// )
func ( handlers *Handlers ) getAPIDoc( c fiber.Ctx , in *apidoc.None ) ( err error ) {
	err = handlers.sendMarkdown( c , handlers.APIDoc )
	return
}

// apiDocURL is where the readable API reference is read: config/api.md in
// app.repo when that is set, so the repository's site renders it, else this
// server's own page.
func ( handlers *Handlers ) apiDocURL() ( result string ) {
	result = handlers.cfg().RepoFile( "config/api.md" )
	if result == "" {
		result = "/docs/api"
	}
	return
}


func ( handlers *Handlers ) sendMarkdown( c fiber.Ctx , body []byte ) ( err error ) {
	if len( body ) == 0 {
		err = notFound( c , "not found" )
		return
	}
	c.Set( fiber.HeaderContentType , "text/markdown; charset=utf-8" )
	err = c.Send( body )
	return
}

// ShowDocsPage is /docs/api and /docs/mcp: static/docs.html, which reads its
// own path to pick the file and shows an error for any other /docs/ path.
// The page itself is public, like index.html; the Markdown it fetches needs
// a sign-in.
func ( handlers *Handlers ) ShowDocsPage( c fiber.Ctx ) ( err error ) {
	c.Set( fiber.HeaderReferrerPolicy , "no-referrer" )
	c.Set( "X-Robots-Tag" , "noindex" )
	err = handlers.Static.ServeFile( c , "docs.html" )
	return
}
