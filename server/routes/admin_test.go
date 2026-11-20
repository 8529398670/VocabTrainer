package routes

import (
	fmt "fmt"
	http "net/http"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// Admins editing other accounts: who may touch whom, and what a change of role
// does to what the account already holds.

func userPath( user *models.User , verb string ) ( path string ) {
	path = fmt.Sprintf( "/api/admin/users/%d/%s" , user.ID , verb )
	return
}

func secondAdmin( t *testing.T , fixture *authFixture ) ( admin *models.User ) {
	t.Helper()
	admin , err := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	if err != nil { t.Fatal( err ) }
	return
}

func TestRenamingSomeoneElse( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	alice := fixture.signIn( t , fixture.alice.ID )
	path := userPath( fixture.alice , "rename" )

	status , reply := send( t , owner , path , fiber.Map{ "display_name": "  Alice B  " } )
	if status != fiber.StatusOK || reply[ "display_name" ] != "Alice B" { t.Fatalf( "%d %v" , status , reply ) }
	if status , _ = send( t , owner , path , fiber.Map{ "display_name": "  " } ); status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }
	if status , _ = send( t , alice , path , fiber.Map{ "display_name": "x" } ); status != fiber.StatusForbidden { t.Fatalf( "%d" , status ) }

	// An undo that finds a different name leaves it alone, and says what is there.
	status , reply = send( t , owner , path , fiber.Map{ "display_name": "old" , "expect": "stale" } )
	if status != fiber.StatusConflict || reply[ "display_name" ] != "Alice B" { t.Fatalf( "%d %v" , status , reply ) }

	if status , _ = send( t , owner , "/api/admin/users/9999/rename" , fiber.Map{ "display_name": "x" } ); status != fiber.StatusNotFound { t.Fatalf( "%d" , status ) }
}

func TestOnlyTheOwnerRenamesAnAdmin( t *testing.T ) {
	fixture := newAuthFixture( t )
	second := secondAdmin( t , fixture )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )

	if status , _ := send( t , admin , userPath( fixture.alice , "rename" ) , fiber.Map{ "display_name": "fine" } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	if status , _ := send( t , admin , userPath( fixture.owner , "rename" ) , fiber.Map{ "display_name": "x" } ); status != fiber.StatusForbidden { t.Fatalf( "%d" , status ) }
	if status , _ := send( t , admin , userPath( second , "rename" ) , fiber.Map{ "display_name": "x" } ); status != fiber.StatusForbidden { t.Fatalf( "%d" , status ) }
	if status , _ := send( t , owner , userPath( second , "rename" ) , fiber.Map{ "display_name": "Sam" } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
}

func TestBecomingAnAdminEndsWhatWasOpen( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	alice := fixture.signIn( t , fixture.alice.ID )
	credential , err := models.IssueLoginToken( fixture.store , fixture.alice.ID , time.Hour )
	if err != nil { t.Fatal( err ) }
	path := userPath( fixture.alice , "role" )

	if status , _ := send( t , owner , path , fiber.Map{ "role": "owner" } ); status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }
	if status , _ := send( t , alice , path , fiber.Map{ "role": "admin" } ); status != fiber.StatusForbidden { t.Fatalf( "%d" , status ) }
	if status , reply := send( t , owner , path , fiber.Map{ "role": "admin" } ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }

	user , _ := models.GetUser( fixture.store , fixture.alice.ID )
	if user.IsAdmin() == false { t.Fatalf( "%+v" , user ) }
	if status , _ := alice.do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "%d" , status ) }
	if _ , redeem_err := models.RedeemLoginToken( fixture.store , credential , models.SignInRules{ SessionTTL: time.Hour } ); redeem_err == nil {
		t.Fatal( "a link from before the promotion still works" )
	}
	if status , reply := fixture.signIn( t , fixture.alice.ID ).do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK || reply[ "role" ] != "admin" {
		t.Fatalf( "%d %v" , status , reply )
	}
}

