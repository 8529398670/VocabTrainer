package routes

import (
	net "net"
	http "net/http"
	exec "os/exec"
	filepath "path/filepath"
	runtime "runtime"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	db "vocabtrainer/server/db"
	models "vocabtrainer/server/models"
)

// The portal contract, route by route (portal contract v3, item 7). Keep this
// file as it is in every app: an app-specific change that breaks what
// ProjectPortal relies on -- an owner-only guard on an account route, a
// refusal without its code -- fails the app's own go test here.
//
// ProjectPortal works through an admin account of its own, never the owner's.
// The table is run four ways: with the portal's key on an ordinary account,
// with an ordinary user's key, with a viewer key, and on the owner and on
// another admin, where the ladder must hold.

// contractCall is one call ProjectPortal makes. In path, {user} is the
// account it acts on and {key} a key on that account.
type contractCall struct {
	method string
	path   string
	body   fiber.Map
	status int
	// ladder: the call acts on one account, so only the owner may make it on
	// another admin, and nobody on the owner.
	ladder bool
}

func contractCalls() ( calls []contractCall ) {
	soon := time.Now().UTC().Add( time.Hour ).Format( time.RFC3339 )
	calls = []contractCall{
		{ http.MethodGet , "/api/admin/users?limit=1" , nil , fiber.StatusOK , false },
		{ http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Cy" , "role": "user" , "max_uses": 1 , "expires_in_minutes": 15 } , fiber.StatusCreated , false },
		{ http.MethodPost , "/api/admin/users/{user}/reissue-login" , fiber.Map{ "max_uses": 1 , "expires_in_minutes": 15 } , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/role" , fiber.Map{ "role": "viewer" } , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/expiry" , fiber.Map{ "expires_at": soon } , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/disabled" , fiber.Map{ "disabled": true } , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/disabled" , fiber.Map{ "disabled": false } , fiber.StatusOK , true },
		{ http.MethodGet , "/api/admin/users/{user}/sessions" , nil , fiber.StatusOK , true },
		{ http.MethodGet , "/api/admin/users/{user}/keys" , nil , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/keys" , fiber.Map{ "name": "for the portal" , "role": "user" , "expires_in_days": 30 } , fiber.StatusCreated , true },
		{ http.MethodPost , "/api/admin/keys/{key}/revoke" , fiber.Map{} , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/revoke-access" , fiber.Map{} , fiber.StatusOK , true },
		{ http.MethodPost , "/api/admin/users/{user}/delete" , fiber.Map{} , fiber.StatusOK , true },
	}
	return
}

// portalAccount is the account ProjectPortal works through -- an admin that
// is not the owner -- and its admin key, which may make keys. It is what
// Connect on the owner's API keys screen makes (the portal-account upgrade),
// built here by hand so this file stands on its own in any app.
func portalAccount( t *testing.T , fixture *authFixture ) ( user *models.User , credential string ) {
	t.Helper()
	user , err := models.CreateUser( fixture.store , "ProjectPortal" , models.RoleAdmin )
	if err != nil { t.Fatal( err ) }
	credential , key , err := models.IssueAPIKey( fixture.store , user.ID , "portal" , models.RoleAdmin , 30 * 24 * time.Hour )
	if err != nil { t.Fatal( err ) }
	err = fixture.store.Write( func( tx *db.Tx ) ( tx_err error ) {
		stored := &models.APIKey{}
		if tx_err = tx.Get( db.BucketAPIKeys , []byte( key.ID ) , stored ); tx_err != nil { return }
		stored.MayMintKeys = true
		tx_err = tx.Put( db.BucketAPIKeys , []byte( key.ID ) , stored )
		return
	} )
	if err != nil { t.Fatal( err ) }
	return
}

// target is an account to act on, with one key of its own.
func contractTarget( t *testing.T , fixture *authFixture , user *models.User ) ( paths map[ string ]string ) {
	t.Helper()
	_ , key , err := models.IssueAPIKey( fixture.store , user.ID , "its own" , user.Role , time.Hour )
	if err != nil { t.Fatal( err ) }
	paths = map[ string ]string{ "{user}": idText( user.ID ) , "{key}": key.ID }
	return
}

func contractPath( path string , target map[ string ]string ) ( result string ) {
	result = path
	for name , value := range target { result = strings.ReplaceAll( result , name , value ) }
	return
}

func TestThePortalRunsAnOrdinaryAccount( t *testing.T ) {
	fixture := newAuthFixture( t )
	_ , portal := portalAccount( t , fixture )
	cy , _ := models.CreateUser( fixture.store , "Cyd" , models.RoleUser )
	target := contractTarget( t , fixture , cy )
	for _ , call := range contractCalls() {
		path := contractPath( call.path , target )
		status , reply := fixture.withKey( t , portal , call.method , path , call.body )
		if status != call.status { t.Errorf( "%s %s: %d %v, want %d" , call.method , path , status , reply , call.status ) }
	}
}

func TestAnOrdinaryKeyIsNoPortal( t *testing.T ) {
	fixture := newAuthFixture( t )
	credential , _ , err := models.IssueAPIKey( fixture.store , fixture.alice.ID , "script" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	target := contractTarget( t , fixture , fixture.alice )
	for _ , call := range contractCalls() {
		path := contractPath( call.path , target )
		status , reply := fixture.withKey( t , credential , call.method , path , call.body )
		if status != fiber.StatusForbidden || reply[ "code" ] != apidoc.CodeNotAdmin {
			t.Errorf( "%s %s with a user key: %d %v, want 403 not_admin" , call.method , path , status , reply )
		}
	}
}

func TestAViewerKeyChangesNothing( t *testing.T ) {
	fixture := newAuthFixture( t )
	portal_user , _ := portalAccount( t , fixture )
	credential , _ , err := models.IssueAPIKey( fixture.store , portal_user.ID , "reader" , models.RoleViewer , time.Hour )
	if err != nil { t.Fatal( err ) }
	target := contractTarget( t , fixture , fixture.alice )
	for _ , call := range contractCalls() {
		path := contractPath( call.path , target )
		want := apidoc.CodeViewOnly
		if call.method == http.MethodGet { want = apidoc.CodeNotAdmin }
		status , reply := fixture.withKey( t , credential , call.method , path , call.body )
		if status != fiber.StatusForbidden || reply[ "code" ] != want {
			t.Errorf( "%s %s with a viewer key: %d %v, want 403 %s" , call.method , path , status , reply , want )
		}
	}
}

func TestThePortalLeavesTheOwnerAndOtherAdminsAlone( t *testing.T ) {
	fixture := newAuthFixture( t )
	_ , portal := portalAccount( t , fixture )
	peer , _ := models.CreateUser( fixture.store , "Pia" , models.RoleAdmin )
	for _ , each := range []struct{ user *models.User ; code string }{
		{ fixture.owner , apidoc.CodeOwnerProtected },
		{ peer , apidoc.CodeOwnerRequired },
	} {
		target := contractTarget( t , fixture , each.user )
		for _ , call := range contractCalls() {
			if call.ladder == false { continue }
			path := contractPath( call.path , target )
			status , reply := fixture.withKey( t , portal , call.method , path , call.body )
			if status != fiber.StatusForbidden || reply[ "code" ] != each.code {
				t.Errorf( "%s %s on %s: %d %v, want 403 %s" , call.method , path , each.user.DisplayName , status , reply , each.code )
			}
		}
	}
}

// scripts/contract_check.sh, run against this app: what an owner runs against
// a live one before pasting the key into the portal.
func TestTheContractCheckPassesThePortalsKey( t *testing.T ) {
	if runtime.GOOS == "windows" { t.Skip( "contract_check.sh is for unix shells" ) }
	if _ , err := exec.LookPath( "curl" ); err != nil { t.Skip( "needs curl" ) }
	fixture := newAuthFixture( t )
	_ , portal := portalAccount( t , fixture )
	models.CreateUser( fixture.store , "Cyd" , models.RoleUser )
	listener , err := net.Listen( "tcp" , "127.0.0.1:0" )
	if err != nil { t.Fatal( err ) }
	go fixture.app.Listener( listener , fiber.ListenConfig{ DisableStartupMessage: true } )
	t.Cleanup( func() { fixture.app.Shutdown() } )
	script , err := filepath.Abs( "../../scripts/contract_check.sh" )
	if err != nil { t.Fatal( err ) }
	check := func( key string ) ( output string , ok bool ) {
		command := exec.Command( "bash" , script , "http://" + listener.Addr().String() )
		command.Stdin = strings.NewReader( key + "\n" )
		raw , run_err := command.CombinedOutput()
		output , ok = string( raw ) , run_err == nil
		if strings.Contains( output , key ) { t.Errorf( "the key was printed: %s" , output ) }
		return
	}

	output , ok := check( portal )
	if ok == false || strings.Contains( output , "FAIL" ) || strings.Contains( output , "PASS  keys:" ) == false {
		t.Fatalf( "the portal's key:\n%s" , output )
	}
	if strings.Contains( output , "s devices listed" ) == false { t.Fatalf( "the portal's key reads no devices:\n%s" , output ) }
	t.Log( output )
	user_key , _ , err := models.IssueAPIKey( fixture.store , fixture.alice.ID , "script" , models.RoleUser , time.Hour )
	if err != nil { t.Fatal( err ) }
	if output , ok = check( user_key ); ok || strings.Contains( output , "FAIL  accounts: 403 (not_admin)" ) == false {
		t.Fatalf( "a user key:\n%s" , output )
	}
	if output , ok = check( "nope.nope" ); ok || strings.Contains( output , "FAIL  me: 401 (not_authenticated)" ) == false {
		t.Fatalf( "a bad key:\n%s" , output )
	}
}
