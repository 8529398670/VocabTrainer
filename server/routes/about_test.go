package routes

import (
	http "net/http"
	testing "testing"
)

// /api/me says where the API reference is: api.json in app.repo when that
// is set, else this server's own copy.
func TestMeSaysWhereTheAPIReferenceIs( t *testing.T ) {
	fixture := newAuthFixture( t )
	alice := fixture.signIn( t , fixture.alice.ID )
	if _ , me := alice.do( t , http.MethodGet , "/api/me" , nil ); me[ "openapi_url" ] != "/api/api.json" || me[ "repo" ] != "" {
		t.Fatalf( "without a repo: %v" , me )
	}
	fixture.cfg.Repo = "https://github.com/you/app"
	fixture.cfg.Description = "Counts things."
	_ , me := alice.do( t , http.MethodGet , "/api/me" , nil )
	if me[ "openapi_url" ] != "https://github.com/you/app/blob/HEAD/config/api.json" || me[ "repo" ] != "https://github.com/you/app" || me[ "description" ] != "Counts things." {
		t.Fatalf( "with a repo: %v" , me )
	}
}
