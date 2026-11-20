package middleware

import (
	fiber "github.com/gofiber/fiber/v3"
)

// NoStore marks every response as private and never to be stored, before
// any handler runs. The static file server overrides it for the files it
// serves (images get a long max-age, text private no-cache with an ETag); everything else,
// every API reply, error page and stream, keeps it.
//
// Without it, a proxy or CDN in front of the app is free to cache an API
// reply: a Cloudflare "cache everything" rule, or its default of caching any
// path that ends in a file extension, would hand the owner's log for a day
// (/api/owner/logs/days/<date>) to whoever asks for the same URL next. Vary
// says the reply depends on who asked, for any cache that ignores no-store.
func NoStore() ( handler fiber.Handler ) {
	handler = func( c fiber.Ctx ) ( err error ) {
		c.Set( fiber.HeaderCacheControl , "no-store, private" )
		c.Set( fiber.HeaderVary , "Cookie, Authorization" )
		err = c.Next()
		return
	}
	return
}
