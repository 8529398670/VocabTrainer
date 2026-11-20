package routes

import (
	json "encoding/json"
	io "io"
	http "net/http"
	os "os"
	filepath "path/filepath"
	strings "strings"
	testing "testing"
	fstest "testing/fstest"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
	static "vocabtrainer/server/static"
)

// The Settings screen's routes, wired the way main.go wires them, over a
// real config.yaml and secret.key in a temporary folder.

type settingsFixture struct {
	app      *fiber.App
	settings *config.Holder
	path     string
	owner    string // a session cookie
	csrf     string
	admin    string // another admin's session cookie
	key      string // the owner's admin API key
}

func useDefaults( t *testing.T ) {
	t.Helper()
	defaults , err := os.ReadFile( "../../config/defaults.yaml" )
	if err != nil { t.Fatal( err ) }
	header , err := os.ReadFile( "../../config/example_header.yaml" )
	if err != nil { t.Fatal( err ) }
	config.UseEmbedded( defaults , header )
	// A project's defaults.yaml says which features it chose. These tests are
	// about the features themselves, so they run on the template's choice
	// whatever that was.
	pinned , _ , err := config.EditChanges( defaults , map[string]any{
		"auth.api_keys": true ,
	} , "defaults.yaml" , false )
	if err != nil { t.Fatal( err ) }
	config.UseEmbedded( pinned , header )
}

func newSettingsFixture( t *testing.T ) ( fixture *settingsFixture ) {
	t.Helper()
	useDefaults( t )
	dir := t.TempDir()
	path := filepath.Join( dir , "config.yaml" )
	if _ , err := config.WriteNew( path ); err != nil { t.Fatal( err ) }
	// Keep the app directory in the test's folder, not ~/.config.
	raw , _ := os.ReadFile( path )
	raw = []byte( strings.Replace( string( raw ) , `app_dir: ""` , `app_dir: "`+filepath.Join( dir , "data" )+`"` , 1 ) )
	os.WriteFile( path , raw , 0o600 )

	cfg , err := config.LoadPath( path , true )
	if err != nil { t.Fatal( err ) }
	store , err := db.Open( cfg )
	if err != nil { t.Fatal( err ) }
	t.Cleanup( func() { store.Close() } )

	owner , _ := models.CreateUser( store , "Owner" , models.RoleAdmin )
	if _ , err = models.EnsureOwner( store ); err != nil { t.Fatal( err ) }
	other , _ := models.CreateUser( store , "Other admin" , models.RoleAdmin )
	key , _ , err := models.IssueAPIKey( store , owner.ID , "admin" , models.RoleAdmin , time.Hour )
	if err != nil { t.Fatal( err ) }

	settings := config.NewHolder( cfg )
	static_server , _ := static.New( fstest.MapFS{ "index.html": { Data: []byte( "index" ) } } )
	handlers := New( store , settings , security.New( store , settings ) , nil , static_server , "test" )
	app := fiber.New()
	app.Use( checkReplies( t ) )
	handlers.Register( app )

	fixture = &settingsFixture{ app: app , settings: settings , path: path }
	fixture.key = key
	fixture.owner = sessionCookie( t , store , cfg , owner.ID )
	fixture.admin = sessionCookie( t , store , cfg , other.ID )
	_ , me := fixture.request( t , http.MethodGet , "/api/me" , fixture.owner , "" , "" )
	fixture.csrf , _ = me[ "csrf_token" ].( string )
	return
}

func ( fixture *settingsFixture ) request( t *testing.T , method string , path string , cookie string , key string , body string ) ( status int , reply map[string]any ) {
	t.Helper()
	request , _ := http.NewRequest( method , path , strings.NewReader( body ) )
	request.Header.Set( "Content-Type" , "application/json" )
	if cookie != "" {
		request.Header.Set( "Cookie" , cookie )
	}
	if key != "" {
		request.Header.Set( "Authorization" , "Bearer "+key )
	}
	response , err := fixture.app.Test( request )
	if err != nil { t.Fatal( err ) }
	raw , _ := io.ReadAll( response.Body )
	status = response.StatusCode
	json.Unmarshal( raw , &reply )
	if reply == nil {
		reply = map[string]any{ "raw": string( raw ) }
	}
	return
}

