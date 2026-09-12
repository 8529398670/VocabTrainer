package routes

import (
	strings "strings"

	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
)

// RedeemLoginLink is the whole login flow. Visiting a valid link is the
// login; there is no form, no password, and nothing to phish out of a user
// beyond the link itself, which dies on first use.
//
// The route is registered as /login/* rather than /login/:credential because
// the credential contains a "." and route parameters should not have to care
// about the shape of a secret.
func ( handlers *Handlers ) RedeemLoginLink( c fiber.Ctx ) ( err error ) {
	credential := strings.Trim( c.Params( "*" ) , "/" )
	if credential == "" {
		err = c.Redirect().Status( fiber.StatusSeeOther ).To( "/?login=invalid" )
		return
	}

	user_id , redeem_err := models.RedeemLoginToken( handlers.Store , credential )
	if redeem_err != nil {
		// Expired, already spent, never existed, or wrong secret all land
		// here with the same answer. Telling them apart would let someone
		// probe which links are real.
		err = c.Redirect().Status( fiber.StatusSeeOther ).To( "/?login=invalid" )
		return
	}

	credential_value , _ , session_err := models.CreateSession( handlers.Store , user_id , handlers.Config.SessionTTL )
	if session_err != nil {
		err = serverError( c )
		return
	}
	if cookie_err := handlers.Guard.SetSessionCookie( c , credential_value ); cookie_err != nil {
		err = serverError( c )
		return
	}

	err = c.Redirect().Status( fiber.StatusSeeOther ).To( "/" )
	return
}
