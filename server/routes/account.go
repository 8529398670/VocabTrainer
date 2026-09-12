package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

type renameRequest struct {
	DisplayName string `json:"display_name"`
	CSRFToken   string `json:"csrf_token"`
}

// GetMe is the frontend's first call on every page load: it answers "who am
// I, what may I do, and what CSRF token do I echo back". Handing the token
// out here means no page needs a hidden form field or a template variable.
func ( handlers *Handlers ) GetMe( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	session := security.SessionFrom( c )
	err = c.JSON( fiber.Map{
		"authenticated": true,
		"id":            user.ID,
		"display_name":  user.DisplayName,
		"role":          user.Role,
		"csrf_token":    session.CSRFToken,
	} )
	return
}

func ( handlers *Handlers ) RenameAccount( c fiber.Ctx ) ( err error ) {
	var body renameRequest
	if c.Bind().Body( &body ) != nil {
		err = badRequest( c , "malformed request body" )
		return
	}
	if handlers.Guard.CheckCSRF( c , body.CSRFToken ) == false {
		err = forbidden( c , "invalid csrf token" )
		return
	}
	if models.ValidDisplayName( body.DisplayName ) == false {
		err = badRequest( c , "display name must be 1-80 characters" )
		return
	}

	user := security.UserFrom( c )
	if models.RenameUser( handlers.Store , user.ID , body.DisplayName ) != nil {
		err = serverError( c )
		return
	}
	err = c.JSON( fiber.Map{ "ok": true , "display_name": body.DisplayName } )
	return
}

// Logout deletes the session server-side as well as clearing the cookie, so
// a copy of the cookie captured earlier is dead too.
func ( handlers *Handlers ) Logout( c fiber.Ctx ) ( err error ) {
	raw := c.Cookies( handlers.Config.CookieName )
	if raw != "" {
		if credential , decrypt_err := handlers.Guard.DecryptCookie( raw ); decrypt_err == nil {
			models.DestroySession( handlers.Store , credential )
		}
	}
	handlers.Guard.ClearSessionCookie( c )
	err = c.JSON( fiber.Map{ "ok": true } )
	return
}