func TestAskingForTheRoleTheyHaveChangesNothing( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	alice := fixture.signIn( t , fixture.alice.ID )
	if status , _ := send( t , owner , userPath( fixture.alice , "role" ) , fiber.Map{ "role": "user" } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	if status , _ := alice.do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
}

func TestLosingAdminRightsAppliesAtOnce( t *testing.T ) {
	fixture := newAuthFixture( t )
	second := secondAdmin( t , fixture )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )

	if status , _ := send( t , owner , userPath( second , "role" ) , fiber.Map{ "role": "user" } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }

	// Still signed in, but as a user, and any link they made has stopped.
	if status , reply := admin.do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK || reply[ "role" ] != "user" { t.Fatalf( "%d %v" , status , reply ) }
	if status , _ := admin.do( t , http.MethodGet , "/api/admin/users" , nil ); status != fiber.StatusForbidden { t.Fatalf( "%d" , status ) }

	// Made an admin again, the link is back, as the screen's question says.
	if status , _ := send( t , owner , userPath( second , "role" ) , fiber.Map{ "role": "admin" } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
}

func TestWhoMayChangeWhoseRole( t *testing.T ) {
	fixture := newAuthFixture( t )
	second := secondAdmin( t , fixture )
	third := secondAdmin( t , fixture )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )

	cases := []struct {
		client *browser
		target *models.User
		role   string
		want   int
	}{
		{ owner , fixture.owner , "user" , fiber.StatusBadRequest }, // yourself
		{ admin , second , "user" , fiber.StatusBadRequest },        // yourself
		{ admin , fixture.owner , "user" , fiber.StatusForbidden },  // the owner
		{ admin , third , "user" , fiber.StatusForbidden },          // another admin
		{ admin , fixture.alice , "admin" , fiber.StatusOK },        // anyone may make one
	}
	for index , item := range cases {
		if status , reply := send( t , item.client , userPath( item.target , "role" ) , fiber.Map{ "role": item.role } ); status != item.want {
			t.Fatalf( "case %d: %d %v" , index , status , reply )
		}
	}
	if status , _ := send( t , owner , "/api/admin/users/9999/role" , fiber.Map{ "role": "admin" } ); status != fiber.StatusNotFound { t.Fatalf( "%d" , status ) }
}

// S1: a login link is a way in, so reissuing or cancelling one follows the
// owner rule. Before this, any admin could mint the owner a link and sign in
// as the owner.
func TestOnlyTheOwnerHandsOutAnAdminsLoginLink( t *testing.T ) {
	fixture := linkFixture( t )
	second := secondAdmin( t , fixture )
	third := secondAdmin( t , fixture )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )

	for _ , verb := range []string{ "reissue-login" , "cancel-login-link" } {
		for _ , target := range []*models.User{ fixture.owner , third } {
			if status , reply := send( t , admin , userPath( target , verb ) , fiber.Map{} ); status != fiber.StatusForbidden || reply[ "login_path" ] != nil {
				t.Fatalf( "%s for #%d by a non-owner admin: %d %v" , verb , target.ID , status , reply )
			}
		}
		if status , reply := send( t , admin , userPath( fixture.alice , verb ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "%s for a user: %d %v" , verb , status , reply ) }
	}
	// The owner still reissues an admin's link, and it signs in.
	status , reply := send( t , owner , userPath( third , "reissue-login" ) , fiber.Map{} )
	if status != fiber.StatusOK || signInWith( t , fixture , credentialOf( reply ) ) != fiber.StatusOK { t.Fatalf( "owner reissuing for an admin: %d %v" , status , reply ) }
	if status , _ := send( t , owner , userPath( third , "cancel-login-link" ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "owner cancelling: %d" , status ) }
}

