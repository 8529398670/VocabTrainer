// Package middleware holds the cross-cutting protections that apply to every
// request no matter which route answers it. Keeping them here means a route
// handler never has to remember a header or a throttle -- if it is registered
// on the app, it is covered.
package middleware

import (
	log "log"
	netip "net/netip"
	path "path"
	strings "strings"
	sync "sync"
	atomic "sync/atomic"
	time "time"

	fiber "github.com/gofiber/fiber/v3"
	helmet "github.com/gofiber/fiber/v3/middleware/helmet"
	limiter "github.com/gofiber/fiber/v3/middleware/limiter"
	recoverer "github.com/gofiber/fiber/v3/middleware/recover"

	config "vocabtrainer/server/config"
	logs "vocabtrainer/server/logs"
)

// robotsPolicy keeps every response out of search results.
const robotsPolicy = "noindex, nofollow"

// SecurityHeaders assumes everything the page needs is same-origin, which is
// true for this template on purpose: libraries are downloaded once and served
// from static/vendor/ rather than pulled from a CDN at runtime. That is what
// lets the CSP stay this tight, and it is why adding a <script src> pointing
// at some other origin will visibly break -- the fix is to vendor the file,
// not to loosen the policy.
//
// server.secure_cookies decides HSTS and can change while the server runs,
// so both variants are built here and the current setting picks one per
// request.
func SecurityHeaders( settings *config.Holder ) ( handler fiber.Handler ) {
	secure , plain := securityHeaders( true ) , securityHeaders( false )
	handler = func( c fiber.Ctx ) ( err error ) {
		// helmet has no field for this one. Every app built from this template
		// is private, and robots.txt only asks: this covers a crawler that
		// reaches a page anyway.
		c.Set( fiber.HeaderXRobotsTag , robotsPolicy )
		if settings.Get().SecureCookies {
			err = secure( c )
			return
		}
		err = plain( c )
		return
	}
	return
}

