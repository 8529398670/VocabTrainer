package routes

import (
	http "net/http"
	os "os"
	filepath "path/filepath"
	strconv "strconv"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	logs "vocabtrainer/server/logs"
	models "vocabtrainer/server/models"
)

// Round 3 of the security fixes (S19, S20): who handed out a way into an
// account, and what stays of it once that person loses the right.

// S20: disabling an admin, or taking their admin role away, ends what their
// keys made: links not yet used stop working, and browsers signed in through
// them are signed out. Before, both outlived the admin's right to make them.
func TestLosingTheRightEndsWhatTheKeysMade( t *testing.T ) {
	for _ , how := range []string{ "disable" , "demote" } {
		fixture := newAuthFixture( t )
		bob , err := models.CreateUser( fixture.store , "Bob" , models.RoleAdmin )
		if err != nil { t.Fatal( err ) }
		key , _ , err := models.IssueAPIKey( fixture.store , bob.ID , "ops" , models.RoleAdmin , time.Hour )
		if err != nil { t.Fatal( err ) }
		status , reply := fixture.withKey( t , key , http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Carol" , "role": "user" , "max_uses": 0 , "expires_in_minutes": 0 } )
		if status != fiber.StatusCreated { t.Fatalf( "%s: create: %d %v" , how , status , reply ) }
		credential := strings.TrimPrefix( reply[ "login_path" ].( string ) , "/login/" )
		early := fixture.browser()
		if status , _ := early.post( t , "/api/login" , fiber.Map{ "credential": credential } ); status != fiber.StatusOK { t.Fatalf( "%s: sign-in: %d" , how , status ) }
		// Signed in from a shell's link, before Bob's key replaces it: nobody's
		// key made this one.
		person := fixture.signIn( t , fixture.alice.ID )
		// The same for a link Bob's key reissued for someone who already had an
		// account (the confirmed case).
		status , reply = fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "reissue-login" ) , fiber.Map{ "max_uses": 0 , "expires_in_minutes": 0 } )
		if status != fiber.StatusOK { t.Fatalf( "%s: reissue: %d %v" , how , status , reply ) }
		reissued := strings.TrimPrefix( reply[ "login_path" ].( string ) , "/login/" )
		as_alice := fixture.browser()
		if status , _ := as_alice.post( t , "/api/login" , fiber.Map{ "credential": reissued } ); status != fiber.StatusOK { t.Fatalf( "%s: sign-in as Alice: %d" , how , status ) }

		owner := fixture.signIn( t , fixture.owner.ID )
		if how == "disable" {
			status , reply = send( t , owner , userPath( bob , "disabled" ) , fiber.Map{ "disabled": true } )
		} else {
			status , reply = send( t , owner , userPath( bob , "role" ) , fiber.Map{ "role": "user" } )
		}
		if status != fiber.StatusOK { t.Fatalf( "%s: %d %v" , how , status , reply ) }
		if early.signedIn( t ) { t.Fatalf( "%s: the browser Bob's key signed in is still signed in" , how ) }
		if late := fixture.browser(); func() bool { status , _ := late.post( t , "/api/login" , fiber.Map{ "credential": credential } ); return status == fiber.StatusOK }() {
			t.Fatalf( "%s: the link Bob's key made still signs in" , how )
		}
		if as_alice.signedIn( t ) { t.Fatalf( "%s: the browser signed in as Alice through Bob's key is still signed in" , how ) }
		if status , _ := fixture.browser().post( t , "/api/login" , fiber.Map{ "credential": reissued } ); status == fiber.StatusOK { t.Fatalf( "%s: the link Bob's key reissued still signs in" , how ) }
		if person.signedIn( t ) == false { t.Fatalf( "%s: a session nobody's key made was signed out" , how ) }
	}
}

