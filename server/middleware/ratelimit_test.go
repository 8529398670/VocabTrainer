package middleware

import (
	bytes "bytes"
	fmt "fmt"
	log "log"
	http "net/http"
	os "os"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
)

// What these pin down: a page's files never spend the budget, the two budgets
// are separate, and no spelling of a path gets around either.

func limitedApp( general int , sign_in int ) ( app *fiber.App ) {
	cfg := &config.Config{ RateLimitMax: general , RateLimitSignInMax: sign_in , RateLimitWindow: time.Minute }
	settings := config.NewHolder( cfg )
	app = fiber.New()
	app.Use( RateLimit( settings , nil ) )
	app.All( "/*" , func( c fiber.Ctx ) ( err error ) {
		err = c.SendString( "ok" )
		return
	} )
	return
}

func status( t *testing.T , app *fiber.App , method string , target string ) ( code int ) {
	t.Helper()
	request , err := http.NewRequest( method , target , nil )
	if err != nil { t.Fatal( err ) }
	response , err := app.Test( request )
	if err != nil { t.Fatal( err ) }
	response.Body.Close()
	code = response.StatusCode
	return
}

func TestStaticFilesNeverSpendTheBudget( t *testing.T ) {
	app := limitedApp( 2 , 2 )
	for count := 0; count < 50; count++ {
		for _ , target := range []string{ "/js/app.js" , "/css/style.css" , "/vendor/bootstrap/bootstrap.min.js" } {
			if code := status( t , app , http.MethodGet , target ); code != 200 {
				t.Fatalf( "%s, request %d: %d" , target , count , code )
			}
		}
	}
	// None of that counted, so the whole budget is still here.
	for count := 0; count < 2; count++ {
		if code := status( t , app , http.MethodGet , "/api/me" ); code != 200 { t.Fatalf( "api call %d: %d" , count , code ) }
	}
	if code := status( t , app , http.MethodGet , "/api/me" ); code != 429 { t.Fatalf( "past the budget: %d" , code ) }
}


func TestTheBudgetsAreSeparate( t *testing.T ) {
	app := limitedApp( 3 , 2 )
	for count := 0; count < 2; count++ {
		if code := status( t , app , http.MethodPost , "/api/login" ); code != 200 { t.Fatalf( "sign-in %d: %d" , count , code ) }
	}
	if code := status( t , app , http.MethodPost , "/api/login" ); code != 429 { t.Fatalf( "past the sign-in budget: %d" , code ) }
	if code := status( t , app , http.MethodGet , "/api/me" ); code != 200 { t.Fatalf( "the general budget was spent by sign-in: %d" , code ) }
}

func TestNoSpellingGetsAroundTheBudgets( t *testing.T ) {
	spellings := []string{ "/API/login" , "/Api/Login/" , "/api/login/" }
	for _ , target := range spellings {
		app := limitedApp( 100 , 1 )
		status( t , app , http.MethodPost , "/api/login" )
		if code := status( t , app , http.MethodPost , target ); code != 429 {
			t.Errorf( "%s got past the sign-in budget: %d" , target , code )
		}
	}
	// Spelled to look like a static file, but it reaches an API route.
	for _ , target := range []string{ "/API/owner/logs/days/2026.11" , "/Api/me.json" , "/MCP.json" , "/LOGIN/abc.def" , "/JOIN/abc.def" } {
		app := limitedApp( 1 , 100 )
		status( t , app , http.MethodGet , "/api/me" )
		if code := status( t , app , http.MethodGet , target ); code != 429 {
			t.Errorf( "%s counted as a static file: %d" , target , code )
		}
	}
	// Only a read of a file is exempt.
	app := limitedApp( 1 , 100 )
	status( t , app , http.MethodPost , "/js/app.js" )
	if code := status( t , app , http.MethodPost , "/js/app.js" ); code != 429 {
		t.Errorf( "a POST counted as a static file: %d" , code )
	}
}