func TestSettingsAreTheOwnersAlone( t *testing.T ) {
	fixture := newSettingsFixture( t )
	if status , _ := fixture.request( t , http.MethodGet , "/api/owner/settings" , fixture.admin , "" , "" ); status != 404 {
		t.Errorf( "another admin: status %d, want 404" , status )
	}
	if status , _ := fixture.request( t , http.MethodGet , "/api/owner/settings" , "" , fixture.key , "" ); status != 404 {
		t.Errorf( "the owner's API key: status %d, want 404" , status )
	}
	status , reply := fixture.request( t , http.MethodGet , "/api/owner/settings" , fixture.owner , "" , "" )
	if status != 200 {
		t.Fatalf( "owner: status %d" , status )
	}
	encoded , _ := json.Marshal( reply )
	if strings.Contains( string( encoded ) , fixture.settings.Get().SecretKey ) {
		t.Fatal( "the settings response contains the secret key" )
	}
}

func TestSaveSettings( t *testing.T ) {
	fixture := newSettingsFixture( t )
	_ , snapshot := fixture.request( t , http.MethodGet , "/api/owner/settings" , fixture.owner , "" , "" )
	version , _ := snapshot[ "version" ].( string )

	body := `{"version":"` + version + `","changes":{"server.rate_limit_max":7}}`
	if status , _ := fixture.request( t , http.MethodPost , "/api/owner/settings" , fixture.owner , "" , body ); status != 403 {
		t.Errorf( "no csrf token: status %d, want 403" , status )
	}

	body = `{"csrf_token":"` + fixture.csrf + `","version":"` + version + `","changes":{"server.rate_limit_max":7}}`
	status , reply := fixture.request( t , http.MethodPost , "/api/owner/settings" , fixture.owner , "" , body )
	if status != 200 || fixture.settings.Get().RateLimitMax != 7 {
		t.Fatalf( "save: status %d %v, rate_limit_max %d" , status , reply , fixture.settings.Get().RateLimitMax )
	}

	// The same version again is now out of date.
	status , reply = fixture.request( t , http.MethodPost , "/api/owner/settings" , fixture.owner , "" , body )
	if status != 409 || reply[ "error" ] != "settings_changed" || reply[ "version" ] == version {
		t.Fatalf( "stale version: status %d %v" , status , reply )
	}

	body = `{"csrf_token":"` + fixture.csrf + `","version":"` + reply[ "version" ].( string ) + `","changes":{"server.port":0}}`
	status , reply = fixture.request( t , http.MethodPost , "/api/owner/settings" , fixture.owner , "" , body )
	if status != 400 || reply[ "error" ] != "invalid_settings" {
		t.Fatalf( "invalid value: status %d %v" , status , reply )
	}
}

// S2: a path is never set from Settings, inside a container or out. Before,
// the owner's screen could point static_dir at the config folder, and the
// static handler then served secret.key to anyone.
func TestSettingsNeverChangeAPath( t *testing.T ) {
	fixture := newSettingsFixture( t )
	for _ , key := range []string{ "server.static_dir" , "server.language_file" , "server.app_dir" , "docker.data_dir" , "build.output_dir" } {
		_ , snapshot := fixture.request( t , http.MethodGet , "/api/owner/settings" , fixture.owner , "" , "" )
		version , _ := snapshot[ "version" ].( string )
		body := `{"csrf_token":"` + fixture.csrf + `","version":"` + version + `","changes":{"` + key + `":"/s2-probe"}}`
		status , reply := fixture.request( t , http.MethodPost , "/api/owner/settings" , fixture.owner , "" , body )
		if status != 403 || reply[ "error" ] != "settings_file_only" {
			t.Fatalf( "%s: status %d %v" , key , status , reply )
		}
	}
	raw , _ := os.ReadFile( fixture.path )
	if strings.Contains( string( raw ) , "s2-probe" ) { t.Fatalf( "config.yaml was changed:\n%s" , raw ) }
	// The screen shows them as fixed.
	_ , snapshot := fixture.request( t , http.MethodGet , "/api/owner/settings" , fixture.owner , "" , "" )
	encoded , _ := json.Marshal( snapshot )
	if strings.Contains( string( encoded ) , `"file_only":true` ) == false { t.Fatalf( "no key is marked file_only: %s" , encoded ) }
}

