package routes

import (
	http "net/http"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// Portal contract v2 (ProjectPortal's CROSS_PROJECT.md): what an outside admin
// hub relies on, field for field.

func TestMeSaysTheContract( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	status , me := owner.do( t , http.MethodGet , "/api/me" , nil )
	if status != fiber.StatusOK || me[ "contract" ] != float64( 2 ) { t.Fatalf( "%d %v" , status , me ) }
	listed := map[ string ]bool{}
	for _ , name := range me[ "capabilities" ].( []any ) { listed[ name.( string ) ] = true }
	wanted := []string{ "viewer_role" , "account_expiry" }
	wanted = append( wanted , "key_minting" , "key_sign_in" )
	for _ , name := range wanted {
		if listed[ name ] == false { t.Fatalf( "%s missing from %v" , name , me[ "capabilities" ] ) }
	}
}

func TestAViewerReadsButChangesNothing( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	status , reply := send( t , owner , "/api/admin/users/" + idText( fixture.alice.ID ) + "/role" , fiber.Map{ "role": "viewer" } )
	if status != fiber.StatusOK || reply[ "role" ] != "viewer" { t.Fatalf( "%d %v" , status , reply ) }

	alice := fixture.signIn( t , fixture.alice.ID )
	status , me := alice.do( t , http.MethodGet , "/api/me" , nil )
	if status != fiber.StatusOK || me[ "role" ] != "viewer" { t.Fatalf( "%d %v" , status , me ) }
	status , reply = send( t , alice , "/api/account/rename" , fiber.Map{ "display_name": "Mallory" } )
	if status != fiber.StatusForbidden || reply[ "error" ] != "view_only" { t.Fatalf( "rename: %d %v" , status , reply ) }
	if user , _ := models.GetUser( fixture.store , fixture.alice.ID ); user.DisplayName != "Alice" { t.Fatalf( "renamed to %q" , user.DisplayName ) }
	// What a viewer may still send.
	status , reply = send( t , alice , "/api/account/login-link/cancel" , fiber.Map{} )
	if status != fiber.StatusOK { t.Fatalf( "cancel: %d %v" , status , reply ) }
	status , reply = send( t , alice , "/api/account/sessions/revoke-others" , fiber.Map{} )
	if status != fiber.StatusOK { t.Fatalf( "revoke others: %d %v" , status , reply ) }
	status , reply = send( t , alice , "/api/logout" , fiber.Map{} )
	if status != fiber.StatusOK { t.Fatalf( "logout: %d %v" , status , reply ) }

	// A key for a viewer is a viewer key, and a viewer key on an admin
	// account reads only.
	key , _ , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "reader" , models.RoleViewer , time.Hour )
	if err != nil { t.Fatal( err ) }
	status , me = fixture.withKey( t , key , http.MethodGet , "/api/me" , nil )
	if status != fiber.StatusOK || me[ "role" ] != "viewer" || me[ "account_role" ] != "admin" { t.Fatalf( "%d %v" , status , me ) }
	status , reply = fixture.withKey( t , key , http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Eve" , "role": "user" } )
	if status != fiber.StatusForbidden || reply[ "error" ] != "view_only" { t.Fatalf( "%d %v" , status , reply ) }
}

func TestAnAccountExpires( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	later := time.Now().UTC().Add( 48 * time.Hour ).Truncate( time.Second )
	status , reply := send( t , owner , "/api/admin/users" , fiber.Map{ "display_name": "Guest" , "role": "user" , "account_expires_at": later.Format( time.RFC3339 ) } )
	if status != fiber.StatusCreated { t.Fatalf( "%d %v" , status , reply ) }
	guest_id := uint64( reply[ "user_id" ].( float64 ) )
	if guest , _ := models.GetUser( fixture.store , guest_id ); guest.ExpiresAt == nil || guest.ExpiresAt.Equal( later ) == false {
		t.Fatalf( "%v" , guest.ExpiresAt )
	}

	// Alice works until her expiry passes, and not a moment after.
	alice := fixture.signIn( t , fixture.alice.ID )
	if status , _ := alice.do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	soon := time.Now().UTC().Add( time.Hour ).Format( time.RFC3339 )
	status , reply = send( t , owner , "/api/admin/users/" + idText( fixture.alice.ID ) + "/expiry" , fiber.Map{ "expires_at": soon } )
	if status != fiber.StatusOK || reply[ "ok" ] != true || reply[ "expires_at" ] == nil { t.Fatalf( "%d %v" , status , reply ) }
	past := time.Now().UTC().Add( -time.Minute ).Format( time.RFC3339 )
	if status , reply = send( t , owner , "/api/admin/users/" + idText( fixture.alice.ID ) + "/expiry" , fiber.Map{ "expires_at": past } ); status != fiber.StatusBadRequest { t.Fatalf( "past: %d %v" , status , reply ) }
	// Let the hour pass.
	if err := models.SaveUser( fixture.store , func() *models.User { user , _ := models.GetUser( fixture.store , fixture.alice.ID ); at := time.Now().UTC().Add( -time.Second ); user.ExpiresAt = &at; return user }() ); err != nil { t.Fatal( err ) }
	if status , _ := alice.do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "still signed in: %d" , status ) }
	if status := signInWith( t , fixture , fixture.link( t , fixture.alice.ID ) ); status == fiber.StatusOK { t.Fatalf( "signed in when expired" ) }

	status , page := owner.do( t , http.MethodGet , "/api/admin/users" , nil )
	if status != fiber.StatusOK { t.Fatalf( "%d" , status ) }
	for _ , item := range page[ "users" ].( []any ) {
		row := item.( map[ string ]any )
		if _ , has := row[ "expires_at" ]; has == false { t.Fatalf( "no expires_at: %v" , row ) }
		if row[ "id" ] == float64( fixture.alice.ID ) && ( row[ "expired" ] != true || row[ "disabled" ] != true ) { t.Fatalf( "%v" , row ) }
		if row[ "id" ] == float64( fixture.owner.ID ) && ( row[ "expired" ] != false || row[ "expires_at" ] != nil ) { t.Fatalf( "%v" , row ) }
	}

	// Housekeeping makes it stick: disabled, access revoked.
	expired , err := models.ExpireAccounts( fixture.store )
	if err != nil || expired != 1 { t.Fatalf( "%d %v" , expired , err ) }
	if user , _ := models.GetUser( fixture.store , fixture.alice.ID ); user.DisabledAt == nil { t.Fatal( "not disabled" ) }
	if expired , _ = models.ExpireAccounts( fixture.store ); expired != 0 { t.Fatalf( "twice: %d" , expired ) }

	// Null is never; the owner never expires, and nobody sets their own.
	status , reply = send( t , owner , "/api/admin/users/" + idText( guest_id ) + "/expiry" , fiber.Map{ "expires_at": nil } )
	if status != fiber.StatusOK || reply[ "expires_at" ] != nil { t.Fatalf( "%d %v" , status , reply ) }
	status , reply = send( t , owner , "/api/admin/users/" + idText( fixture.owner.ID ) + "/expiry" , fiber.Map{ "expires_at": soon } )
	if status != fiber.StatusForbidden || reply[ "error" ] != "the owner's account cannot expire" { t.Fatalf( "owner: %d %v" , status , reply ) }
}


