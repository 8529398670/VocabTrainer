package routes

import (
	testing "testing"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// Bulk routes (bulk=True, see server/apidoc/bulk.go): each item runs through
// the route's own handler, so its rules and words are the route's, item by
// item. python-webserver's test_bulk.py checks the same things.

func bulkResults( t *testing.T , reply map[ string ]any ) ( results []map[ string ]any ) {
	t.Helper()
	raw , ok := reply[ "results" ].( []any )
	if ok == false { t.Fatalf( "no results in %v" , reply ) }
	for _ , entry := range raw { results = append( results , entry.( map[ string ]any ) ) }
	return
}

func isDisabled( t *testing.T , fixture *authFixture , user_id uint64 ) ( disabled bool ) {
	t.Helper()
	user , err := models.GetUser( fixture.store , user_id )
	if err != nil { t.Fatal( err ) }
	disabled = user.Disabled()
	return
}

func TestABulkRouteRunsEachItem( t *testing.T ) {
	fixture := newAuthFixture( t )
	bob , err := models.CreateUser( fixture.store , "Bob" , models.RoleUser )
	if err != nil { t.Fatal( err ) }
	owner := fixture.signIn( t , fixture.owner.ID )
	path := "/api/bulk/admin/users/disabled"

	// shared gives every item disabled; one item's own value wins; a missing
	// account and your own fail with the single route's words, the rest go on.
	status , reply := send( t , owner , path , fiber.Map{
		"items": []any{
			fiber.Map{ "user_id": fixture.alice.ID },
			fiber.Map{ "user_id": 9999 },
			fiber.Map{ "user_id": fixture.owner.ID },
			fiber.Map{ "user_id": bob.ID , "disabled": false },
		},
		"shared": fiber.Map{ "disabled": true },
	} )
	if status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }
	if reply[ "done" ] != float64( 2 ) || reply[ "failed" ] != float64( 2 ) { t.Fatalf( "%v" , reply ) }
	results := bulkResults( t , reply )
	if results[ 0 ][ "ok" ] != true || results[ 0 ][ "reply" ].( map[ string ]any )[ "ok" ] != true { t.Errorf( "alice: %v" , results[ 0 ] ) }
	if results[ 1 ][ "status" ] != float64( 404 ) || results[ 1 ][ "error" ] != "no such user" { t.Errorf( "missing: %v" , results[ 1 ] ) }
	if results[ 2 ][ "status" ] != float64( 400 ) || results[ 2 ][ "error" ] != "you cannot disable your own account" { t.Errorf( "self: %v" , results[ 2 ] ) }
	if results[ 3 ][ "ok" ] != true || results[ 3 ][ "index" ] != float64( 3 ) { t.Errorf( "bob: %v" , results[ 3 ] ) }
	if isDisabled( t , fixture , fixture.alice.ID ) == false { t.Error( "alice is still enabled" ) }
	if isDisabled( t , fixture , bob.ID ) { t.Error( "bob's own disabled: false lost to shared" ) }

	// An item the route would refuse as it stands fails alone, with its words.
	status , reply = send( t , owner , path , fiber.Map{ "items": []any{ fiber.Map{ "user_id": fixture.alice.ID } , fiber.Map{ "user_id": bob.ID , "disabled": "yes" } } , "shared": fiber.Map{ "disabled": false } } )
	results = bulkResults( t , reply )
	if status != fiber.StatusOK || results[ 0 ][ "ok" ] != true || results[ 1 ][ "status" ] != float64( 400 ) { t.Fatalf( "%d %v" , status , reply ) }
}

func TestABulkRequestIsCheckedAsAWhole( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	alice := fixture.signIn( t , fixture.alice.ID )
	path := "/api/bulk/admin/users/disabled"
	too_many := []any{}
	for index := 0 ; index <= 100 ; index++ { too_many = append( too_many , fiber.Map{ "user_id": fixture.alice.ID } ) }
	for _ , entry := range []struct {
		body fiber.Map
		want string
	}{
		{ fiber.Map{} , "items is required" },
		{ fiber.Map{ "items": []any{} } , "items must hold 1 to 100 entries" },
		{ fiber.Map{ "items": too_many } , "items must hold 1 to 100 entries" },
		{ fiber.Map{ "items": []any{ 3 } } , "malformed request body" },
		{ fiber.Map{ "items": []any{ fiber.Map{ "user_id": 3 } } , "extra": 1 } , "unknown field: extra" },
		{ fiber.Map{ "items": []any{ fiber.Map{ "user_id": 3 } } , "shared": fiber.Map{ "user_id": 3 } } , "unknown field: shared.user_id" },
	} {
		status , reply := send( t , owner , path , entry.body )
		if status != fiber.StatusBadRequest || reply[ "error" ] != entry.want { t.Errorf( "%v: %d %v, want %q" , entry.body , status , reply , entry.want ) }
	}
	// The route's guards hold: no csrf token, or not an admin.
	if status , _ := owner.post( t , path , fiber.Map{ "items": []any{ fiber.Map{ "user_id": fixture.alice.ID } } } ); status != fiber.StatusForbidden { t.Errorf( "no csrf: %d" , status ) }
	if status , _ := send( t , alice , path , fiber.Map{ "items": []any{ fiber.Map{ "user_id": fixture.owner.ID } } } ); status != fiber.StatusForbidden { t.Errorf( "a user: %d" , status ) }
}
