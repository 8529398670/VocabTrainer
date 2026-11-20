package logs

import (
	url "net/url"
	strings "strings"
	utf8 "unicode/utf8"
)

// What keeps a credential out of anything written down. The error handler needs
// this with or without the activity log.

// The path prefixes whose next segment is a credential: a login link, an
// invite link. Opening one is logged; the credential is not. Matched without
// regard to case, because the router routes /LOGIN/<credential> to the same
// page as /login/<credential>.
var secretPathPrefixes = []string{ "/login/" , "/join/" }

// Query parameters whose values are never written, whatever route they are on.
var secretQueryKeys = map[string]bool{
	"token": true , "key": true , "secret": true , "code": true ,
	"credential": true , "csrf_token": true , "pin": true , "password": true ,
	"password_digest": true , "challenge": true , "recovery_code": true ,
}

const maxPathLength = 300

// Redact makes a request path safe to keep: the credential in a login or
// invite link becomes "…", secret-looking query values become "…", and the
// whole is capped so a pathological URL cannot bloat a line.
func Redact( path string , raw_query string ) ( result string ) {
	result = path
	lowered := strings.ToLower( result )
	for _ , prefix := range secretPathPrefixes {
		if strings.HasPrefix( lowered , prefix ) && len( result ) > len( prefix ) {
			result = result[ :len( prefix ) ] + "…"
			break
		}
	}
	if raw_query != "" {
		values , err := url.ParseQuery( raw_query )
		if err != nil {
			result += "?…"
		} else {
			for name := range values {
				if secretQueryKeys[ strings.ToLower( name ) ] {
					values.Set( name , "…" )
				}
			}
			result += "?" + values.Encode()
		}
	}
	if len( result ) > maxPathLength {
		result = result[ :maxPathLength ] + "…"
	}
	return
}

// Truncate caps a string from a browser or a header at max bytes, on a rune
// boundary.
func Truncate( value string , max int ) ( result string ) {
	result = value
	if len( result ) <= max { return }
	result = result[ :max ]
	for len( result ) > 0 && utf8.ValidString( result ) == false {
		result = result[ :len( result ) - 1 ]
	}
	return
}
