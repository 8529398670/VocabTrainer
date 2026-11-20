package middleware

import (
	strings "strings"
	testing "testing"

	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
)

// S21: the paths the request log leaves out are matched in any case and with
// a trailing slash, the way the rate limiter reads them (normalisedPath).
func TestSkippedPathsIgnoreCase( t *testing.T ) {
	settings := config.LogsConfig{ StaticAssets: true }
	for _ , path := range []string{ "/api/health" , "/API/Health" , "/api/ready/" , "/Metrics" , "/api/LOGS/events" , "/API/owner/logs/days" } {
		if skipRequestLog( fiber.MethodGet , path , settings ) == false { t.Errorf( "%s was logged" , path ) }
	}
	for _ , path := range []string{ "/api/me" , "/api/healthy" , "/" } {
		if skipRequestLog( fiber.MethodGet , path , settings ) { t.Errorf( "%s was left out" , path ) }
	}
}

// S6: a Referer naming a login or invite link in any case keeps the
// credential out of the log, as the path itself does.
func TestRefererRedactionIgnoresCase( t *testing.T ) {
	for _ , referer := range []string{ "https://example.com/LOGIN/abc.secret" , "https://example.com/Join/abc.secret" , "https://example.com/login/abc.secret" } {
		if got := redactReferer( referer ); strings.Contains( got , "secret" ) {
			t.Errorf( "%s logged as %s" , referer , got )
		}
	}
}
