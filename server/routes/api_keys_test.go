package routes

import (
	encoding_json "encoding/json"
	io "io"
	http "net/http"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// mintFor is POST /api/admin/keys from a browser.
func mintFor( t *testing.T , client *browser , user_id uint64 , role string ) ( status int , reply map[ string ]any ) {
	t.Helper()
	status , reply = send( t , client , "/api/admin/keys" , fiber.Map{ "user_id": user_id , "name": "deploy" , "role": role } )
	return
}

func TestWhoMayMintAKeyForWhom( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	third , _ := models.CreateUser( fixture.store , "Third admin" , models.RoleAdmin )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )
	alice := fixture.signIn( t , fixture.alice.ID )

	cases := []struct {
		name   string
		client *browser
		target uint64
		role   string
		status int
	}{
		{ "an admin for a user" , admin , fixture.alice.ID , "user" , fiber.StatusCreated },
		{ "the owner for a user" , owner , fixture.alice.ID , "user" , fiber.StatusCreated },
		{ "the owner for an admin" , owner , second.ID , "admin" , fiber.StatusCreated },
		{ "an admin for themselves" , admin , second.ID , "admin" , fiber.StatusCreated },
		{ "an admin for another admin" , admin , third.ID , "user" , fiber.StatusForbidden },
		{ "an admin for the owner" , admin , fixture.owner.ID , "user" , fiber.StatusForbidden },
		{ "an admin role for a user" , owner , fixture.alice.ID , "admin" , fiber.StatusForbidden },
		{ "a user for anyone" , alice , second.ID , "user" , fiber.StatusForbidden },
		{ "nobody there" , admin , 99999 , "user" , fiber.StatusNotFound },
	}
	for _ , item := range cases {
		status , reply := mintFor( t , item.client , item.target , item.role )
		if status != item.status {
			t.Fatalf( "%s: status %d, wanted %d: %v" , item.name , status , item.status , reply )
		}
	}
}

// S3: revoking, deleting and restoring someone else's key follow the owner
// rule for the key's account. Before, any admin could kill another admin's
// keys, or bring back a key the owner had deleted.
func TestOnlyTheOwnerTouchesAnAdminsKeys( t *testing.T ) {
	for _ , verb := range []string{ "revoke" , "delete" } {
		fixture := newAuthFixture( t )
		second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
		third , _ := models.CreateUser( fixture.store , "Third admin" , models.RoleAdmin )
		owner := fixture.signIn( t , fixture.owner.ID )
		admin := fixture.signIn( t , second.ID )
		for _ , holder := range []uint64{ fixture.owner.ID , third.ID } {
			_ , key , err := models.IssueAPIKey( fixture.store , holder , "ci" , models.RoleUser , time.Hour )
			if err != nil { t.Fatal( err ) }
			if status , reply := send( t , admin , "/api/admin/keys/"+key.ID+"/"+verb , fiber.Map{} ); status != fiber.StatusForbidden {
				t.Fatalf( "%s #%d's key by a peer: %d %v" , verb , holder , status , reply )
			}
			if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.Live() == false { t.Fatalf( "%s: a peer's refusal still changed the key" , verb ) }
		}
		_ , key , _ := models.IssueAPIKey( fixture.store , fixture.alice.ID , "ci" , models.RoleUser , time.Hour )
		if status , _ := send( t , admin , "/api/admin/keys/"+key.ID+"/"+verb , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "%s a user's key: %d" , verb , status ) }
		_ , key , _ = models.IssueAPIKey( fixture.store , third.ID , "ci" , models.RoleUser , time.Hour )
		if status , _ := send( t , owner , "/api/admin/keys/"+key.ID+"/"+verb , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "the owner %s an admin's key: %d" , verb , status ) }
	}
}

func TestRestoringAKeyFollowsItsAccount( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )
	_ , key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "ci" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , _ := send( t , owner , "/api/keys/"+key.ID+"/delete" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "delete: %d" , status ) }
	if status , reply := send( t , admin , "/api/admin/keys/"+key.ID+"/restore" , fiber.Map{} ); status != fiber.StatusForbidden {
		t.Fatalf( "a peer brought back the owner's key: %d %v" , status , reply )
	}
	if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.Deleted() == false { t.Fatal( "the key came back" ) }
	if status , _ := send( t , owner , "/api/keys/"+key.ID+"/restore" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "the owner's own restore: %d" , status ) }
}

