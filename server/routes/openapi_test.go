package routes

import (
	http "net/http"
	os "os"
	testing "testing"

	fiber "github.com/gofiber/fiber/v3"
)

func specFile( t *testing.T ) ( raw []byte ) {
	t.Helper()
	raw , err := os.ReadFile( "../../config/api.json" )
	if err != nil { t.Fatal( err ) }
	return
}

func TestTheSpecIsForSignedInCallers( t *testing.T ) {
	fixture := newAuthFixture( t )
	fixture.handlers.OpenAPI = specFile( t )
	stranger := fixture.browser()
	if status , _ := stranger.do( t , http.MethodGet , "/api/api.json" , nil ); status != fiber.StatusUnauthorized {
		t.Fatalf( "status %d" , status )
	}
	alice := fixture.signIn( t , fixture.alice.ID )
	status , reply := alice.do( t , http.MethodGet , "/api/api.json" , nil )
	if status != fiber.StatusOK || reply[ "openapi" ] != "3.1.0" {
		t.Fatalf( "%d %v" , status , reply )
	}
}
