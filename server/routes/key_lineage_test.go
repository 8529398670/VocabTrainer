package routes

import (
	http "net/http"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	db "vocabtrainer/server/db"
	models "vocabtrainer/server/models"
)

// What a key can leave behind (security round 3, from the ProjectPortal +
// TodoBoard audit of 2026-11-22). Revoking a key, or its expiry, must end
// everything it made, however indirectly: through a login link it reissued
// for its own account, through the browser session such a link opened, or
// through a key it minted.

// keyBornSession signs a browser in through a login link the key made for its
// own account, the way ProjectPortal's Open does.
func keyBornSession( t *testing.T , fixture *authFixture , key string ) ( client *browser ) {
	t.Helper()
	status , reply := fixture.withKey( t , key , http.MethodPost , "/api/account/login-link" , fiber.Map{} )
	if status != fiber.StatusOK { t.Fatalf( "key's own login link: %d %v" , status , reply ) }
	client = fixture.browser()
	credential := reply[ "login_path" ].( string )[ len( "/login/" ): ]
	if status , reply = client.post( t , "/api/login" , fiber.Map{ "credential": credential } ); status != fiber.StatusOK {
		t.Fatalf( "signing in with it: %d %v" , status , reply )
	}
	return
}

// H1: reissue-login pointed at your own account was a way round MaySignIn
// and its once-and-15-minutes cap.
func TestAKeyCannotReissueItsOwnLoginLink( t *testing.T ) {
	fixture := newAuthFixture( t )
	key , stored , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "script" , models.RoleAdmin , 0 )
	if err != nil { t.Fatal( err ) }
	own := "/api/admin/users/" + idText( fixture.owner.ID ) + "/reissue-login"
	forever := fiber.Map{ "max_uses": 0 , "expires_in_minutes": 0 }

	status , reply := fixture.withKey( t , key , http.MethodPost , own , forever )
	if status != fiber.StatusForbidden { t.Fatalf( "a key without sign-in links reissued its own: %d %v" , status , reply ) }

	// Allowed to sign in, it gets what /api/account/login-link gives a key.
	owner := fixture.signIn( t , fixture.owner.ID )
	if status , reply = send( t , owner , "/api/keys/" + stored.ID + "/sign-in" , fiber.Map{ "may_sign_in": true } ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }
	status , reply = fixture.withKey( t , key , http.MethodPost , own , forever )
	if status != fiber.StatusOK || reply[ "max_uses" ] != float64( 1 ) || reply[ "expires_in_seconds" ].( float64 ) > 15 * 60 {
		t.Fatalf( "a key's own link must be once, 15 minutes at most: %d %v" , status , reply )
	}

	// A browser session reissuing its own (the Users screen) is unchanged.
	if status , reply = send( t , owner , own , forever ); status != fiber.StatusOK || reply[ "unlimited" ] != true { t.Fatalf( "session: %d %v" , status , reply ) }
}

// H2: a session a key opened made keys, links and credentials that outlived
// revoking the key.
func TestAKeyBornSessionLeavesNothingBehind( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	key , stored , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "portal" , models.RoleAdmin , 0 )
	if err != nil { t.Fatal( err ) }
	_ , other , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "ci" , models.RoleAdmin , 0 )
	if err != nil { t.Fatal( err ) }
	if status , reply := send( t , owner , "/api/keys/" + stored.ID + "/sign-in" , fiber.Map{ "may_sign_in": true } ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }

	borrowed := keyBornSession( t , fixture , key )

	// What it makes records the key.
	status , reply := send( t , borrowed , "/api/keys" , fiber.Map{ "name": "left behind" , "role": "admin" , "expires_in_days": 0 } )
	if status != fiber.StatusCreated || reply[ "minted_by_key" ] != stored.ID { t.Fatalf( "a key made in a key's session: %d %v" , status , reply ) }
	left := reply[ "credential" ].( string )
	status , reply = send( t , borrowed , "/api/account/login-link" , fiber.Map{ "max_uses": 0 , "expires_in_minutes": 0 } )
	if status != fiber.StatusOK { t.Fatalf( "own link from a key's session: %d %v" , status , reply ) }
	spare := reply[ "login_path" ].( string )

	// What would hand out a credential nothing ties to the key is refused.
	if status , reply = send( t , borrowed , "/api/keys/" + other.ID + "/rotate" , fiber.Map{} ); status != fiber.StatusForbidden || reply[ "code" ] != "session_only" {
		t.Fatalf( "rotating another key from a key's session: %d %v" , status , reply )
	}

	// The owner's own session still does all of it.
	if status , reply = send( t , owner , "/api/keys/" + other.ID + "/rotate" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "owner rotating: %d %v" , status , reply ) }

	// Revoking the key ends all of it.
	if status , reply = send( t , owner , "/api/keys/" + stored.ID + "/revoke" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "revoke: %d %v" , status , reply ) }
	if borrowed.signedIn( t ) { t.Fatal( "the key's session outlived it" ) }
	if status , _ := fixture.withKey( t , left , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "a key made in its session outlived it: %d" , status ) }
	if fixture.loginWith( t , spare ) { t.Fatal( "a link made in its session outlived it" ) }
}