func TestAKeyMintsKeysWithLineage( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	portal , portal_key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "portal" , models.RoleAdmin , 0 )
	if err != nil { t.Fatal( err ) }
	mint := "/api/admin/users/" + idText( fixture.alice.ID ) + "/keys"
	body := fiber.Map{ "name": "for alice" , "role": "admin" , "expires_in_days": 30 }

	// Off until the owner switches it on, from a session.
	status , reply := fixture.withKey( t , portal , http.MethodPost , mint , body )
	if status != fiber.StatusForbidden || reply[ "error" ] != "key_minting_off" { t.Fatalf( "%d %v" , status , reply ) }
	status , reply = fixture.withKey( t , portal , http.MethodPost , "/api/keys/" + portal_key.ID + "/mint-keys" , fiber.Map{ "may_mint_keys": true } )
	if status != fiber.StatusForbidden { t.Fatalf( "a key switched itself on: %d %v" , status , reply ) }
	status , reply = send( t , owner , "/api/keys/" + portal_key.ID + "/mint-keys" , fiber.Map{ "may_mint_keys": true } )
	if status != fiber.StatusOK || reply[ "may_mint_keys" ] != true { t.Fatalf( "%d %v" , status , reply ) }
	if status , me := fixture.withKey( t , portal , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK || me[ "may_mint_keys" ] != true { t.Fatalf( "%d %v" , status , me ) }

	// Capped at the account's role, recorded, and never a key that mints or signs in.
	status , reply = fixture.withKey( t , portal , http.MethodPost , mint , body )
	if status != fiber.StatusCreated || reply[ "role" ] != "user" || reply[ "minted_by_key" ] != portal_key.ID || reply[ "credential" ] == nil { t.Fatalf( "%d %v" , status , reply ) }
	child_id := reply[ "id" ].( string )
	child := reply[ "credential" ].( string )
	if status , me := fixture.withKey( t , child , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK || me[ "id" ] != float64( fixture.alice.ID ) { t.Fatalf( "%d %v" , status , me ) }
	alice := fixture.signIn( t , fixture.alice.ID )
	if status , reply = send( t , alice , "/api/keys/" + child_id + "/sign-in" , fiber.Map{ "may_sign_in": true } ); status != fiber.StatusBadRequest { t.Fatalf( "sign-in on a minted key: %d %v" , status , reply ) }

	// Expiry is required; the owner is out of reach.
	if status , reply = fixture.withKey( t , portal , http.MethodPost , mint , fiber.Map{ "name": "x" , "role": "user" , "expires_in_days": 0 } ); status != fiber.StatusBadRequest { t.Fatalf( "no expiry: %d %v" , status , reply ) }
	status , reply = fixture.withKey( t , portal , http.MethodPost , "/api/admin/users/" + idText( fixture.owner.ID ) + "/keys" , body )
	if status != fiber.StatusForbidden { t.Fatalf( "for the owner: %d %v" , status , reply ) }
	status , reply = fixture.withKey( t , portal , http.MethodPost , "/api/admin/keys/" + portal_key.ID + "/revoke" , fiber.Map{} )
	if status != fiber.StatusForbidden { t.Fatalf( "revoked the owner's key: %d %v" , status , reply ) }

	// The list says where each key came from.
	status , page := fixture.withKey( t , portal , http.MethodGet , mint , nil )
	if status != fiber.StatusOK { t.Fatalf( "%d %v" , status , page ) }
	rows := page[ "keys" ].( []any )
	if len( rows ) != 1 || rows[ 0 ].( map[ string ]any )[ "minted_by_key" ] != portal_key.ID { t.Fatalf( "%v" , rows ) }

	// The minting key revokes what it made.
	status , reply = fixture.withKey( t , portal , http.MethodPost , "/api/admin/keys/" + child_id + "/revoke" , fiber.Map{} )
	if status != fiber.StatusOK || reply[ "ok" ] != true { t.Fatalf( "%d %v" , status , reply ) }
	if status , _ := fixture.withKey( t , child , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "revoked key works: %d" , status ) }

	// Revoking the minting key revokes the whole chain below it.
	status , reply = fixture.withKey( t , portal , http.MethodPost , mint , body )
	if status != fiber.StatusCreated { t.Fatalf( "%d %v" , status , reply ) }
	second := reply[ "credential" ].( string )
	_ , grandchild , err := models.MintAPIKey( fixture.store , fixture.alice.ID , reply[ "id" ].( string ) , fixture.alice.ID , "below" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , reply = send( t , owner , "/api/keys/" + portal_key.ID + "/revoke" , fiber.Map{} ); status != fiber.StatusOK { t.Fatalf( "%d %v" , status , reply ) }
	if status , _ := fixture.withKey( t , second , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Fatalf( "child outlived its maker: %d" , status ) }
	if found , _ := models.GetAPIKey( fixture.store , grandchild.ID ); found.Live() { t.Fatal( "grandchild outlived the chain" ) }
}

func TestOnlyTheOwnerLetsAKeyMint( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Second admin" , models.RoleAdmin )
	_ , key , err := models.IssueAPIKey( fixture.store , second.ID , "theirs" , models.RoleAdmin , 0 )
	if err != nil { t.Fatal( err ) }
	admin := fixture.signIn( t , second.ID )
	if status , reply := send( t , admin , "/api/keys/" + key.ID + "/mint-keys" , fiber.Map{ "may_mint_keys": true } ); status != fiber.StatusForbidden { t.Fatalf( "%d %v" , status , reply ) }
	_ , user_key , err := models.IssueAPIKey( fixture.store , fixture.owner.ID , "plain" , models.RoleUser , 0 )
	if err != nil { t.Fatal( err ) }
	owner := fixture.signIn( t , fixture.owner.ID )
	if status , reply := send( t , owner , "/api/keys/" + user_key.ID + "/mint-keys" , fiber.Map{ "may_mint_keys": true } ); status != fiber.StatusBadRequest { t.Fatalf( "user-role key: %d %v" , status , reply ) }
}
