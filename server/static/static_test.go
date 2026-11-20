package static

import (
	io "io"
	os "os"
	strings "strings"
	http "net/http"
	testing "testing"
	fstest "testing/fstest"

	fiber "github.com/gofiber/fiber/v3"
)

// S2: the static handler never serves a dotfile, whatever directory it was
// pointed at. .well-known/ is public by definition and still served.
func TestDotfilesAreNeverServed( t *testing.T ) {
	server , _ := New( fstest.MapFS{
		"index.html":                 { Data: []byte( "index" ) },
		".env":                       { Data: []byte( "SECRET=1" ) },
		".git/config":                { Data: []byte( "[core]" ) },
		"css/.secret.css":            { Data: []byte( "x" ) },
		".well-known/security.txt":   { Data: []byte( "Contact: x" ) },
	} )
	app := fiber.New()
	app.Get( "/*" , server.Handler )
	get := func( path string ) ( status int , body string ) {
		response , err := app.Test( httptestRequest( path ) )
		if err != nil { t.Fatal( err ) }
		raw , _ := io.ReadAll( response.Body )
		status , body = response.StatusCode , string( raw )
		return
	}
	for _ , path := range []string{ "/.env" , "/.git/config" , "/css/.secret.css" , "/%2Eenv" , "/css/../.env" } {
		if status , body := get( path ); status != http.StatusNotFound {
			t.Errorf( "%s: %d %q" , path , status , body )
		}
	}
	if status , body := get( "/.well-known/security.txt" ); status != http.StatusOK || body != "Contact: x" {
		t.Errorf( ".well-known: %d %q" , status , body )
	}
	if status , _ := get( "/" ); status != http.StatusOK { t.Errorf( "index: %d" , status ) }
}

func httptestRequest( path string ) ( request *http.Request ) {
	request , _ = http.NewRequest( http.MethodGet , path , nil )
	return
}

// A text asset is revalidated: the tag comes back as a 304 with no body
// while the file is unchanged, and a changed file gets a new tag and its bytes.
func TestTextAssetsRevalidate( t *testing.T ) {
	files := fstest.MapFS{ "js/app.js": { Data: []byte( "one" ) } }
	server , _ := New( files )
	app := fiber.New()
	app.Get( "/*" , server.Handler )
	get := func( tag string ) ( response *http.Response , body string ) {
		request := httptestRequest( "/js/app.js" )
		request.Header.Set( "Accept-Encoding" , "gzip" )
		if tag != "" { request.Header.Set( "If-None-Match" , tag ) }
		response , err := app.Test( request )
		if err != nil { t.Fatal( err ) }
		raw , _ := io.ReadAll( response.Body )
		body = string( raw )
		return
	}

	first , _ := get( "" )
	tag := first.Header.Get( "ETag" )
	if first.StatusCode != http.StatusOK || tag == "" { t.Fatalf( "first: %d %q" , first.StatusCode , tag ) }
	if cache := first.Header.Get( "Cache-Control" ); cache != "private, no-cache" { t.Errorf( "Cache-Control %q" , cache ) }

	again , body := get( tag )
	if again.StatusCode != http.StatusNotModified || body != "" { t.Errorf( "unchanged: %d %q" , again.StatusCode , body ) }
	if other , _ := get( `W/"0", ` + tag ); other.StatusCode != http.StatusNotModified { t.Errorf( "listed: %d" , other.StatusCode ) }

	files[ "js/app.js" ] = &fstest.MapFile{ Data: []byte( "two" ) }
	changed , _ := get( tag )
	if changed.StatusCode != http.StatusOK || changed.Header.Get( "ETag" ) == tag { t.Errorf( "changed: %d %q" , changed.StatusCode , changed.Header.Get( "ETag" ) ) }
}

// Each path index.html declares with data-path, and each signed-out reason, is
// answered with index.html; any other path is still a 404. Adding a screen to
// index.html is enough, with no restart.
func TestScreenPathsGetThePage( t *testing.T ) {
	files := fstest.MapFS{ "index.html": { Data: []byte(
		`<section class="view" data-view="users" data-path="/admin/users">` +
		`<section class="card" id="x" data-path="/account/two-factor">` ) } }
	server , _ := New( files )
	app := fiber.New()
	app.Get( "/*" , server.Handler )
	status := func( path string ) ( result int ) {
		response , err := app.Test( httptestRequest( path ) )
		if err != nil { t.Fatal( err ) }
		result = response.StatusCode
		return
	}
	for _ , path := range []string{ "/admin/users" , "/admin/users/" , "/account/two-factor" , "/signed-out/ended" } {
		if got := status( path ); got != http.StatusOK { t.Errorf( "%s: %d, want 200" , path , got ) }
	}
	for _ , path := range []string{ "/admin" , "/admin/users/x" , "/nope" , "/signed-out/other" , "/users" } {
		if got := status( path ); got != http.StatusNotFound { t.Errorf( "%s: %d, want 404" , path , got ) }
	}
	files[ "index.html" ] = &fstest.MapFile{ Data: []byte( `<section class="view" data-view="logs" data-path="/admin/logs">` ) }
	if got := status( "/admin/logs" ); got != http.StatusOK { t.Errorf( "/admin/logs after edit: %d" , got ) }
	if got := status( "/admin/users" ); got != http.StatusNotFound { t.Errorf( "/admin/users after edit: %d" , got ) }
}

// The app icons every page links to are served from the real static/
// directory, each with the type a browser needs to use it.
func TestAppIconsAreServed( t *testing.T ) {
	server , _ := New( os.DirFS( "../../static" ) )
	app := fiber.New()
	app.Get( "/*" , server.Handler )
	for path , want := range map[string]string{
		"/favicon.ico":          "image/",
		"/favicon.svg":          "image/svg+xml",
		"/apple-touch-icon.png": "image/png",
		"/icon-192.png":         "image/png",
		"/icon-512.png":         "image/png",
		"/site.webmanifest":     "application/manifest+json",
	} {
		response , err := app.Test( httptestRequest( path ) )
		if err != nil { t.Fatal( err ) }
		got := response.Header.Get( "Content-Type" )
		if response.StatusCode != http.StatusOK || strings.HasPrefix( got , want ) == false {
			t.Errorf( "%s: %d %q, want %q" , path , response.StatusCode , got , want )
		}
	}
}
