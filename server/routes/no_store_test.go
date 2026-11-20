package routes

import (
	http "net/http"
	os "os"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	middleware "vocabtrainer/server/middleware"
)

// S11: every route in the table answers with Cache-Control: no-store and
// Vary: Cookie, Authorization, so no proxy or CDN in front of the app may
// keep a copy of a reply for the next visitor. GETs are asked by the signed-in
// owner, so the reply is the real one; other methods fail on the csrf token,
// so they are checked on the error path. Static files keep their own rule.
func TestEveryRouteSaysNoStore( t *testing.T ) {
	// The owner's settings routes read the defaults, and keep what they found.
	defaults , err := os.ReadFile( "../../config/defaults.yaml" )
	if err != nil { t.Fatal( err ) }
	header , err := os.ReadFile( "../../config/example_header.yaml" )
	if err != nil { t.Fatal( err ) }
	config.UseEmbedded( defaults , header )
	fixture := newAuthFixture( t )
	app := fiber.New()
	app.Use( middleware.NoStore() )
	// The fixture leaves out what a few routes need (the language file), and
	// what they do on a panic is beside the point here, as in main.go.
	app.Use( middleware.Recover( config.NewStaticHolder( fixture.cfg ) ) )
	fixture.handlers.Register( app )
	owner := fixture.signIn( t , fixture.owner.ID )

	ask := func( method string , path string , cookie string ) ( response *http.Response ) {
		t.Helper()
		request , _ := http.NewRequest( method , path , strings.NewReader( "{}" ) )
		request.Header.Set( "Content-Type" , "application/json" )
		if cookie != "" { request.Header.Set( "Cookie" , cookie ) }
		response , err := app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
		if err != nil { t.Fatalf( "%s %s: %v" , method , path , err ) }
		response.Body.Close()
		return
	}
	checked := 0
	for _ , route := range app.GetRoutes( true ) {
		if route.Path == "/*" || route.Method == http.MethodHead || route.Method == http.MethodOptions { continue }
		if route.Path == "/api/owner/logs/stream" { continue } // a stream never ends; stream.Serve sets no-store itself
		path := route.Path
		for _ , part := range strings.Split( path , "/" ) {
			if strings.HasPrefix( part , ":" ) || strings.HasPrefix( part , "*" ) { path = strings.Replace( path , part , "1" , 1 ) }
		}
		response := ask( route.Method , path , owner.cookie )
		if cache := response.Header.Get( fiber.HeaderCacheControl ); strings.Contains( cache , "no-store" ) == false {
			t.Errorf( "%s %s (%d): Cache-Control %q" , route.Method , route.Path , response.StatusCode , cache )
		}
		if vary := response.Header.Get( fiber.HeaderVary ); strings.Contains( vary , "Cookie" ) == false && strings.HasPrefix( route.Path , "/api/" ) {
			t.Errorf( "%s %s: Vary %q" , route.Method , route.Path , vary )
		}
		checked++
	}
	if checked < 20 { t.Fatalf( "only %d routes checked" , checked ) }
	// index.html is revalidated, not refused: the browser may keep it, a
	// shared cache may not.
	if cache := ask( http.MethodGet , "/" , "" ).Header.Get( fiber.HeaderCacheControl ); strings.Contains( cache , "private" ) == false || strings.Contains( cache , "no-cache" ) == false {
		t.Errorf( "index.html: %q" , cache )
	}
}