// S3: disabling and revoking another admin follow the owner rule too, so a
// non-owner admin cannot lock a peer out.
func TestOnlyTheOwnerDisablesOrRevokesAnAdmin( t *testing.T ) {
	fixture := newAuthFixture( t )
	second := secondAdmin( t , fixture )
	third := secondAdmin( t , fixture )
	owner := fixture.signIn( t , fixture.owner.ID )
	admin := fixture.signIn( t , second.ID )
	victim := fixture.signIn( t , third.ID )

	if status , reply := send( t , admin , userPath( third , "disabled" ) , fiber.Map{ "disabled": true } ); status != fiber.StatusForbidden { t.Fatalf( "disable: %d %v" , status , reply ) }
	if status , reply := send( t , admin , userPath( third , "revoke-access" ) , fiber.Map{} ); status != fiber.StatusForbidden { t.Fatalf( "revoke: %d %v" , status , reply ) }
	if user , _ := models.GetUser( fixture.store , third.ID ); user.Disabled() || victim.signedIn( t ) == false { t.Fatal( "the other admin was locked out" ) }

	// Any admin still does both to an ordinary user; the owner to an admin.
	if status , _ := send( t , admin , userPath( fixture.alice , "disabled" ) , fiber.Map{ "disabled": true } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	if status , _ := send( t , admin , userPath( fixture.alice , "revoke-access" ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	if status , _ := send( t , owner , userPath( third , "disabled" ) , fiber.Map{ "disabled": true } ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	// Switching an admin back on is the owner's too.
	if status , _ := send( t , admin , userPath( third , "disabled" ) , fiber.Map{ "disabled": false } ); status != fiber.StatusForbidden { t.Fatalf( "re-enable: %d" , status ) }
	if status , _ := send( t , owner , userPath( third , "revoke-access" ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
}

func TestAnAdminKeyCannotReissueTheOwnersLink( t *testing.T ) {
	fixture := linkFixture( t )
	second := secondAdmin( t , fixture )
	third := secondAdmin( t , fixture )
	key , _ , err := models.IssueAPIKey( fixture.store , second.ID , "ops" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	for _ , verb := range []string{ "reissue-login" , "cancel-login-link" } {
		for _ , target := range []*models.User{ fixture.owner , third } {
			if status , reply := fixture.withKey( t , key , http.MethodPost , userPath( target , verb ) , fiber.Map{} ); status != fiber.StatusForbidden || reply[ "login_path" ] != nil {
				t.Fatalf( "%s for #%d through a key: %d %v" , verb , target.ID , status , reply )
			}
		}
	}
	if status , reply := fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "reissue-login" ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }
}

func TestAnAdminKeyManagesAccounts( t *testing.T ) {
	fixture := newAuthFixture( t )
	key , _ , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "ops" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }

	status , reply := fixture.withKey( t , key , http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Bob" , "role": "user" } )
	if status != fiber.StatusCreated { t.Fatalf( "create: %d %v" , status , reply ) }
	if fixture.loginWith( t , reply[ "login_path" ].( string ) ) == false { t.Fatal( "the new account's link did not sign in" ) }

	if status , reply = fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "reissue-login" ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "reissue: %d %v" , status , reply ) }
	if fixture.loginWith( t , reply[ "login_path" ].( string ) ) == false { t.Fatal( "the reissued link did not sign in" ) }

	if status , reply = fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "rename" ) , fiber.Map{ "display_name": "Alicia" } ); status != fiber.StatusOK { t.Fatalf( "rename: %d %v" , status , reply ) }
	if status , reply = fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "role" ) , fiber.Map{ "role": "admin" } ); status != fiber.StatusOK { t.Fatalf( "role: %d %v" , status , reply ) }
	// The owner rules hold through a key as through a browser.
	if status , _ = fixture.withKey( t , key , http.MethodPost , userPath( fixture.owner , "delete" ) , fiber.Map{} ); status != fiber.StatusBadRequest { t.Fatalf( "deleting yourself: %d" , status ) }
	if status , reply = fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "delete" ) , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "delete: %d %v" , status , reply ) }

	// A user-scoped key on the same admin account is not an admin.
	user_key , _ , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "narrow" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , _ = fixture.withKey( t , user_key , http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Eve" , "role": "admin" } ); status != fiber.StatusForbidden { t.Fatalf( "narrowed key made an account: %d" , status ) }
}