// S22: revoking an account's access, or deleting it, ends what its keys made
// in other accounts too, as disabling does (S20). Before, both stopped the
// keys themselves but left their links and the browsers those signed in.
func TestRevokingOrDeletingEndsWhatTheKeysMade( t *testing.T ) {
	for _ , how := range []string{ "revoke-access" , "delete" } {
		fixture := newAuthFixture( t )
		bob , err := models.CreateUser( fixture.store , "Bob" , models.RoleAdmin )
		if err != nil { t.Fatal( err ) }
		key , _ , err := models.IssueAPIKey( fixture.store , bob.ID , "ops" , models.RoleAdmin , time.Hour )
		if err != nil { t.Fatal( err ) }
		status , reply := fixture.withKey( t , key , http.MethodPost , "/api/admin/users" , fiber.Map{ "display_name": "Carol" , "role": "user" , "max_uses": 0 , "expires_in_minutes": 0 } )
		if status != fiber.StatusCreated { t.Fatalf( "%s: create: %d %v" , how , status , reply ) }
		credential := strings.TrimPrefix( reply[ "login_path" ].( string ) , "/login/" )
		as_carol := fixture.browser()
		if status , _ := as_carol.post( t , "/api/login" , fiber.Map{ "credential": credential } ); status != fiber.StatusOK { t.Fatalf( "%s: sign-in as Carol: %d" , how , status ) }
		person := fixture.signIn( t , fixture.alice.ID )
		status , reply = fixture.withKey( t , key , http.MethodPost , userPath( fixture.alice , "reissue-login" ) , fiber.Map{ "max_uses": 0 , "expires_in_minutes": 0 } )
		if status != fiber.StatusOK { t.Fatalf( "%s: reissue: %d %v" , how , status , reply ) }
		reissued := strings.TrimPrefix( reply[ "login_path" ].( string ) , "/login/" )
		as_alice := fixture.browser()
		if status , _ := as_alice.post( t , "/api/login" , fiber.Map{ "credential": reissued } ); status != fiber.StatusOK { t.Fatalf( "%s: sign-in as Alice: %d" , how , status ) }

		owner := fixture.signIn( t , fixture.owner.ID )
		status , reply = send( t , owner , userPath( bob , how ) , fiber.Map{} )
		if status != fiber.StatusOK { t.Fatalf( "%s: %d %v" , how , status , reply ) }
		if as_carol.signedIn( t ) { t.Fatalf( "%s: the browser Bob's key signed in as Carol is still signed in" , how ) }
		if status , _ := fixture.browser().post( t , "/api/login" , fiber.Map{ "credential": credential } ); status == fiber.StatusOK { t.Fatalf( "%s: the link Bob's key made still signs in" , how ) }
		if as_alice.signedIn( t ) { t.Fatalf( "%s: the browser Bob's key signed in as Alice is still signed in" , how ) }
		if status , _ := fixture.browser().post( t , "/api/login" , fiber.Map{ "credential": reissued } ); status == fiber.StatusOK { t.Fatalf( "%s: the link Bob's key reissued still signs in" , how ) }
		if person.signedIn( t ) == false { t.Fatalf( "%s: a session nobody's key made was signed out" , how ) }
	}
}

// S19: an admin can sign in as anyone below them, by resetting two-factor
// and reissuing a link. That is how links work, so it is made visible: the
// session records who issued its link, and the account's own log says so.
func TestSigningInThroughAnAdminsLinkIsLogged( t *testing.T ) {
	fixture := newAuthFixture( t )
	dir := t.TempDir()
	logger := logs.New( config.LogsConfig{ Enabled: true , Dir: dir , RetentionDays: 7 , MaxTotalBytes: 1 << 20 , MaxFileBytes: 1 << 20 } , fixture.cfg.SecretKey , false )
	logger.Start()
	fixture.handlers.Logs = logger

	owner := fixture.signIn( t , fixture.owner.ID )
	status , reply := send( t , owner , userPath( fixture.alice , "reissue-login" ) , fiber.Map{} )
	if status != fiber.StatusOK { t.Fatalf( "reissue: %d %v" , status , reply ) }
	if status , _ := fixture.browser().post( t , "/api/login" , fiber.Map{ "credential": strings.TrimPrefix( reply[ "login_path" ].( string ) , "/login/" ) } ); status != fiber.StatusOK { t.Fatalf( "the reissued link did not sign in: %d" , status ) }
	shell := fixture.signIn( t , fixture.alice.ID ) // a link from a shell names nobody
	if shell.signedIn( t ) == false { t.Fatal( "the shell's link did not sign in" ) }
	logger.Close()

	issued , plain , coded := 0 , 0 , 0
	files , _ := filepath.Glob( filepath.Join( dir , "*.jsonl" ) )
	for _ , file := range files {
		raw , _ := os.ReadFile( file )
		for _ , line := range strings.Split( string( raw ) , "\n" ) {
			if strings.Contains( line , `"user_id":`+strconv.FormatUint( fixture.alice.ID , 10 ) ) == false { continue }
			switch {
			case strings.Contains( line , "signed in with a link issued by Owner" ) && strings.Contains( line , `"issued_by":`+strconv.FormatUint( fixture.owner.ID , 10 ) ):
				issued++
			case strings.Contains( line , `"signed in with a login link"` ):
				plain++
			case strings.Contains( line , "signed in with a two-factor code, through a link issued by Owner" ):
				coded++
			}
		}
	}
	if issued != 1 || plain != 1 { t.Fatalf( "issued-by entries %d, plain %d, want 1 and 1" , issued , plain ) }
}
