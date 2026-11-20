package routes

import (
	strconv "strconv"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// linkFixture is newAuthFixture with the two default link lifetimes set, as
// a real config always has them.
func linkFixture( t *testing.T ) ( fixture *authFixture ) {
	fixture = newAuthFixture( t )
	fixture.cfg.LoginTokenTTL = 30 * time.Minute
	fixture.cfg.MultiUseLoginTokenTTL = 7 * 24 * time.Hour
	return
}

func idText( id uint64 ) ( result string ) {
	result = strconv.FormatUint( id , 10 )
	return
}

func credentialOf( reply map[string]any ) ( credential string ) {
	path , _ := reply[ "login_path" ].( string )
	credential = strings.TrimPrefix( path , "/login/" )
	return
}

func signInWith( t *testing.T , fixture *authFixture , credential string ) ( status int ) {
	t.Helper()
	status , _ = fixture.browser().post( t , "/api/login" , fiber.Map{ "credential": credential } )
	return
}

func TestMakingAReusableLoginLink( t *testing.T ) {
	fixture := linkFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )

	// The default is still once, for the configured half hour.
	status , reply := send( t , owner , "/api/admin/users" , fiber.Map{ "display_name": "Bea" , "role": "user" } )
	if status != fiber.StatusCreated || reply[ "max_uses" ] != float64( 1 ) || reply[ "unlimited" ] != false { t.Fatalf( "%d %v" , status , reply ) }
	if seconds , _ := reply[ "expires_in_seconds" ].( float64 ); seconds < 1790 || seconds > 1800 { t.Fatalf( "expires in %v" , seconds ) }

	// Three uses, and a link that works more than once lasts the multi-use default.
	path := "/api/admin/users/" + idText( fixture.alice.ID ) + "/reissue-login"
	status , reply = send( t , owner , path , fiber.Map{ "max_uses": 3 } )
	if status != fiber.StatusOK || reply[ "max_uses" ] != float64( 3 ) { t.Fatalf( "%d %v" , status , reply ) }
	if seconds , _ := reply[ "expires_in_seconds" ].( float64 ); seconds < 7*24*3600-10 { t.Fatalf( "expires in %v" , seconds ) }
	credential := credentialOf( reply )
	for range 3 {
		if status := signInWith( t , fixture , credential ); status != fiber.StatusOK { t.Fatalf( "sign-in: %d" , status ) }
	}
	if status := signInWith( t , fixture , credential ); status != fiber.StatusBadRequest { t.Fatalf( "fourth sign-in: %d" , status ) }

	// Unlimited and never expiring.
	status , reply = send( t , owner , path , fiber.Map{ "max_uses": 0 , "expires_in_minutes": 0 } )
	if status != fiber.StatusOK || reply[ "unlimited" ] != true || reply[ "expires_at" ] != nil || reply[ "expires_in_seconds" ] != float64( 0 ) {
		t.Fatalf( "%d %v" , status , reply )
	}
	credential = credentialOf( reply )
	for range 2 { signInWith( t , fixture , credential ) }

	// The user list shows it, with the uses so far.
	_ , list := owner.do( t , "GET" , "/api/admin/users" , nil )
	found := false
	for _ , row := range list[ "users" ].( []any ) {
		user := row.( map[string]any )
		if user[ "id" ] != float64( fixture.alice.ID ) { continue }
		link , _ := user[ "login_link" ].( map[string]any )
		found = link != nil && link[ "uses" ] == float64( 2 ) && link[ "unlimited" ] == true && link[ "expires_at" ] == nil
	}
	if found == false { t.Fatalf( "Alice's row does not show her link: %v" , list ) }

	// Cancelling stops it, and the row no longer shows one.
	if status , _ := send( t , owner , "/api/admin/users/"+idText( fixture.alice.ID )+"/cancel-login-link" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "cancel: %d" , status ) }
	if status := signInWith( t , fixture , credential ); status != fiber.StatusBadRequest { t.Fatalf( "a cancelled link let someone in: %d" , status ) }
	if link , _ := models.LiveLoginLink( fixture.store , fixture.alice.ID ); link != nil { t.Fatalf( "still live: %+v" , link ) }
}

func TestLoginLinkOptionsAreChecked( t *testing.T ) {
	fixture := linkFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	path := "/api/admin/users/" + idText( fixture.alice.ID ) + "/reissue-login"
	for _ , bad := range []fiber.Map{
		{ "max_uses": -1 },
		{ "max_uses": 1001 },
		{ "max_uses": true },
		{ "max_uses": "5" },
		{ "expires_in_minutes": -1 },
		{ "expires_in_minutes": 525601 },
		{ "expires_in_minutes": false },
	} {
		if status , _ := send( t , owner , path , bad ); status != fiber.StatusBadRequest { t.Fatalf( "%v: %d" , bad , status ) }
	}
	// A refused create makes no account.
	before , _ := models.ListUsers( fixture.store )
	if status , _ := send( t , owner , "/api/admin/users" , fiber.Map{ "display_name": "Cy" , "role": "user" , "max_uses": 5000 } ); status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }
	if after , _ := models.ListUsers( fixture.store ); len( after ) != len( before ) { t.Fatalf( "a refused create made an account" ) }
}

func TestYourOwnReusableLink( t *testing.T ) {
	fixture := linkFixture( t )
	alice := fixture.signIn( t , fixture.alice.ID )

	status , reply := send( t , alice , "/api/account/login-link" , fiber.Map{ "max_uses": 2 , "expires_in_minutes": 60 } )
	if status != fiber.StatusOK || reply[ "max_uses" ] != float64( 2 ) { t.Fatalf( "%d %v" , status , reply ) }
	credential := credentialOf( reply )
	signInWith( t , fixture , credential )

	_ , me := alice.do( t , "GET" , "/api/me" , nil )
	link , _ := me[ "login_link" ].( map[string]any )
	if link == nil || link[ "uses" ] != float64( 1 ) || link[ "max_uses" ] != float64( 2 ) { t.Fatalf( "me: %v" , me ) }

	if status , _ := send( t , alice , "/api/account/login-link/cancel" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "cancel: %d" , status ) }
	if status := signInWith( t , fixture , credential ); status != fiber.StatusBadRequest { t.Fatalf( "%d" , status ) }
	_ , me = alice.do( t , "GET" , "/api/me" , nil )
	if me[ "login_link" ] != nil { t.Fatalf( "me after cancel: %v" , me[ "login_link" ] ) }
}

// Someone else's link is an admin's to cancel, not a user's.
func TestOnlyAnAdminCancelsSomeoneElsesLink( t *testing.T ) {
	fixture := linkFixture( t )
	alice := fixture.signIn( t , fixture.alice.ID )
	path := "/api/admin/users/" + idText( fixture.owner.ID ) + "/cancel-login-link"
	if status , _ := send( t , alice , path , fiber.Map{} ); status != fiber.StatusForbidden { t.Fatalf( "%d" , status ) }
	owner := fixture.signIn( t , fixture.owner.ID )
	if status , _ := send( t , owner , "/api/admin/users/999/cancel-login-link" , fiber.Map{} ); status != fiber.StatusNotFound { t.Fatalf( "%d" , status ) }
}
