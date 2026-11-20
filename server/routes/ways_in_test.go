package routes

import (
	os "os"
	filepath "path/filepath"
	strconv "strconv"
	strings "strings"
	testing "testing"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	logs "vocabtrainer/server/logs"
)

// Round 3 of the security fixes (S19, S20): who handed out a way into an
// account, and what stays of it once that person loses the right.


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