func securityHeaders( secure_cookies bool ) ( handler fiber.Handler ) {
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
	if secure_cookies {
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

// rateBudget is which limit a request counts against.
type rateBudget int

const (
	budgetGeneral rateBudget = iota
	budgetSignIn
	budgetNone
)

// signInPaths are the exact POSTs that try a credential. Joining through an
// invite link counts: each of those calls takes a PIN or mints an account.
var signInPaths = map[string]bool{
	"/api/login":            true,
}


// staticAsset is a file the page loads (/js/app.js, /vendor/...), as opposed
// to the page itself, an API call, or a login or invite link -- whose
// credential can contain a dot, which is why those are ruled out first.
func staticAsset( method string , request_path string ) ( result bool ) {
	if method != fiber.MethodGet && method != fiber.MethodHead { return }
	for _ , prefix := range []string{ "/api/" , "/login/" , "/join/" , "/mcp" } {
		if strings.HasPrefix( request_path , prefix ) { return }
	}
	result = path.Ext( request_path ) != ""
	return
}

// budgetFor sorts a request.
//
// The path is normalised first because fiber's routing is not strict: it
// ignores case and a trailing slash, so /API/LOGIN/ reaches the same handler
// as /api/login. Comparing the raw path would let a caller pick a spelling
// that skips the sign-in budget, or one that passes for a static file.
//
// Static files are exempt because index.html alone pulls in more than twenty,
// all no-store, so counting them meant about five reloads a minute hit a 429.
// Serving one touches no database and no credential. The page itself is not
// exempt, and neither is anything under /api.
func budgetFor( method string , request_path string ) ( budget rateBudget ) {
	normalised := normalisedPath( request_path )
	switch {
	case method == fiber.MethodPost && signInPaths[ normalised ]:
		budget = budgetSignIn
	case staticAsset( method , normalised ):
		budget = budgetNone
	}
	return
}

// normalisedPath is a request path the way the router matches it: in any
// case, and with or without a trailing slash. Whatever decides by path (the
// rate budgets, the request log's skip list) reads this, so no spelling of a
// path gets a different answer from the route it reaches.
func normalisedPath( request_path string ) ( normalised string ) {
	normalised = strings.ToLower( request_path )
	if len( normalised ) > 1 {
		normalised = strings.TrimRight( normalised , "/" )
	}
	return
}

// RateLimit is a per-process, in-memory limiter. That is exactly right for
// the single container docker/run.sh builds. Run more than one replica and the
// counters stop being shared, so each replica would allow the full budget --
// move to a shared store (limiter.Config.Storage) at that point rather than
// assuming the limit still holds.
//
// Each address has three budgets over the same window: one for signing in, one
// for the chunks of an upload, and one for everything else. Keeping them apart
// means a page full of API calls cannot lock someone out of signing in, and
// guessing at sign-in does not get the budget the rest of the app has.
//
// The budgets and the window are read per request, so an edit to any of
// server.rate_limit_max, rate_limit_signin_max, rate_limit_upload_max or
// rate_limit_window_seconds applies at once.
//
// A budget belongs to an address, or for IPv6 to its /64 (rateKey): one home
// or one server gets a whole /64, so counting each /128 alone would hand
// whoever has one as many budgets as they care to pick addresses.
//
// It writes to logger (nil is fine) and the server's own log twice over: the
// first 429 a budget gives in a window, so a lockout can be seen, and once per
// process when a request carries a proxy's address header while
// server.trust_proxy is off (warnProxyHeaders). RequestLog runs after this, so
// those 429s would otherwise appear nowhere.
func RateLimit( settings *config.Holder , logger *logs.Logger ) ( handler fiber.Handler ) {
	refusals := &limitNotices{ last: map[ string ]time.Time{} }
	limited := func( prefix string , limit func( cfg *config.Config ) int ) ( result fiber.Handler ) {
		result = limiter.New( limiter.Config{
			MaxFunc: func( fiber.Ctx ) ( max int ) {
				max = limit( settings.Get() )
				return
			},
			ExpirationFunc: func( fiber.Ctx ) ( window time.Duration ) {
				window = settings.Get().RateLimitWindow
				return
			},
			KeyGenerator: func( c fiber.Ctx ) ( key string ) {
				key = prefix + rateKey( c.IP() )
				return
			},
			LimitReached: func( c fiber.Ctx ) ( err error ) {
				refusals.note( c , logger , strings.TrimSuffix( prefix , ":" ) , rateKey( c.IP() ) , settings.Get().RateLimitWindow )
				// Through the ErrorHandler: a page load gets the error page,
				// and everything else the usual JSON.
				err = fiber.ErrTooManyRequests
				return
			},
		} )
		return
	}
	general := limited( "general:" , func( cfg *config.Config ) int { return cfg.RateLimitMax } )
	sign_in := limited( "sign-in:" , func( cfg *config.Config ) int { return cfg.RateLimitSignInMax } )

	var warned atomic.Bool
	handler = func( c fiber.Ctx ) ( err error ) {
		if warned.Load() == false { warnProxyHeaders( c , settings.Get() , logger , &warned ) }
		switch budgetFor( c.Method() , c.Path() ) {
		case budgetNone:
			err = c.Next()
		case budgetSignIn:
			err = sign_in( c )
		default:
			err = general( c )
		}
		return
	}
	return
}

// rateKey is who a budget belongs to: an IPv4 address as it is, an IPv6
// address by its /64. Anything that does not parse is used as it is.
func rateKey( ip string ) ( key string ) {
	key = ip
	address , err := netip.ParseAddr( ip )
	if err != nil { return }
	address = address.Unmap()
	if address.Is4() {
		key = address.String()
		return
	}
	prefix , err := address.WithZone( "" ).Prefix( 64 )
	if err != nil { return }
	key = prefix.String()
	return
}

// proxyHeaders are the headers a proxy names the visitor's address in.
var proxyHeaders = []string{ "CF-Connecting-IP" , "X-Forwarded-For" , "X-Real-IP" }

// warnProxyHeaders says, once per process, that a request came through a
// proxy the server does not trust. Then every visitor has the proxy's address,
// so they all share one budget: 60 sign-in attempts a minute from anyone
// would lock everyone out. That is the default behind Cloudflare Tunnel or a
// local proxy until server.trust_proxy is set.
func warnProxyHeaders( c fiber.Ctx , cfg *config.Config , logger *logs.Logger , warned *atomic.Bool ) {
	if cfg.TrustProxy { return }
	for _ , name := range proxyHeaders {
		if c.Get( name ) == "" { continue }
		if warned.Swap( true ) { return }
		message := "requests carry " + name + " but server.trust_proxy is off, so every visitor is counted as the proxy's address " +
			c.IP() + " and they share one rate limit; see server.trust_proxy in config.yaml"
		log.Printf( "[rate-limit] %s" , message )
		if logger != nil { logger.System( logs.LevelWarn , message , map[string]any{ "header": name , "peer": c.IP() } ) }
		return
	}
}

// limitNotices remembers when each budget last said it was used up, so a
// flood of refused requests writes one line per budget per window, not one
// each.
type limitNotices struct {
	lock sync.Mutex
	last map[ string ]time.Time
}

func ( notices *limitNotices ) note( c fiber.Ctx , logger *logs.Logger , budget string , key string , window time.Duration ) {
	now := time.Now()
	bucket := budget + ":" + key
	notices.lock.Lock()
	if now.Sub( notices.last[ bucket ] ) < window {
		notices.lock.Unlock()
		return
	}
	if len( notices.last ) > 10000 {
		for name , when := range notices.last {
			if now.Sub( when ) >= window { delete( notices.last , name ) }
		}
	}
	notices.last[ bucket ] = now
	notices.lock.Unlock()
	log.Printf( "[rate-limit] %s budget used up for %s (%s %s); more 429s for it this window are not logged" , budget , key , c.Method() , c.Path() )
	if logger != nil {
		logger.System( logs.LevelWarn , "rate limit reached" , map[string]any{ "budget": budget , "key": key , "method": c.Method() , "path": c.Path() , "window_seconds": int( window.Seconds() ) } )
	}
}

// Recover keeps one panicking handler from taking the process down with it.
// Stack traces stay off by default: they are enormously useful in a terminal
// and a gift to an attacker if they ever reach a response body.
func Recover( settings *config.Holder ) ( handler fiber.Handler ) {
	traced := recoverer.New( recoverer.Config{ EnableStackTrace: true } )
	quiet := recoverer.New( recoverer.Config{ EnableStackTrace: false } )
	handler = func( c fiber.Ctx ) ( err error ) {
		if settings.Get().SecureCookies {
			err = quiet( c )
			return
		}
		err = traced( c )
		return
	}
	return
}
