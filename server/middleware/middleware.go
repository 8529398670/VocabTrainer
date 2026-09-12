// Package middleware holds the cross-cutting protections that apply to every
// request no matter which route answers it. Keeping them here means a route
// handler never has to remember a header or a throttle -- if it is registered
// on the app, it is covered.
package middleware

import (
	fiber "github.com/gofiber/fiber/v3"
	helmet "github.com/gofiber/fiber/v3/middleware/helmet"
	limiter "github.com/gofiber/fiber/v3/middleware/limiter"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"

	config "vocabtrainer/server/config"
)

// SecurityHeaders assumes everything the page needs is same-origin, which is
// true for this template on purpose: libraries are downloaded once and served
// from static/vendor/ rather than pulled from a CDN at runtime. That is what
// lets the CSP stay this tight, and it is why adding a <script src> pointing
// at some other origin will visibly break -- the fix is to vendor the file,
// not to loosen the policy.
func SecurityHeaders( cfg *config.Config ) ( handler fiber.Handler ) {
	content_security_policy := "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self'; " +
		"img-src 'self' data:; " +
		"font-src 'self'; " +
		"connect-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'"

	hsts_max_age := 0
	if cfg.SecureCookies {
		// Only claim HTTPS-only when the deployment actually is. Sending HSTS
		// from a local http:// run would pin the browser to https for
		// localhost and break every other project on that port.
		hsts_max_age = 63072000
	}

	handler = helmet.New( helmet.Config{
		ContentSecurityPolicy:     content_security_policy,
		XFrameOptions:             "DENY",
		ContentTypeNosniff:        "nosniff",
		ReferrerPolicy:            "same-origin",
		PermissionPolicy:          "geolocation=(), microphone=(), camera=(), payment=(), usb=()",
		CrossOriginOpenerPolicy:   "same-origin",
		CrossOriginResourcePolicy: "same-origin",
		XDNSPrefetchControl:       "off",
		HSTSMaxAge:                hsts_max_age,
		HSTSExcludeSubdomains:     false,
	} )
	return
}

// RateLimit is a per-process, in-memory limiter. That is exactly right for
// the single container dockerRun.sh builds. Run more than one replica and the
// counters stop being shared, so each replica would allow the full budget --
// move to a shared store (limiter.Config.Storage) at that point rather than
// assuming the limit still holds.
func RateLimit( cfg *config.Config ) ( handler fiber.Handler ) {
	handler = limiter.New( limiter.Config{
		Max:        cfg.RateLimitMax,
		Expiration: cfg.RateLimitWindow,
		KeyGenerator: func( c fiber.Ctx ) ( result string ) {
			result = c.IP()
			return
		},
		LimitReached: func( c fiber.Ctx ) ( err error ) {
			err = c.Status( fiber.StatusTooManyRequests ).JSON( fiber.Map{ "error": "too many requests" } )
			return
		},
	} )
	return
}

// Recover keeps one panicking handler from taking the process down with it.
// Stack traces stay off by default: they are enormously useful in a terminal
// and a gift to an attacker if they ever reach a response body.
func Recover( cfg *config.Config ) ( handler fiber.Handler ) {
	handler = recoverer.New( recoverer.Config{ EnableStackTrace: cfg.SecureCookies == false } )
	return
}