// S14: an admin who is not the owner renames neither the owner's key nor
// another admin's. A non-owner admin renamed the owner's prod-deploy key to
// unused-delete-me, which could lead the owner to revoke the wrong one.
func TestOnlyTheOwnerRenamesAnAdminsKey( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	third , _ := models.CreateUser( fixture.store , "Third admin" , models.RoleAdmin )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )
	for _ , holder := range []uint64{ fixture.owner.ID , third.ID } {
		_ , key , err := models.IssueAPIKey( fixture.store , holder , "prod-deploy" , models.RoleUser , time.Hour )
		if err != nil { t.Fatal( err ) }
		if status , reply := send( t , admin , "/api/admin/keys/"+key.ID+"/rename" , fiber.Map{ "name": "unused-delete-me" } ); status != fiber.StatusForbidden {
			t.Fatalf( "a peer renamed #%d's key: %d %v" , holder , status , reply )
		}
		if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.Name != "prod-deploy" { t.Fatalf( "renamed anyway: %q" , stored.Name ) }
		if status , _ := send( t , owner , "/api/admin/keys/"+key.ID+"/rename" , fiber.Map{ "name": "kept" } ); status != fiber.StatusOK { t.Fatalf( "the owner: %d" , status ) }
	}
	_ , key , _ := models.IssueAPIKey( fixture.store , fixture.alice.ID , "ci" , models.RoleUser , time.Hour )
	if status , _ := send( t , admin , "/api/admin/keys/"+key.ID+"/rename" , fiber.Map{ "name": "ci-2" } ); status != fiber.StatusOK { t.Fatalf( "a user's key: %d" , status ) }
}

// S14: what the owner deleted, only the owner puts back. The owner deleted
// admin Bob's key, Bob restored it, and it was live again.
func TestWhatTheOwnerDeletedOnlyTheOwnerRestores( t *testing.T ) {
	fixture := newAuthFixture( t )
	bob_user , _ := models.CreateUser( fixture.store , "Bob" , models.RoleAdmin )
	owner := fixture.signIn( t , fixture.owner.ID )
	bob := fixture.signIn( t , bob_user.ID )
	_ , key , err := models.IssueAPIKey( fixture.store , bob_user.ID , "bob's" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , _ := send( t , owner , "/api/admin/keys/"+key.ID+"/delete" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "delete: %d" , status ) }
	for _ , path := range []string{ "/api/keys/"+key.ID+"/restore" , "/api/admin/keys/"+key.ID+"/restore" } {
		if status , reply := send( t , bob , path , fiber.Map{} ); status != fiber.StatusForbidden { t.Fatalf( "%s by Bob: %d %v" , path , status , reply ) }
	}
	if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.Deleted() == false { t.Fatal( "the key came back" ) }
	if status , _ := send( t , owner , "/api/admin/keys/"+key.ID+"/restore" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "the owner's restore: %d" , status ) }

	// What Bob deleted himself, Bob may still put back.
	if status , _ := send( t , bob , "/api/keys/"+key.ID+"/delete" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "Bob's delete: %d" , status ) }
	if status , _ := send( t , bob , "/api/keys/"+key.ID+"/restore" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "Bob's own restore: %d" , status ) }
}

