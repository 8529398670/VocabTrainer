package routes

import (
	io "io"
	http "net/http"
	os "os"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"
)

// GET /api/api.md hands out config/api.md to signed-in callers only, and
// /api/me points the API keys card at the /docs/api page that renders it,
// or at the repository's copy when app.repo is set.
func TestTheReadableReferenceIsForSignedInCallers( t *testing.T ) {
	fixture := newAuthFixture( t )
	raw , err := os.ReadFile( "../../config/api.md" )
	if err != nil { t.Fatal( err ) }
	fixture.handlers.APIDoc = raw
	if status , _ := fixture.browser().do( t , http.MethodGet , "/api/api.md" , nil ); status != fiber.StatusUnauthorized {
		t.Fatalf( "a stranger got %d" , status )
	}
	alice := fixture.signIn( t , fixture.alice.ID )
	request , _ := http.NewRequest( http.MethodGet , "/api/api.md" , nil )
	request.Header.Set( "Cookie" , alice.cookie )
	response , err := fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
	if err != nil { t.Fatal( err ) }
	body , _ := io.ReadAll( response.Body )
	if response.StatusCode != fiber.StatusOK || !strings.HasPrefix( response.Header.Get( "Content-Type" ) , "text/markdown" ) || string( body ) != string( raw ) {
		t.Fatalf( "%d %q" , response.StatusCode , response.Header.Get( "Content-Type" ) )
	}

	if _ , me := alice.do( t , http.MethodGet , "/api/me" , nil ); me[ "api_doc_url" ] != "/docs/api" {
		t.Fatalf( "without a repo: %v" , me[ "api_doc_url" ] )
	}
	fixture.cfg.Repo = "https://github.com/you/app"
	if _ , me := alice.do( t , http.MethodGet , "/api/me" , nil ); me[ "api_doc_url" ] != "https://github.com/you/app/blob/HEAD/config/api.md" {
		t.Fatalf( "with a repo: %v" , me[ "api_doc_url" ] )
	}
}
