package routes

import (
	http "net/http"
	regexp "regexp"
	sort "sort"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	middleware "vocabtrainer/server/middleware"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

// Every 4xx answer carries a code from one closed list (portal contract v3,
// item 4), so a caller branches on code and shows error. These tests walk
// the whole route table, so a new route that refuses some other way fails
// here.

var pathParam = regexp.MustCompile( `\{[^}]+\}` )

func listedCode( code any ) ( result bool ) {
	text , _ := code.( string )
	for _ , each := range apidoc.ErrorCodes {
		if each == text { result = true }
	}
	return
}

func wantCode( t *testing.T , what string , status int , reply map[ string ]any , want string ) {
	t.Helper()
	if status < 400 || status > 499 { return }
	if listedCode( reply[ "code" ] ) == false { t.Errorf( "%s: %d %v has no listed code" , what , status , reply ) }
	if want != "" && reply[ "code" ] != want { t.Errorf( "%s: %d %v, want code %s" , what , status , reply , want ) }
	if message , _ := reply[ "error" ].( string ); message == "" { t.Errorf( "%s: %d %v has no error" , what , status , reply ) }
}

func TestMeListsErrorCodes( t *testing.T ) {
	fixture := newAuthFixture( t )
	_ , me := fixture.signIn( t , fixture.alice.ID ).do( t , http.MethodGet , "/api/me" , nil )
	listed := false
	for _ , name := range me[ "capabilities" ].( []any ) { listed = listed || name == "error_codes" }
	if listed == false { t.Fatalf( "%v" , me[ "capabilities" ] ) }
}

func TestEveryStatusHasACode( t *testing.T ) {
	for status := 400; status < 500; status++ {
		if listedCode( apidoc.StatusCode( status ) ) == false { t.Errorf( "%d: %q" , status , apidoc.StatusCode( status ) ) }
	}
	if apidoc.StatusCode( 503 ) != "" || apidoc.StatusCode( 200 ) != "" { t.Error( "a code outside 4xx" ) }
}

func TestEveryRouteRefusesWithACode( t *testing.T ) {
	spec , fixture := apiSpec( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	viewer , _ := models.CreateUser( fixture.store , "Vera" , models.RoleViewer )
	viewer_key , _ , err := models.IssueAPIKey( fixture.store , viewer.ID , "reader" , models.RoleViewer , time.Hour )
	if err != nil { t.Fatal( err ) }
	paths := []string{}
	for path := range spec[ "paths" ].( map[ string ]any ) { paths = append( paths , path ) }
	sort.Strings( paths )
	for _ , path := range paths {
		// MCP's transport answers in JSON-RPC, as its protocol says.
		if path == "/mcp" { continue }
		url := pathParam.ReplaceAllString( path , "1" )
		for method , raw := range spec[ "paths" ].( map[ string ]any )[ path ].( map[ string ]any ) {
			op , _ := raw.( map[ string ]any )
			if op == nil { continue }
			method = map[ string ]string{ "get": http.MethodGet , "post": http.MethodPost , "delete": http.MethodDelete }[ method ]
			what := method + " " + path
			public := len( op[ "security" ].( []any ) ) == 0
			role , _ := op[ "x-required-role" ].( string )

			// No credential: 401 not_authenticated, unless anyone may call it.
			// The owner's routes answer everyone else 404, so they give
			// nothing away.
			status , reply := fixture.browser().do( t , method , url , fiber.Map{} )
			if role == "owner" {
				wantCode( t , what + " with no credential" , status , reply , apidoc.CodeNotFound )
			} else if public == false {
				if status != fiber.StatusUnauthorized { t.Errorf( "%s with no credential: %d %v" , what , status , reply ) }
				wantCode( t , what + " with no credential" , status , reply , apidoc.CodeNotAuthenticated )
			} else {
				wantCode( t , what + " with no credential" , status , reply , "" )
			}

			// An ordinary user's key on an admin's route, a viewer's key writing.
			if role == "admin" || role == "owner" {
				want := map[ string ]string{ "admin": apidoc.CodeNotAdmin , "owner": apidoc.CodeNotFound }[ role ]
				status , reply = fixture.withKey( t , fixture.userKey , method , url , fiber.Map{} )
				wantCode( t , what + " with a user key" , status , reply , want )
			}
			if public == false && method != http.MethodGet && security.ViewerMayCall( method , url ) == false {
				status , reply = fixture.withKey( t , viewer_key , method , url , fiber.Map{} )
				wantCode( t , what + " with a viewer key" , status , reply , apidoc.CodeViewOnly )
			}
			// A field no route has, from the owner's own session.
			if method != http.MethodGet {
				status , reply = send( t , owner , url , fiber.Map{ "not_a_field": 1 } )
				wantCode( t , what + " with an unknown field" , status , reply , "" )
			}
		}
	}
}

func TestTheCodesSayWhy( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	peer , _ := models.CreateUser( fixture.store , "Pia" , models.RoleAdmin )
	second , _ := models.CreateUser( fixture.store , "Sam" , models.RoleAdmin )
	admin := fixture.signIn( t , second.ID )

	// One field: field names it.
	status , reply := send( t , owner , "/api/account/rename" , fiber.Map{ "display_name": "" } )
	wantCode( t , "an empty name" , status , reply , apidoc.CodeInvalid )
	if reply[ "field" ] != "display_name" { t.Errorf( "field: %v" , reply ) }
	status , reply = send( t , owner , "/api/account/rename" , fiber.Map{ "display_name": "X" , "not_a_field": 1 } )
	wantCode( t , "an unknown field" , status , reply , apidoc.CodeInvalid )
	if reply[ "field" ] != "not_a_field" { t.Errorf( "field: %v" , reply ) }

	// The ladder.
	status , reply = send( t , admin , "/api/admin/users/" + idText( fixture.owner.ID ) + "/disabled" , fiber.Map{ "disabled": true } )
	wantCode( t , "disabling the owner" , status , reply , apidoc.CodeOwnerProtected )
	status , reply = send( t , admin , "/api/admin/users/" + idText( fixture.owner.ID ) + "/role" , fiber.Map{ "role": "user" } )
	wantCode( t , "demoting the owner" , status , reply , apidoc.CodeOwnerProtected )
	status , reply = send( t , admin , "/api/admin/users/" + idText( peer.ID ) + "/role" , fiber.Map{ "role": "user" } )
	wantCode( t , "demoting another admin" , status , reply , apidoc.CodeOwnerRequired )
	status , reply = send( t , admin , "/api/admin/users/" + idText( peer.ID ) + "/delete" , fiber.Map{} )
	wantCode( t , "deleting another admin" , status , reply , apidoc.CodeOwnerRequired )
	status , reply = send( t , admin , "/api/admin/users/" + idText( peer.ID ) + "/reissue-login" , fiber.Map{} )
	wantCode( t , "a link for another admin" , status , reply , apidoc.CodeOwnerRequired )

	// A bad csrf token is forbidden, not any of the named reasons.
	status , reply = owner.post( t , "/api/account/rename" , fiber.Map{ "display_name": "X" , "csrf_token": "nope" } )
	wantCode( t , "a bad csrf token" , status , reply , apidoc.CodeForbidden )

	// A key on a browser-only route; a key that may not mint.
	key , _ , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "script" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	status , reply = fixture.withKey( t , key , http.MethodPost , "/api/keys" , fiber.Map{ "name": "x" , "role": "user" } )
	wantCode( t , "a key making a key" , status , reply , apidoc.CodeSessionOnly )
	status , reply = fixture.withKey( t , key , http.MethodGet , "/api/admin/users/" + idText( fixture.alice.ID ) + "/keys" , nil )
	wantCode( t , "a key that may not mint" , status , reply , apidoc.CodeKeyMintingOff )
	if reply[ "error" ] != "key_minting_off" { t.Errorf( "error changed: %v" , reply ) }
	status , reply = fixture.withKey( t , "nope.nope" , http.MethodGet , "/api/me" , nil )
	wantCode( t , "a bad key" , status , reply , apidoc.CodeNotAuthenticated )

	// The error handler's answers: a 429, a 405.
	app := fiber.New( fiber.Config{ ErrorHandler: middleware.NewErrorHandler( nil ) } )
	app.Get( "/busy" , func( c fiber.Ctx ) ( err error ) { err = fiber.ErrTooManyRequests; return } )
	app.Get( "/wrong" , func( c fiber.Ctx ) ( err error ) { err = fiber.ErrMethodNotAllowed; return } )
	for path , want := range map[ string ]string{ "/busy": apidoc.CodeRateLimited , "/wrong": apidoc.CodeInvalid } {
		client := &browser{ fixture: &authFixture{ app: app } }
		status , reply = client.do( t , http.MethodGet , path , nil )
		wantCode( t , path , status , reply , want )
	}
}