// A key's expiry ends the sessions its links opened, as revoking it does:
// nothing runs at the expiry, so the session is checked as it is loaded.
func TestAKeyBornSessionEndsWhenTheKeyExpires( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	key , stored , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "portal" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , reply := send( t , owner , "/api/keys/" + stored.ID + "/sign-in" , fiber.Map{ "may_sign_in": true } ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }
	borrowed := keyBornSession( t , fixture , key )
	if borrowed.signedIn( t ) == false { t.Fatal( "not signed in" ) }

	past := time.Now().UTC().Add( -time.Minute )
	if err = fixture.store.UpdateValue( db.BucketAPIKeys , []byte( stored.ID ) , func() any { return &models.APIKey{} } , func( item any ) ( mutate_err error ) {
		item.( *models.APIKey ).ExpiresAt = &past
		return
	} ); err != nil { t.Fatal( err ) }
	if borrowed.signedIn( t ) { t.Fatal( "the key's session outlived its expiry" ) }
	if owner.signedIn( t ) == false { t.Fatal( "the owner's own session ended too" ) }
}

// M1: a minted key lived past the key that made it.
func TestAMintedKeyDiesWithItsParent( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	parent , stored , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "portal" , models.RoleAdmin , 24 * time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , reply := send( t , owner , "/api/keys/" + stored.ID + "/mint-keys" , fiber.Map{ "may_mint_keys": true } ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }

	status , reply := fixture.withKey( t , parent , http.MethodPost , "/api/admin/users/" + idText( fixture.alice.ID ) + "/keys" , fiber.Map{ "name": "long" , "role": "user" , "expires_in_days": 365 } )
	if status != fiber.StatusCreated { t.Fatalf( "%d %v" , status , reply ) }
	child_id := reply[ "id" ].( string )
	expires , _ := time.Parse( time.RFC3339Nano , reply[ "expires_at" ].( string ) )
	if expires.After( *stored.ExpiresAt ) { t.Fatalf( "the child expires %v, after its parent's %v" , expires , *stored.ExpiresAt ) }

	// Its account cannot stretch it past the parent either.
	alice := fixture.signIn( t , fixture.alice.ID )
	status , reply = send( t , alice , "/api/keys/" + child_id + "/expiry" , fiber.Map{ "expires_in_days": 0 } )
	if status == fiber.StatusOK {
		child , _ := models.GetAPIKey( fixture.store , child_id )
		if child.ExpiresAt == nil || child.ExpiresAt.After( *stored.ExpiresAt ) { t.Fatalf( "stretched to %v past %v" , child.ExpiresAt , *stored.ExpiresAt ) }
	}
}

// M2: an admin cleared the expiry the owner set on them.
func TestAnAdminCannotChangeTheirOwnExpiry( t *testing.T ) {
	fixture := newAuthFixture( t )
	bob , _ := models.CreateUser( fixture.store , "Bob" , models.RoleAdmin )
	owner := fixture.signIn( t , fixture.owner.ID )
	path := "/api/admin/users/" + idText( bob.ID ) + "/expiry"
	soon := time.Now().Add( 2 * time.Hour ).UTC().Format( time.RFC3339 )
	if status , reply := send( t , owner , path , fiber.Map{ "expires_at": soon } ); status != fiber.StatusOK { t.Fatalf( "owner: %d %v" , status , reply ) }

	admin := fixture.signIn( t , bob.ID )
	if status , reply := send( t , admin , path , fiber.Map{ "expires_at": nil } ); status != fiber.StatusForbidden { t.Fatalf( "an admin cleared their own expiry: %d %v" , status , reply ) }
	key , _ , err := models.IssueAPIKey( fixture.store , bob.ID , "bob's" , models.RoleAdmin , 0 )
	if err != nil { t.Fatal( err ) }
	if status , reply := fixture.withKey( t , key , http.MethodPost , path , fiber.Map{ "expires_at": nil } ); status != fiber.StatusForbidden { t.Fatalf( "with a key: %d %v" , status , reply ) }
	if user , _ := models.GetUser( fixture.store , bob.ID ); user.ExpiresAt == nil { t.Fatal( "the expiry is gone" ) }
}
