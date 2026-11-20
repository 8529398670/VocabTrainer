package routes

import (
	io "io"
	http "net/http"
	strings "strings"
	testing "testing"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	errorpage "vocabtrainer/server/errorpage"
	language "vocabtrainer/server/language"
)

func testErrorPages( t *testing.T ) ( pages *errorpage.Pages ) {
	t.Helper()
	lang , err := language.Parse( []byte( "error_page:\n  home: \"Home\"\n  maintenance_title: \"Down for maintenance\"\n  maintenance_message: \"Soon.\"\n" ) )
	if err != nil { t.Fatal( err ) }
	pages = errorpage.New( lang )
	return
}


func TestFullMaintenanceLeavesOnlyTheOwnersSession( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	alice := fixture.signIn( t , fixture.alice.ID )
	fixture.cfg.Maintenance = config.MaintenanceFull

	if status , reply := owner.do( t , http.MethodGet , "/api/team" , nil ); status != fiber.StatusOK {
		t.Fatalf( "the owner was refused: %d %v" , status , reply )
	}
	status , reply := alice.do( t , http.MethodGet , "/api/team" , nil )
	if status != fiber.StatusServiceUnavailable || reply[ "maintenance" ] != "full" {
		t.Fatalf( "%d %v" , status , reply )
	}

	// Still able to find out why.
	if status , reply = alice.do( t , http.MethodGet , "/api/me" , nil ); status != fiber.StatusOK || reply[ "maintenance" ] != "full" {
		t.Fatalf( "%d %v" , status , reply )
	}
}

func TestReadOnlyMaintenanceRefusesWritesButNotReads( t *testing.T ) {
	fixture := newAuthFixture( t )
	owner := fixture.signIn( t , fixture.owner.ID )
	alice := fixture.signIn( t , fixture.alice.ID )
	fixture.cfg.Maintenance = config.MaintenanceReadOnly

	if status , reply := alice.do( t , http.MethodGet , "/api/team" , nil ); status != fiber.StatusOK {
		t.Fatalf( "%d %v" , status , reply )
	}
	if status , reply := send( t , alice , "/api/account/rename" , fiber.Map{ "display_name": "Al" } ); status != fiber.StatusServiceUnavailable {
		t.Fatalf( "%d %v" , status , reply )
	}
	if status , reply := send( t , owner , "/api/account/rename" , fiber.Map{ "display_name": "Boss" } ); status != fiber.StatusOK {
		t.Fatalf( "the owner was refused: %d %v" , status , reply )
	}
}

func TestMaintenanceAppliesWithoutARestart( t *testing.T ) {
	fixture := newAuthFixture( t )
	alice := fixture.signIn( t , fixture.alice.ID )
	for _ , step := range []struct {
		mode   string
		status int
	}{
		{ config.MaintenanceNone , fiber.StatusOK },
		{ config.MaintenanceFull , fiber.StatusServiceUnavailable },
		{ config.MaintenanceNone , fiber.StatusOK },
	} {
		fixture.cfg.Maintenance = step.mode
		if status , _ := alice.do( t , http.MethodGet , "/api/team" , nil ); status != step.status {
			t.Fatalf( "%s: status %d" , step.mode , status )
		}
	}
}

func TestSigningInStillWorksDuringMaintenance( t *testing.T ) {
	fixture := newAuthFixture( t )
	fixture.cfg.Maintenance = config.MaintenanceFull
	owner := fixture.signIn( t , fixture.owner.ID )
	if status , reply := owner.do( t , http.MethodGet , "/api/team" , nil ); status != fiber.StatusOK {
		t.Fatalf( "%d %v" , status , reply )
	}
}

func TestABrowserGetsThePageNotJSON( t *testing.T ) {
	fixture := newAuthFixture( t )
	fixture.cfg.Maintenance = config.MaintenanceFull
	fixture.handlers.ErrorPages = testErrorPages( t )
	request , _ := http.NewRequest( http.MethodGet , "/" , nil )
	request.Header.Set( "Accept" , "text/html" )
	response , err := fixture.app.Test( request , fiber.TestConfig{ Timeout: 10 * time.Second } )
	if err != nil { t.Fatal( err ) }
	raw , _ := io.ReadAll( response.Body )
	if response.StatusCode != fiber.StatusServiceUnavailable || strings.Contains( string( raw ) , "Down for maintenance" ) == false {
		t.Fatalf( "%d %s" , response.StatusCode , raw )
	}
}
