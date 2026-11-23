package routes

import (
	http "net/http"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	models "vocabtrainer/server/models"
)

// A key rotates itself (portal contract v3, item 5): a new secret, the same
// key -- id, grants, lineage, expiry -- so what it made keeps working, and a
// leaked key that rotates still dies on schedule.

func TestAKeyRotatesItself( t *testing.T ) {
	fixture := newAuthFixture( t )
	portal_user , old := portalAccount( t , fixture )
	status , me := fixture.withKey( t , old , http.MethodGet , "/api/me" , nil )
	if status != fiber.StatusOK { t.Fatalf( "%d %v" , status , me ) }
	before := me[ "key" ].( map[ string ]any )
	listed := false
	for _ , name := range me[ "capabilities" ].( []any ) { listed = listed || name == "key_rotation" }
	if listed == false { t.Fatalf( "key_rotation missing: %v" , me[ "capabilities" ] ) }

	// A key it made, before the rotation.
	status , minted := fixture.withKey( t , old , http.MethodPost , "/api/admin/users/" + idText( fixture.alice.ID ) + "/keys" , fiber.Map{ "name": "for alice" , "role": "user" , "expires_in_days": 30 } )
	if status != fiber.StatusCreated { t.Fatalf( "%d %v" , status , minted ) }

	status , reply := fixture.withKey( t , old , http.MethodPost , "/api/keys/self/rotate" , fiber.Map{} )
	fresh , _ := reply[ "credential" ].( string )
	if status != fiber.StatusOK || fresh == "" || fresh == old || len( reply ) != 1 { t.Fatalf( "%d %v" , status , reply ) }
	if status , _ = fixture.withKey( t , old , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "the old secret still works: %d" , status ) }
	status , me = fixture.withKey( t , fresh , http.MethodGet , "/api/me" , nil )
	if status != fiber.StatusOK || me[ "id" ] != float64( portal_user.ID ) { t.Fatalf( "%d %v" , status , me ) }
	after := me[ "key" ].( map[ string ]any )
	for _ , name := range []string{ "id" , "name" , "role" , "may_mint_keys" , "may_sign_in" , "expires_at" } {
		if after[ name ] != before[ name ] { t.Fatalf( "%s changed: %v -> %v" , name , before[ name ] , after[ name ] ) }
	}
	if status , _ = fixture.withKey( t , minted[ "credential" ].( string ) , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK {
		t.Fatalf( "what it made stopped working: %d" , status )
	}
	// A session has no key to rotate.
	owner := fixture.signIn( t , fixture.owner.ID )
	if status , reply = send( t , owner , "/api/keys/self/rotate" , fiber.Map{} ); status != fiber.StatusForbidden { t.Fatalf( "a session: %d %v" , status , reply ) }
}

func TestAViewerKeyRotatesToo( t *testing.T ) {
	fixture := newAuthFixture( t )
	credential , _ , err := models.IssueAPIKey( fixture.store , fixture.alice.ID , "reader" , models.RoleViewer , time.Hour )
	if err != nil { t.Fatal( err ) }
	status , reply := fixture.withKey( t , credential , http.MethodPost , "/api/keys/self/rotate" , fiber.Map{} )
	if status != fiber.StatusOK || reply[ "credential" ] == nil { t.Fatalf( "%d %v" , status , reply ) }
}

func TestTheOwnerRotatesAnOwnKeyFromASession( t *testing.T ) {
	fixture := newAuthFixture( t )
	old , key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "cron" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	owner := fixture.signIn( t , fixture.owner.ID )
	status , reply := send( t , owner , "/api/keys/" + key.ID + "/rotate" , fiber.Map{} )
	if status != fiber.StatusOK || reply[ "credential" ] == nil { t.Fatalf( "%d %v" , status , reply ) }
	if status , _ = fixture.withKey( t , old , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "old secret: %d" , status ) }
	if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.ExpiresAt.Equal( *key.ExpiresAt ) == false { t.Fatalf( "the expiry moved: %v" , stored.ExpiresAt ) }

	// Someone else's key is no such key; another admin's own is the owner's
	// say-so; a key cannot use the session form.
	_ , alices , _ := models.IssueAPIKey( fixture.store , fixture.alice.ID , "hers" , models.RoleUser , time.Hour )
	if status , reply = send( t , owner , "/api/keys/" + alices.ID + "/rotate" , fiber.Map{} ); status != fiber.StatusNotFound { t.Fatalf( "someone else's: %d %v" , status , reply ) }
	second , _ := models.CreateUser( fixture.store , "Sam" , models.RoleAdmin )
	_ , sams , _ := models.IssueAPIKey( fixture.store , second.ID , "his" , models.RoleAdmin , time.Hour )
	admin := fixture.signIn( t , second.ID )
	if status , reply = send( t , admin , "/api/keys/" + sams.ID + "/rotate" , fiber.Map{} ); status != fiber.StatusForbidden || reply[ "code" ] != apidoc.CodeOwnerRequired {
		t.Fatalf( "a second admin: %d %v" , status , reply )
	}
}
