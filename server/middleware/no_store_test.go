package middleware

import (
	http "net/http"
	strings "strings"
	testing "testing"
	fstest "testing/fstest"

	fiber "github.com/gofiber/fiber/v3"

	static "vocabtrainer/server/static"
)

// S11: NoStore runs before the static file server, which keeps its own rule:
// images are still cached hard, text is kept only by the browser and
// re-checked every load, and a 404 for a missing file is not stored at all.
func TestNoStoreLeavesTheStaticRuleAlone( t *testing.T ) {
	server , _ := static.New( fstest.MapFS{
		"index.html":  { Data: []byte( "index" ) },
		"img/logo.png": { Data: []byte( "\x89PNG" ) },
	} )
	app := fiber.New()
	app.Use( NoStore() )
	app.Get( "/*" , server.Handler )
	for path , want := range map[ string ]string{ "/img/logo.png": "max-age=" , "/index.html": "private, no-cache" , "/nope.png": "no-store" } {
		request , _ := http.NewRequest( http.MethodGet , path , nil )
		response , err := app.Test( request )
		if err != nil { t.Fatal( err ) }
		response.Body.Close()
		if cache := response.Header.Get( fiber.HeaderCacheControl ); strings.Contains( cache , want ) == false {
			t.Errorf( "%s: Cache-Control %q, want %q" , path , cache , want )
		}
	}
}