// S7: a request carrying both a signed-in cookie and a key acts as the key.
// It used to act as the cookie, so a script using a narrow key from a
// signed-in browser quietly ran with the full account.
func TestABearerHeaderWinsOverTheCookie( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	narrow , _ , err := models.IssueAPIKey( fixture.store , fixture.alice.ID , "narrow" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	who := func( key string ) ( status int , reply map[ string ]any ) {
		request , _ := http.NewRequest( http.MethodGet , "/api/me" , nil )
		request.Header.Set( "Cookie" , owner.cookie )
		request.Header.Set( "Authorization" , "Bearer "+key )
		response , test_err := fixture.app.Test( request )
		if test_err != nil { t.Fatal( test_err ) }
		raw , _ := io.ReadAll( response.Body )
		status = response.StatusCode
		encoding_json.Unmarshal( raw , &reply )
		return
	}
	if status , reply := who( narrow ); status != fiber.StatusOK || reply[ "id" ] != float64( fixture.alice.ID ) {
		t.Fatalf( "the key: %d %v" , status , reply )
	}
	// A bad key is no one, not the signed-in account.
	if status , reply := who( "nope.nope" ); status != fiber.StatusUnauthorized { t.Fatalf( "a bad key: %d %v" , status , reply ) }

	// S16: any Authorization header is an attempt at a key. "Bearer<TAB>key"
	// used to fall back to the cookie and run as the signed-in owner.
	header := func( value string ) ( status int , reply map[ string ]any ) {
		request , _ := http.NewRequest( http.MethodGet , "/api/me" , nil )
		request.Header.Set( "Cookie" , owner.cookie )
		request.Header.Set( "Authorization" , value )
		response , test_err := fixture.app.Test( request )
		if test_err != nil { t.Fatal( test_err ) }
		raw , _ := io.ReadAll( response.Body )
		status = response.StatusCode
		encoding_json.Unmarshal( raw , &reply )
		return
	}
	if status , reply := header( "Bearer\t" + narrow ); status != fiber.StatusOK || reply[ "id" ] != float64( fixture.alice.ID ) {
		t.Errorf( "a tab after Bearer: %d %v" , status , reply )
	}
	for _ , value := range []string{ "Bearer\tnope.nope" , "Basic abc" , "Bearer" , "nonsense" } {
		if status , reply := header( value ); status != fiber.StatusUnauthorized { t.Errorf( "%q: %d %v" , value , status , reply ) }
	}
}

func TestAKeyMadeForSomeoneActsAsThemAndSaysWhoMadeIt( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	status , reply := mintFor( t , owner , fixture.alice.ID , "user" )
	if status != fiber.StatusCreated { t.Fatalf( "%d %v" , status , reply ) }
	if reply[ "created_by" ] != float64( fixture.owner.ID ) { t.Fatalf( "%v" , reply ) }

	request , _ := http.NewRequest( http.MethodGet , "/api/me" , nil )
	request.Header.Set( "Authorization" , "Bearer " + reply[ "credential" ].( string ) )
	response , err := fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
	if err != nil { t.Fatal( err ) }
	raw , _ := io.ReadAll( response.Body )
	me := map[ string ]any{}
	encoding_json.Unmarshal( raw , &me )
	if me[ "id" ] != float64( fixture.alice.ID ) { t.Fatalf( "%s" , raw ) }

	alice := fixture.signIn( t , fixture.alice.ID )
	status , page := alice.do( t , http.MethodGet , "/api/keys" , nil )
	if status != fiber.StatusOK { t.Fatalf( "%d %v" , status , page ) }
	rows := page[ "keys" ].( []any )
	if len( rows ) != 1 { t.Fatalf( "%v" , rows ) }
	row := rows[ 0 ].( map[ string ]any )
	if row[ "created_by_name" ] != "Owner" { t.Fatalf( "%v" , row ) }
	if strings.Contains( string( raw ) , "secret_hash" ) { t.Fatalf( "%s" , raw ) }
}

func TestAKeyCannotMintAKeyForAnyone( t *testing.T ) {
	fixture := newAuthFixture( t )
	key , _ , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "script" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	request , _ := http.NewRequest( http.MethodPost , "/api/admin/keys" , strings.NewReader( `{"user_id":2,"name":"x"}` ) )
	request.Header.Set( "Content-Type" , "application/json" )
	request.Header.Set( "Authorization" , "Bearer " + key )
	response , err := fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
	if err != nil { t.Fatal( err ) }
	if response.StatusCode != fiber.StatusForbidden {
		t.Fatalf( "status %d" , response.StatusCode )
	}
	keys , _ := models.ListAPIKeysForUser( fixture.store , fixture.alice.ID )
	if len( keys ) != 0 { t.Fatalf( "%v" , keys ) }
}

func TestAShellStillMintsForTheOwner( t *testing.T ) {
	fixture := newAuthFixture( t )
	_ , key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "script" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	if key.CreatedBy != 0 { t.Fatalf( "%d" , key.CreatedBy ) }
}

// issued is a key for user_id, straight from the model.
func issued( t *testing.T , fixture *authFixture , user_id uint64 , ttl time.Duration ) ( key *models.APIKey ) {
	t.Helper()
	_ , key , err := models.IssueAPIKey( fixture.store , user_id , "deploy" , models.RoleUser , ttl )
	if err != nil { t.Fatal( err ) }
	return
}

func TestRenamingAKey( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	alice := fixture.signIn( t , fixture.alice.ID )
	admin := fixture.signIn( t , second.ID )
	key := issued( t , fixture , fixture.alice.ID , time.Hour )
	path := "/api/keys/" + key.ID + "/rename"

	status , reply := send( t , alice , path , fiber.Map{ "name": "  bot  " } )
	if status != fiber.StatusOK || reply[ "name" ] != "bot" { t.Fatalf( "%d %v" , status , reply ) }

	// Someone else's key is not there, even for an admin on the own-keys route.
	if status , _ = send( t , admin , path , fiber.Map{ "name": "x" } ); status != fiber.StatusNotFound { t.Fatalf( "%d" , status ) }
	if status , _ = send( t , alice , path , fiber.Map{ "name": "   " } ); status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }

	// An undo that finds a different name leaves it alone, and says what is there.
	status , reply = send( t , alice , path , fiber.Map{ "name": "old" , "expect": "stale" } )
	if status != fiber.StatusConflict || reply[ "name" ] != "bot" { t.Fatalf( "%d %v" , status , reply ) }

	status , _ = send( t , admin , "/api/admin/keys/" + key.ID + "/rename" , fiber.Map{ "name": "by admin" , "expect": "bot" } )
	if status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.Name != "by admin" { t.Fatalf( "%v" , stored.Name ) }

	// A dead key keeps its history readable, so it can be tidied; a deleted one is gone.
	models.RevokeAPIKey( fixture.store , key.ID )
	if status , _ = send( t , alice , path , fiber.Map{ "name": "retired" } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	models.DeleteAPIKey( fixture.store , key.ID , fixture.alice.ID )
	if status , _ = send( t , alice , path , fiber.Map{ "name": "gone" } ); status != fiber.StatusNotFound { t.Fatalf( "%d" , status ) }
}

func TestWhoMayChangeAKeysExpiry( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	third , _ := models.CreateUser( fixture.store , "Third admin" , models.RoleAdmin )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )
	alice := fixture.signIn( t , fixture.alice.ID )

	cases := []struct {
		name   string
		client *browser
		holder uint64
		prefix string
		status int
	}{
		{ "your own" , alice , fixture.alice.ID , "/api/keys/" , fiber.StatusOK },
		{ "an admin's own, as an admin" , admin , second.ID , "/api/admin/keys/" , fiber.StatusOK },
		{ "an admin for a user" , admin , fixture.alice.ID , "/api/admin/keys/" , fiber.StatusOK },
		{ "the owner for an admin" , owner , second.ID , "/api/admin/keys/" , fiber.StatusOK },
		{ "an admin for another admin" , admin , third.ID , "/api/admin/keys/" , fiber.StatusForbidden },
		{ "an admin for the owner" , admin , fixture.owner.ID , "/api/admin/keys/" , fiber.StatusForbidden },
		{ "a user on the admin route" , alice , second.ID , "/api/admin/keys/" , fiber.StatusForbidden },
		{ "someone else's on the own route" , alice , second.ID , "/api/keys/" , fiber.StatusNotFound },
	}
	for _ , item := range cases {
		key := issued( t , fixture , item.holder , time.Hour )
		status , reply := send( t , item.client , item.prefix + key.ID + "/expiry" , fiber.Map{ "expires_in_days": 30 } )
		if status != item.status { t.Fatalf( "%s: %d %v" , item.name , status , reply ) }
		stored , _ := models.GetAPIKey( fixture.store , key.ID )
		changed := stored.ExpiresAt.After( time.Now().Add( 24 * time.Hour ) )
		if changed != ( item.status == fiber.StatusOK ) { t.Fatalf( "%s: expires %v" , item.name , stored.ExpiresAt ) }
	}
}

