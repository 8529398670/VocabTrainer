package routes

import (
	errors "errors"
	fmt "fmt"
	io "io"
	http "net/http"
	os "os"
	filepath "path/filepath"
	regexp "regexp"
	strings "strings"
	atomic "sync/atomic"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	logs "vocabtrainer/server/logs"
	middleware "vocabtrainer/server/middleware"
)

// monitored is the app the way main.go builds it, with the middleware and
// error handler the plain fixtures leave out, plus routes that fail on demand.
type monitored struct {
	*authFixture
	app      *fiber.App
	logDir   string
	logger   *logs.Logger
}

func newMonitored( t *testing.T , logging bool ) ( fixture *monitored ) {
	t.Helper()
	base := newAuthFixture( t )
	dir := t.TempDir()
	logger := logs.New( config.LogsConfig{ Enabled: logging , Dir: dir , RetentionDays: 7 , MaxTotalBytes: 1 << 20 , MaxFileBytes: 1 << 20 } , base.cfg.SecretKey , false )
	logger.Start()
	settings := config.NewStaticHolder( base.cfg )
	handlers := base.handlers
	handlers.Logs = logger

	app := fiber.New( fiber.Config{ ErrorHandler: middleware.NewErrorHandler( nil ) } )
	app.Use( middleware.RequestID() )
	app.Use( middleware.Recover( settings ) )
	app.Use( middleware.RequestLog( logger ) )
	app.Use( checkReplies( t ) )
	app.Get( "/fail" , func( c fiber.Ctx ) ( err error ) {
		err = serverError( c , errors.New( "the disk is on fire" ) )
		return
	} )
	app.Get( "/panic" , func( c fiber.Ctx ) ( err error ) {
		panic( "the wheels came off" )
	} )
	handlers.Register( app )

	fixture = &monitored{ authFixture: base , app: app }
	fixture.logDir , fixture.logger = dir , logger
	base.app = app
	return
}

func ( fixture *monitored ) get( t *testing.T , path string , headers map[ string ]string ) ( response *http.Response , body string ) {
	t.Helper()
	request , _ := http.NewRequest( http.MethodGet , path , nil )
	for name , value := range headers {
		request.Header.Set( name , value )
	}
	response , err := fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
	if err != nil { t.Fatal( err ) }
	raw , _ := io.ReadAll( response.Body )
	body = string( raw )
	return
}


var requestIDShape = regexp.MustCompile( `^[0-9a-f]{16}$` )

func TestEveryResponseCarriesAFreshRequestID( t *testing.T ) {
	fixture := newMonitored( t , false )
	first , _ := fixture.get( t , "/api/health" , map[ string ]string{ "X-Request-ID": "chosen-by-caller" } )
	second , _ := fixture.get( t , "/api/health" , nil )
	one , two := first.Header.Get( "X-Request-ID" ) , second.Header.Get( "X-Request-ID" )
	if requestIDShape.MatchString( one ) == false || requestIDShape.MatchString( two ) == false || one == two {
		t.Fatalf( "%q %q" , one , two )
	}
}

func TestAFailureCanBeMatchedToItsLogLine( t *testing.T ) {
	for path , cause := range map[ string ]string{ "/fail": "the disk is on fire" , "/panic": "the wheels came off" } {
		fixture := newMonitored( t , true )
		response , body := fixture.get( t , path , nil )
		id := response.Header.Get( "X-Request-ID" )
		if response.StatusCode != http.StatusInternalServerError {
			t.Fatalf( "%s: status %d" , path , response.StatusCode )
		}
		if strings.Contains( body , cause ) {
			t.Fatalf( "%s: the cause reached the client: %s" , path , body )
		}
		if strings.Contains( body , fmt.Sprintf( `"request_id":"%s"` , id ) ) == false {
			t.Fatalf( "%s: no reference in the body: %s" , path , body )
		}
		fixture.logger.Close()
		files , _ := filepath.Glob( filepath.Join( fixture.logDir , "*.jsonl" ) )
		matched := false
		for _ , file := range files {
			raw , _ := os.ReadFile( file )
			for _ , line := range strings.Split( string( raw ) , "\n" ) {
				if strings.Contains( line , id ) && strings.Contains( line , cause ) {
					matched = true
				}
			}
		}
		if matched == false {
			t.Fatalf( "%s: no log line has both %s and the cause" , path , id )
		}
	}
}

func TestLogsOffStillGivesIDsAndMetrics( t *testing.T ) {
	fixture := newMonitored( t , false )
	response , body := fixture.get( t , "/fail" , nil )
	if requestIDShape.MatchString( response.Header.Get( "X-Request-ID" ) ) == false || strings.Contains( body , "request_id" ) == false {
		t.Fatalf( "%v %s" , response.Header , body )
	}
}

func TestReadyAnswersOnlyYesOrNo( t *testing.T ) {
	fixture := newMonitored( t , false )
	response , body := fixture.get( t , "/api/ready" , nil )
	if response.StatusCode != http.StatusOK || body != `{"ok":true}` {
		t.Fatalf( "%d %s" , response.StatusCode , body )
	}
	fixture.handlers.readiness.check = func() bool { return false }
	response , body = fixture.get( t , "/api/ready" , nil )
	if response.StatusCode != http.StatusServiceUnavailable || body != `{"ok":false}` {
		t.Fatalf( "%d %s" , response.StatusCode , body )
	}
}

func TestReadySaysNoOnABrokenDatabaseAndShrugsOffAPanic( t *testing.T ) {
	fixture := newMonitored( t , false )
	fixture.store.Close()
	if response , _ := fixture.get( t , "/api/ready" , nil ); response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf( "closed database: %d" , response.StatusCode )
	}
	fixture.handlers.readiness.check = func() bool { panic( "boom" ) }
	if response , _ := fixture.get( t , "/api/ready" , nil ); response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf( "panic: %d" , response.StatusCode )
	}
}

func TestReadyGivesUpOnAStuckDatabaseAndSharesTheProbe( t *testing.T ) {
	fixture := newMonitored( t , false )
	release := make( chan struct{} )
	var calls atomic.Int32
	fixture.handlers.readiness.check = func() bool {
		calls.Add( 1 )
		<-release
		return true
	}
	started := time.Now()
	response , body := fixture.get( t , "/api/ready" , nil )
	if response.StatusCode != http.StatusServiceUnavailable || body != `{"ok":false}` {
		t.Fatalf( "%d %s" , response.StatusCode , body )
	}
	if elapsed := time.Since( started ); elapsed < readyWait || elapsed > readyWait + 2 * time.Second {
		t.Fatalf( "took %v" , elapsed )
	}
	fixture.get( t , "/api/ready" , nil )
	close( release )
	if calls.Load() != 1 {
		t.Fatalf( "%d probes ran at once" , calls.Load() )
	}
}