// S9: a login link becomes an ordinary browser session, so an API key may
// make one for its own account only once that account allowed it, from a
// session. Its link then works once and for at most 15 minutes, whatever it
// asked for. A browser session always gets its link.
func TestAKeyMakesItsOwnLoginLinkOnlyWhenAllowed( t *testing.T ) {
	fixture := newAuthFixture( t )
	narrow , narrow_key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "narrow" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	admin , _ , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "admin" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	full , full_key , err := models.IssueAPIKey( fixture.store , fixture.alice.ID , "full" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	unlimited := fiber.Map{ "max_uses": 0 , "expires_in_minutes": 0 }
	for _ , key := range []string{ narrow , admin , full } {
		if status , reply := fixture.withKey( t , key , http.MethodPost , "/api/account/login-link" , unlimited ); status != fiber.StatusForbidden {
			t.Fatalf( "a key made its own login link unasked: %d %v" , status , reply )
		}
	}

	// A key cannot allow itself, and nobody but the key's own account can.
	allow := fiber.Map{ "may_sign_in": true }
	if status , _ := fixture.withKey( t , narrow , http.MethodPost , "/api/keys/"+narrow_key.ID+"/sign-in" , allow ); status != fiber.StatusForbidden {
		t.Fatalf( "a key allowed itself: %d" , status )
	}
	owner_session := fixture.signIn( t , fixture.owner.ID )
	if status , _ := send( t , owner_session , "/api/keys/"+full_key.ID+"/sign-in" , allow ); status != fiber.StatusNotFound {
		t.Fatalf( "the owner allowed someone else's key: %d" , status )
	}
	if status , reply := send( t , owner_session , "/api/keys/"+narrow_key.ID+"/sign-in" , allow ); status != fiber.StatusOK || reply[ "may_sign_in" ] != true {
		t.Fatalf( "allowing own key: %d %v" , status , reply )
	}

	status , reply := fixture.withKey( t , narrow , http.MethodPost , "/api/account/login-link" , unlimited )
	if status != fiber.StatusOK { t.Fatalf( "allowed key: %d %v" , status , reply ) }
	if seconds := reply[ "expires_in_seconds" ].( float64 ); seconds <= 0 || seconds > 15*60 { t.Fatalf( "a key's link lasts %v seconds" , seconds ) }
	path := reply[ "login_path" ].( string )
	if fixture.loginWith( t , path ) == false { t.Fatal( "the key's link did not sign in" ) }
	if fixture.loginWith( t , path ) { t.Fatal( "the key's link worked twice" ) }

	// Turned off again, the unused link it made stops working and the key is
	// refused again.
	_ , reply = fixture.withKey( t , narrow , http.MethodPost , "/api/account/login-link" , fiber.Map{} )
	unused := reply[ "login_path" ].( string )
	if status , _ := send( t , owner_session , "/api/keys/"+narrow_key.ID+"/sign-in" , fiber.Map{ "may_sign_in": false } ); status != fiber.StatusOK { t.Fatalf( "turning off: %d" , status ) }
	if fixture.loginWith( t , unused ) { t.Fatal( "an unused link worked after turning sign-in links off" ) }
	if status , _ := fixture.withKey( t , narrow , http.MethodPost , "/api/account/login-link" , fiber.Map{} ); status != fiber.StatusForbidden { t.Fatalf( "turned off, still allowed: %d" , status ) }

	status , reply = send( t , fixture.signIn( t , fixture.alice.ID ) , "/api/account/login-link" , fiber.Map{} )
	if status != fiber.StatusOK || fixture.loginWith( t , reply[ "login_path" ].( string ) ) == false { t.Fatalf( "session: %d %v" , status , reply ) }
}

// Revoking or deleting a key voids the login links it made that are still
// unused; a link a person made in the browser is left alone.
func TestRevokingAKeyVoidsTheLinksItMade( t *testing.T ) {
	for _ , how := range []string{ "revoke" , "delete" } {
		fixture := newAuthFixture( t )
		key , record , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "ops" , models.RoleAdmin , time.Hour )
		if err != nil { t.Fatal( err ) }
		_ , made_by_key := fixture.withKey( t , key , http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Bob" , "role": "user" } )
		_ , made_by_person := send( t , fixture.signIn( t , fixture.owner.ID ) , userPath( fixture.alice , "reissue-login" ) , fiber.Map{} )

		if how == "revoke" {
			err = models.RevokeAPIKey( fixture.store , record.ID )
		} else {
			err = models.DeleteAPIKey( fixture.store , record.ID , fixture.owner.ID )
		}
		if err != nil { t.Fatal( err ) }
		if fixture.loginWith( t , made_by_key[ "login_path" ].( string ) ) { t.Fatalf( "%s: the key's link still signs in" , how ) }
		if fixture.loginWith( t , made_by_person[ "login_path" ].( string ) ) == false { t.Fatalf( "%s: a person's link stopped working" , how ) }
	}
}

// S8: a browser signed in through a link a key made is signed out when the
// key is revoked or deleted; one signed in through a person's link is not.
// Before, the session outlived the key that made its way in.
func TestRevokingAKeyEndsTheSessionsItsLinksMade( t *testing.T ) {
	for _ , how := range []string{ "revoke" , "delete" } {
		fixture := newAuthFixture( t )
		key , record , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "ops" , models.RoleAdmin , time.Hour )
		if err != nil { t.Fatal( err ) }
		_ , by_key := fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "reissue-login" ) , fiber.Map{ "max_uses": 2 } )
		through_key := fixture.browser()
		if status , _ := through_key.post( t , "/api/login" , fiber.Map{ "credential": strings.TrimPrefix( by_key[ "login_path" ].( string ) , "/login/" ) } ); status != fiber.StatusOK { t.Fatalf( "%s: sign-in: %d" , how , status ) }
		by_person := fixture.signIn( t , fixture.alice.ID )

		if how == "revoke" {
			err = models.RevokeAPIKey( fixture.store , record.ID )
		} else {
			err = models.DeleteAPIKey( fixture.store , record.ID , fixture.owner.ID )
		}
		if err != nil { t.Fatal( err ) }
		if through_key.signedIn( t ) { t.Fatalf( "%s: the key's session is still signed in" , how ) }
		if by_person.signedIn( t ) == false { t.Fatalf( "%s: a person's session was signed out" , how ) }
		// Restoring the key does not bring it back.
		if how == "delete" {
			if _ , err = models.RestoreAPIKey( fixture.store , record.ID , fixture.owner.ID , true ); err != nil { t.Fatal( err ) }
			if through_key.signedIn( t ) { t.Fatal( "restoring the key restored its session" ) }
		}
	}
}
