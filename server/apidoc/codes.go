package apidoc

import (
	fiber "github.com/gofiber/fiber/v3"
)

// Every 4xx answer is {"error": "<sentence for a person>", "code": "<code>"}
// (portal contract v3): a caller shows error and branches on code, never on
// the words. The list is closed: a new code is a contract change, made in the
// go-webserver and python-webserver skills together.
const (
	CodeNotAuthenticated = "not_authenticated" // 401: no credential, or a bad one
	CodeNotAdmin         = "not_admin"         // the route needs an admin
	CodeViewOnly         = "view_only"         // a viewer writing
	CodeSessionOnly      = "session_only"      // an API key on a browser-only route
	CodeOwnerRequired    = "owner_required"    // the ladder: only the owner does this (to an admin)
	CodeOwnerProtected   = "owner_protected"   // the ladder: nobody does this to the owner
	CodeKeyMintingOff    = "key_minting_off"   // the calling key may not make keys
	CodeServiceAccount   = "service_account"   // a sign-in action on a service account
	CodeNotFound         = "not_found"         // no such account, key, link or route
	CodeInvalid          = "invalid"           // a bad request; field names the field when it is one
	CodeConflict         = "conflict"          // a 409
	CodeRateLimited      = "rate_limited"      // a 429
	CodeForbidden        = "forbidden"         // any other 403: a bad csrf token, a cross-origin call
)

// ErrorCodes is the whole list, in the contract's order.
var ErrorCodes = []string{
	CodeNotAuthenticated , CodeNotAdmin , CodeViewOnly , CodeSessionOnly , CodeOwnerRequired ,
	CodeOwnerProtected , CodeKeyMintingOff , CodeServiceAccount , CodeNotFound , CodeInvalid ,
	CodeConflict , CodeRateLimited , CodeForbidden ,
}

// StatusCode is the code a refusal gets when nothing more exact was said:
// what its status alone tells. Empty for a status that is not a 4xx.
func StatusCode( status int ) ( code string ) {
	switch {
	case status == fiber.StatusUnauthorized:
		code = CodeNotAuthenticated
	case status == fiber.StatusForbidden:
		code = CodeForbidden
	case status == fiber.StatusNotFound:
		code = CodeNotFound
	case status == fiber.StatusConflict:
		code = CodeConflict
	case status == fiber.StatusTooManyRequests:
		code = CodeRateLimited
	case status >= 400 && status < 500:
		code = CodeInvalid
	}
	return
}

// ErrorBody is the body of a refusal: the sentence, and the code (code, or
// StatusCode's when it is empty).
func ErrorBody( status int , message string , code string ) ( body fiber.Map ) {
	if code == "" { code = StatusCode( status ) }
	body = fiber.Map{ "error": message }
	if code != "" { body[ "code" ] = code }
	return
}

// Refuse sends a refusal outside a declared route's handler: a guard, the
// error handler.
func Refuse( c fiber.Ctx , status int , message string , code string ) ( err error ) {
	err = c.Status( status ).JSON( ErrorBody( status , message , code ) )
	return
}
