package routes

import (
	errors "errors"
	http "net/http"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	models "vocabtrainer/server/models"
)

// ProjectPortal's own account (portal contract v3, item 2): a service
// account the owner connects from the API keys screen, with keys only, never
// signed into, and run only by the owner.

func connectPortal( t *testing.T , fixture *authFixture , owner *browser , body fiber.Map ) ( credential string , reply map[ string ]any ) {
	t.Helper()
	status , reply := send( t , owner , "/api/owner/portal/connect" , body )
	if status != fiber.StatusCreated { t.Fatalf( "connect: %d %v" , status , reply ) }
	credential = reply[ "credential" ].( string )
	return
}

func TestConnectMakesTheServiceAccountAndItsKey( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	credential , reply := connectPortal( t , fixture , owner , fiber.Map{ "days": 365 } )
	if reply[ "display_name" ] != "ProjectPortal" || reply[ "role" ] != "admin" || reply[ "may_mint_keys" ] != true || reply[ "may_sign_in" ] != false {
		t.Fatalf( "%v" , reply )
	}
	user_id := uint64( reply[ "user_id" ].( float64 ) )
	status , me := fixture.withKey( t , credential , http.MethodGet , "/api/me" , nil )
	if status != fiber.StatusOK || me[ "kind" ] != "service" || me[ "owner" ] != false || me[ "role" ] != "admin" { t.Fatalf( "%d %v" , status , me ) }
	grants := me[ "key" ].( map[ string ]any )
	if grants[ "may_mint_keys" ] != true || grants[ "may_sign_in" ] != false { t.Fatalf( "%v" , grants ) }
	expires , _ := time.Parse( time.RFC3339 , grants[ "expires_at" ].( string ) )
	if days := time.Until( expires ).Hours() / 24; days < 364 || days > 365 { t.Fatalf( "expires in %v days" , days ) }
	listed := false
	for _ , name := range me[ "capabilities" ].( []any ) { listed = listed || name == "service_accounts" }
	if listed == false { t.Fatalf( "service_accounts missing: %v" , me[ "capabilities" ] ) }

	// Connect again: the same account, one more key.
	_ , again := connectPortal( t , fixture , owner , fiber.Map{ "name": "ProjectPortal" , "days": 30 } )
	if uint64( again[ "user_id" ].( float64 ) ) != user_id { t.Fatalf( "a second account: %v" , again ) }
	status , page := owner.do( t , http.MethodGet , "/api/owner/portal" , nil )
	if status != fiber.StatusOK { t.Fatalf( "%d %v" , status , page ) }
	accounts := page[ "accounts" ].( []any )
	if len( accounts ) != 1 { t.Fatalf( "%v" , page ) }
	account := accounts[ 0 ].( map[ string ]any )
	if account[ "id" ] != float64( user_id ) || len( account[ "keys" ].( []any ) ) != 2 { t.Fatalf( "%v" , account ) }

	// The accounts list says which account is a service account.
	_ , users := owner.do( t , http.MethodGet , "/api/admin/users" , nil )
	for _ , item := range users[ "users" ].( []any ) {
		row := item.( map[ string ]any )
		want := "person"
		if row[ "id" ] == float64( user_id ) { want = "service" }
		if row[ "kind" ] != want { t.Fatalf( "%v" , row ) }
	}

	// The key lifetime is required, 1-365 days.
	for _ , days := range []any{ 0 , 366 , nil } {
		body := fiber.Map{ "days": days }
		if days == nil { body = fiber.Map{} }
		status , reply := send( t , owner , "/api/owner/portal/connect" , body )
		if status != fiber.StatusBadRequest || reply[ "field" ] != "days" { t.Fatalf( "days %v: %d %v" , days , status , reply ) }
	}
}

func TestOnlyTheOwnerConnectsThePortal( t *testing.T ) {
	fixture := newAuthFixture( t )
	second , _ := models.CreateUser( fixture.store , "Sam" , models.RoleAdmin )
	admin := fixture.signIn( t , second.ID )
	for _ , path := range []string{ "/api/owner/portal/connect" , "/api/owner/portal/disconnect" } {
		if status , reply := send( t , admin , path , fiber.Map{ "days": 30 , "user_id": 1 } ); status != fiber.StatusNotFound { t.Fatalf( "%s: %d %v" , path , status , reply ) }
	}
	if status , reply := admin.do( t , http.MethodGet , "/api/owner/portal" , nil ); status != fiber.StatusNotFound { t.Fatalf( "%d %v" , status , reply ) }
}