func TestThePageItselfStillCounts( t *testing.T ) {
	app := limitedApp( 1 , 100 )
	status( t , app , http.MethodGet , "/" )
	if code := status( t , app , http.MethodGet , "/" ); code != 429 { t.Fatalf( "/ is exempt: %d" , code ) }
}

// S10: an IPv6 client is counted by its /64, so picking a new address inside
// one does not get a new budget. IPv4 is counted as it is.
func TestIPv6IsCountedByItsSlash64( t *testing.T ) {
	if rateKey( "2001:db8:1:2::1" ) != rateKey( "2001:db8:1:2:ffff::9" ) { t.Errorf( "one /64, two budgets" ) }
	if rateKey( "2001:db8:1:2::1" ) == rateKey( "2001:db8:1:3::1" ) { t.Errorf( "two /64s, one budget" ) }
	if rateKey( "192.0.2.1" ) != "192.0.2.1" || rateKey( "::ffff:192.0.2.1" ) != "192.0.2.1" { t.Errorf( "IPv4: %q" , rateKey( "::ffff:192.0.2.1" ) ) }

	// Through the limiter: twelve addresses in one /64 share five tries.
	app := proxiedApp( t , true )
	refused := 0
	for count := 1; count <= 12; count++ {
		if code := statusFrom( t , app , "/api/login" , "X-Real-IP" , fmt.Sprintf( "2001:db8:1:2::%x" , count ) ); code == 429 { refused++ }
	}
	if refused != 12 - 5 { t.Fatalf( "%d of 12 refused, want 7" , refused ) }
}

// S10: a request through a proxy the server does not trust is warned about
// once per process, and the first 429 a budget gives in a window is logged,
// once.
func TestProxyHeadersAndRefusalsAreLogged( t *testing.T ) {
	var written bytes.Buffer
	log.SetOutput( &written )
	t.Cleanup( func() { log.SetOutput( os.Stderr ) } )

	app := proxiedApp( t , false )
	for count := 0; count < 8; count++ {
		statusFrom( t , app , "/api/login" , "CF-Connecting-IP" , "198.51.100.7" )
	}
	text := written.String()
	if strings.Count( text , "CF-Connecting-IP" ) != 1 || strings.Contains( text , "server.trust_proxy" ) == false {
		t.Errorf( "proxy warning, want once:\n%s" , text )
	}
	if strings.Count( text , "budget used up" ) != 1 || strings.Contains( text , "sign-in budget" ) == false {
		t.Errorf( "429 notice, want once:\n%s" , text )
	}
}

// proxiedApp has a sign-in budget of five and reads the visitor's address
// from X-Real-IP when trust is on (app.Test's own address is 0.0.0.0).
func proxiedApp( t *testing.T , trust bool ) ( app *fiber.App ) {
	t.Helper()
	cfg := &config.Config{ RateLimitMax: 100 , RateLimitSignInMax: 5 , RateLimitWindow: time.Minute , TrustProxy: trust }
	app = fiber.New( fiber.Config{
		TrustProxy:         trust,
		TrustProxyConfig:   fiber.TrustProxyConfig{ Proxies: []string{ "0.0.0.0" } },
		ProxyHeader:        "X-Real-IP",
		EnableIPValidation: true,
	} )
	app.Use( RateLimit( config.NewHolder( cfg ) , nil ) )
	app.All( "/*" , func( c fiber.Ctx ) ( err error ) {
		err = c.SendString( "ok" )
		return
	} )
	return
}

func statusFrom( t *testing.T , app *fiber.App , target string , header string , value string ) ( code int ) {
	t.Helper()
	request , _ := http.NewRequest( http.MethodPost , target , nil )
	request.Header.Set( header , value )
	response , err := app.Test( request )
	if err != nil { t.Fatal( err ) }
	response.Body.Close()
	code = response.StatusCode
	return
}
