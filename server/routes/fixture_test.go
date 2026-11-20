package routes

import (
	json "encoding/json"
	io "io"
	http "net/http"
	filepath "path/filepath"
	strings "strings"
	testing "testing"
	fstest "testing/fstest"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
	static "vocabtrainer/server/static"
)

// What the tests in this package share: an app wired through the real routes,
// and a browser to drive it with. Anything one feature's tests alone use stays
// in that feature's test file, since a project can leave the feature out.

type authFixture struct {
	app   *fiber.App
	store *db.Store
	cfg   *config.Config
	owner *models.User
	alice *models.User
	handlers *Handlers
}

// browser is one cookie jar.
type browser struct {
	fixture *authFixture
	cookie  string
}

func newAuthFixture( t *testing.T ) ( fixture *authFixture ) {
	t.Helper()
	cfg := &config.Config{
		ProjectName:      "Test",
		DatabasePath:     filepath.Join( t.TempDir() , "test.db" ),
		SecretKey:        strings.Repeat( "ab" , 32 ),
		CookieName:       "session",
		SessionTTL:       time.Hour,
	}
	store , err := db.Open( cfg )
	if err != nil { t.Fatal( err ) }
	t.Cleanup( func() { store.Close() } )
	owner , _ := models.CreateUser( store , "Owner" , models.RoleAdmin )
	if _ , err = models.EnsureOwner( store ); err != nil { t.Fatal( err ) }
	alice , _ := models.CreateUser( store , "Alice" , models.RoleUser )

	static_server , _ := static.New( fstest.MapFS{ "index.html": { Data: []byte( "index" ) } } )
	settings := config.NewStaticHolder( cfg )
	handlers := New( store , settings , security.New( store , settings ) , nil , static_server , "test" )
	app := fiber.New()
	app.Use( checkReplies( t ) )
	handlers.Register( app )
	fixture = &authFixture{ app: app , store: store , cfg: cfg , owner: owner , alice: alice , handlers: handlers }
	return
}

func ( fixture *authFixture ) browser() ( result *browser ) {
	result = &browser{ fixture: fixture }
	return
}

func ( client *browser ) do( t *testing.T , method string , path string , body fiber.Map ) ( status int , reply map[ string ]any ) {
	t.Helper()
	raw , _ := json.Marshal( body )
	request , _ := http.NewRequest( method , path , strings.NewReader( string( raw ) ) )
	request.Header.Set( "Content-Type" , "application/json" )
	if client.cookie != "" {
		request.Header.Set( "Cookie" , client.cookie )
	}
	response , err := client.fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
	if err != nil { t.Fatal( err ) }
	for _ , cookie := range response.Cookies() {
		client.cookie = cookie.Name + "=" + cookie.Value
	}
	text , _ := io.ReadAll( response.Body )
	status = response.StatusCode
	json.Unmarshal( text , &reply )
	return
}

func ( client *browser ) post( t *testing.T , path string , body fiber.Map ) ( status int , reply map[ string ]any ) {
	t.Helper()
	status , reply = client.do( t , http.MethodPost , path , body )
	return
}

// send is a POST with the session's csrf token.
func send( t *testing.T , client *browser , path string , body fiber.Map ) ( status int , reply map[ string ]any ) {
	t.Helper()
	body[ "csrf_token" ] = client.csrf( t )
	status , reply = client.post( t , path , body )
	return
}

func ( client *browser ) csrf( t *testing.T ) ( token string ) {
	t.Helper()
	_ , reply := client.do( t , http.MethodGet , "/api/me" , nil )
	token , _ = reply[ "csrf_token" ].( string )
	return
}

func ( client *browser ) signedIn( t *testing.T ) ( result bool ) {
	t.Helper()
	status , _ := client.do( t , http.MethodGet , "/api/me" , nil )
	result = status == 200
	return
}


func ( fixture *authFixture ) link( t *testing.T , user_id uint64 ) ( credential string ) {
	t.Helper()
	credential , err := models.IssueLoginToken( fixture.store , user_id , time.Hour )
	if err != nil { t.Fatal( err ) }
	return
}

func ( fixture *authFixture ) signIn( t *testing.T , user_id uint64 ) ( client *browser ) {
	t.Helper()
	client = fixture.browser()
	if status , reply := client.post( t , "/api/login" , fiber.Map{ "credential": fixture.link( t , user_id ) } ); status != 200 {
		t.Fatalf( "login link: %d %v" , status , reply )
	}
	return
}

func sessionCookie( t *testing.T , store *db.Store , cfg *config.Config , user_id uint64 ) ( cookie string ) {
	t.Helper()
	login , err := models.IssueLoginToken( store , user_id , time.Hour )
	if err != nil { t.Fatal( err ) }
	signed , err := models.RedeemLoginToken( store , login , models.SignInRules{ SessionTTL: time.Hour } )
	if err != nil { t.Fatal( err ) }
	sealed , _ := encryption.ChaChaEncryptString( encryption.SubKey( cfg.SecretKey , encryption.PurposeCookie ) , signed.SessionCredential )
	cookie = cfg.SessionCookieName() + "=" + sealed
	return
}