func TestAServiceAccountIsNeverSignedInto( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	credential , reply := connectPortal( t , fixture , owner , fiber.Map{ "days": 30 } )
	user_id := uint64( reply[ "user_id" ].( float64 ) )
	service := "/api/admin/users/" + idText( user_id )
	key_id := reply[ "id" ].( string )

	refused := func( what string , status int , reply map[ string ]any ) {
		t.Helper()
		if status != fiber.StatusForbidden || reply[ "code" ] != apidoc.CodeServiceAccount { t.Errorf( "%s: %d %v, want 403 service_account" , what , status , reply ) }
	}
	status , reply := send( t , owner , "/api/admin/users" , fiber.Map{ "display_name": "Bot" , "role": "admin" , "kind": "service" } )
	refused( "creating one by hand" , status , reply )
	status , reply = send( t , owner , service + "/reissue-login" , fiber.Map{} )
	refused( "a login link" , status , reply )
	status , reply = send( t , owner , service + "/role" , fiber.Map{ "role": "user" } )
	refused( "a role change" , status , reply )
	status , reply = fixture.withKey( t , credential , http.MethodPost , "/api/account/login-link" , fiber.Map{ "max_uses": 1 , "expires_in_minutes": 15 } )
	if status != fiber.StatusForbidden { t.Errorf( "its key making a login link: %d %v" , status , reply ) }
	if _ , err := models.SetAPIKeySignIn( fixture.store , key_id , user_id , true ); errors.Is( err , models.ErrServiceAccount ) == false {
		t.Errorf( "sign-in links on its key: %v" , err )
	}
	if _ , _ , err := models.IssueLoginLink( fixture.store , user_id , models.LoginLinkOptions{ MaxUses: 1 , TTL: time.Minute } ); errors.Is( err , models.ErrServiceAccount ) == false {
		t.Errorf( "a login link from a shell: %v" , err )
	}
}

func TestTheServiceAccountIsTheOwnersAlone( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	_ , reply := connectPortal( t , fixture , owner , fiber.Map{ "days": 30 } )
	user_id := uint64( reply[ "user_id" ].( float64 ) )
	service := "/api/admin/users/" + idText( user_id )

	// Nobody but the owner revokes the portal's access, disables it or
	// lists its keys.
	second , _ := models.CreateUser( fixture.store , "Sam" , models.RoleAdmin )
	admin := fixture.signIn( t , second.ID )
	for action , body := range map[ string ]fiber.Map{ "revoke-access": {} , "disabled": { "disabled": true } , "delete": {} } {
		status , reply := send( t , admin , service + "/" + action , body )
		if status != fiber.StatusForbidden || reply[ "code" ] != apidoc.CodeOwnerRequired { t.Errorf( "%s by another admin: %d %v" , action , status , reply ) }
	}
	if status , reply := admin.do( t , http.MethodGet , service + "/keys" , nil ); status != fiber.StatusForbidden { t.Errorf( "keys: %d %v" , status , reply ) }
	if err := models.TransferOwner( fixture.store , user_id ); errors.Is( err , models.ErrServiceAccount ) == false { t.Errorf( "transfer-owner: %v" , err ) }
}

func TestTheOwnerLetsAServiceKeyMint( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	_ , reply := connectPortal( t , fixture , owner , fiber.Map{ "days": 30 } )
	key_id := reply[ "id" ].( string )
	status , reply := send( t , owner , "/api/keys/" + key_id + "/mint-keys" , fiber.Map{ "may_mint_keys": false } )
	if status != fiber.StatusOK || reply[ "may_mint_keys" ] != false { t.Fatalf( "off: %d %v" , status , reply ) }
	status , reply = send( t , owner , "/api/keys/" + key_id + "/mint-keys" , fiber.Map{ "may_mint_keys": true } )
	if status != fiber.StatusOK || reply[ "may_mint_keys" ] != true { t.Fatalf( "on: %d %v" , status , reply ) }

	// Still nobody else's key: another admin's admin key stays out of reach.
	second , _ := models.CreateUser( fixture.store , "Sam" , models.RoleAdmin )
	_ , theirs , err := models.IssueAPIKey( fixture.store , second.ID , "theirs" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }
	if status , reply = send( t , owner , "/api/keys/" + theirs.ID + "/mint-keys" , fiber.Map{ "may_mint_keys": true } ); status == fiber.StatusOK { t.Fatalf( "another admin's key: %d %v" , status , reply ) }
}