func TestChangingAKeysExpiry( t *testing.T ) {
	fixture := newAuthFixture( t )
	alice := fixture.signIn( t , fixture.alice.ID )
	key := issued( t , fixture , fixture.alice.ID , time.Hour )
	path := "/api/keys/" + key.ID + "/expiry"

	for _ , bad := range []fiber.Map{ {} , { "expires_in_days": -1 } , { "expires_in_days": 3651 } } {
		if status , _ := send( t , alice , path , bad ); status != fiber.StatusBadRequest { t.Fatalf( "%v: %d" , bad , status ) }
	}

	status , reply := send( t , alice , path , fiber.Map{ "expires_in_days": 0 } )
	if status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }
	if _ , set := reply[ "expires_at" ]; set { t.Fatalf( "%v" , reply ) }

	// Revoking is final, so an edit cannot be the way back.
	models.RevokeAPIKey( fixture.store , key.ID )
	if status , _ = send( t , alice , path , fiber.Map{ "expires_in_days": 30 } ); status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }

	// Nor round an expiry.
	expired := issued( t , fixture , fixture.alice.ID , time.Nanosecond )
	time.Sleep( time.Millisecond )
	status , _ = send( t , alice , "/api/keys/" + expired.ID + "/expiry" , fiber.Map{ "expires_in_days": 30 } )
	if status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }
}

func TestAKeyCannotExtendItself( t *testing.T ) {
	fixture := newAuthFixture( t )
	credential , key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "script" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	for _ , path := range []string{ "/api/keys/" , "/api/admin/keys/" } {
		request , _ := http.NewRequest( http.MethodPost , path + key.ID + "/expiry" , strings.NewReader( `{"expires_in_days":0}` ) )
		request.Header.Set( "Content-Type" , "application/json" )
		request.Header.Set( "Authorization" , "Bearer " + credential )
		response , test_err := fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
		if test_err != nil { t.Fatal( test_err ) }
		if response.StatusCode != fiber.StatusForbidden { t.Fatalf( "%s: %d" , path , response.StatusCode ) }
	}
	if stored , _ := models.GetAPIKey( fixture.store , key.ID ); stored.ExpiresAt == nil { t.Fatal( "it extended itself" ) }
}