func TestDisconnectEndsThePortalAndAllItMade( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	credential , reply := connectPortal( t , fixture , owner , fiber.Map{ "days": 30 } )
	user_id := reply[ "user_id" ].( float64 )
	status , minted := fixture.withKey( t , credential , http.MethodPost , "/api/admin/users/" + idText( fixture.alice.ID ) + "/keys" , fiber.Map{ "name": "for alice" , "role": "user" , "expires_in_days": 30 } )
	if status != fiber.StatusCreated { t.Fatalf( "%d %v" , status , minted ) }
	child := minted[ "credential" ].( string )

	status , reply = send( t , owner , "/api/owner/portal/disconnect" , fiber.Map{ "user_id": user_id } )
	if status != fiber.StatusOK || reply[ "revoked" ] != float64( 1 ) { t.Fatalf( "%d %v" , status , reply ) }
	for what , key := range map[ string ]string{ "the portal's key": credential , "a key it made": child } {
		if status , _ := fixture.withKey( t , key , http.MethodGet , "/api/me" , nil ); status != fiber.StatusUnauthorized { t.Errorf( "%s still works: %d" , what , status ) }
	}
	// Only a service account is disconnected this way.
	if status , reply = send( t , owner , "/api/owner/portal/disconnect" , fiber.Map{ "user_id": fixture.alice.ID } ); status != fiber.StatusNotFound { t.Fatalf( "a person: %d %v" , status , reply ) }
}

// `manage portal-connect`: the shell is the owner's say-so, but never before
// there is an owner, when the service account would become it.
func TestTheShellConnectsThePortal( t *testing.T ) {
	fixture := newAuthFixture( t )
	credential , key , user , err := models.ConnectPortal( fixture.store , 0 , models.DefaultPortalName , 24 * time.Hour )
	if err != nil || user.IsService() == false || key.MayMintKeys == false || credential == "" { t.Fatalf( "%v %v %v" , user , key , err ) }
	if _ , _ , _ , err = models.ConnectPortal( fixture.store , fixture.alice.ID , "Other" , time.Hour ); errors.Is( err , models.ErrOwnerRequired ) == false {
		t.Fatalf( "an ordinary account: %v" , err )
	}
}

// The portal reads an ordinary account's devices (admin_sessions) to tell who
// is who across apps: its service account sees the addresses, which other
// admins do not, and the rows carry no secret.
func TestThePortalReadsAnAccountsDevices( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	credential , _ := connectPortal( t , fixture , owner , fiber.Map{ "days": 30 } )
	alice := fixture.signIn( t , fixture.alice.ID )
	device := alice.devices( t )[ 0 ][ "id" ].( string )
	if status , reply := alice.post( t , "/api/account/sessions/" + device + "/label" , fiber.Map{ "label": "Phone" , "csrf_token": alice.csrf( t ) } ); status != http.StatusOK {
		t.Fatalf( "label: %d %v" , status , reply )
	}

	status , me := fixture.withKey( t , credential , http.MethodGet , "/api/me" , nil )
	listed := false
	for _ , name := range me[ "capabilities" ].( []any ) { listed = listed || name == "admin_sessions" }
	if status != http.StatusOK || listed == false { t.Fatalf( "admin_sessions missing: %d %v" , status , me[ "capabilities" ] ) }

	status , page := fixture.withKey( t , credential , http.MethodGet , "/api/admin/users/" + idText( fixture.alice.ID ) + "/sessions" , nil )
	rows , _ := page[ "sessions" ].( []any )
	if status != http.StatusOK || len( rows ) != 1 { t.Fatalf( "%d %v" , status , page ) }
	row := rows[ 0 ].( map[ string ]any )
	allowed := map[ string ]bool{ "id": true , "current": true , "created_at": true , "expires_at": true , "last_seen_at": true , "ip": true , "user_agent": true , "label": true }
	for name := range row {
		if allowed[ name ] == false { t.Errorf( "the portal's row carries %q: %v" , name , row ) }
	}
	if row[ "id" ] != device || row[ "label" ] != "Phone" { t.Errorf( "row: %v" , row ) }
	if ip , _ := row[ "ip" ].( string ); ip == "" { t.Errorf( "the portal should see the address: %v" , row ) }
	for _ , name := range []string{ "created_at" , "last_seen_at" } {
		text , present := row[ name ].( string )
		if present == false && name == "last_seen_at" { continue }
		if _ , err := time.Parse( time.RFC3339 , text ); err != nil { t.Errorf( "%s is not RFC 3339: %v" , name , row[ name ] ) }
	}
}
