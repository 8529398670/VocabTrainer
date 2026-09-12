// Package security is the only place that decides who a request belongs to
// and whether it is allowed to change anything. Route handlers read the
// answer; they never re-derive it. One implementation means one place to get
// right, and one place to look when auditing.
package security

import (
	fiber "github.com/gofiber/fiber/v3"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
	models "vocabtrainer/server/models"
)

// Typed, unexported keys so nothing else in the process can overwrite what we
// stashed on the request by accident.
type contextKey string

const (
	keyUser    contextKey = "current_user"
	keySession contextKey = "current_session"
)

type Guard struct {
	Store  *db.Store
	Config *config.Config
}

func New( store *db.Store , cfg *config.Config ) ( guard *Guard ) {
	guard = &Guard{ Store: store , Config: cfg }
	return
}

// SetSessionCookie stores the session credential encrypted with the server
// key. The server already checks the credential against the database, so the
// encryption is not what makes the session unforgeable -- it means the cookie
// is opaque to anyone reading it out of a browser profile or a proxy log, and
// that a tampered cookie fails to decrypt instead of reaching the lookup.
func ( guard *Guard ) SetSessionCookie( c fiber.Ctx , credential string ) ( err error ) {
	sealed , err := encryption.ChaChaEncryptString( guard.Config.SecretKey , credential )
	if err != nil { return }
	c.Cookie( &fiber.Cookie{
		Name:     guard.Config.CookieName,
		Value:    sealed,
		Path:     "/",
		MaxAge:   int( guard.Config.SessionTTL.Seconds() ),
		Secure:   guard.Config.SecureCookies,
		HTTPOnly: true,
		SameSite: "Strict",
	} )
	return
}

func ( guard *Guard ) ClearSessionCookie( c fiber.Ctx ) {
	// Overwrite before clearing: some browsers hold on to a cookie whose
	// attributes do not match exactly, and an empty value is harmless either way.
	c.Cookie( &fiber.Cookie{
		Name:     guard.Config.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   guard.Config.SecureCookies,
		HTTPOnly: true,
		SameSite: "Strict",
	} )
	c.ClearCookie( guard.Config.CookieName )
}

// DecryptCookie turns the stored cookie back into a session credential.
// Exposed because logging out has to destroy the server-side record, not just
// drop the cookie -- otherwise a copy of the cookie taken earlier still works.
func ( guard *Guard ) DecryptCookie( raw string ) ( credential string , err error ) {
	credential , err = encryption.ChaChaDecryptBase64String( guard.Config.SecretKey , raw )
	return
}

// LoadUser runs on every request. It resolves the cookie at most once and
// stashes the result, so a handler and a middleware asking "who is this" do
// not cost two database reads. It never rejects anything -- that is
// RequireLogin's job -- because plenty of routes are fine for anonymous
// visitors.
func ( guard *Guard ) LoadUser( c fiber.Ctx ) ( err error ) {
	raw := c.Cookies( guard.Config.CookieName )
	if raw != "" {
		credential , decrypt_err := encryption.ChaChaDecryptBase64String( guard.Config.SecretKey , raw )
		if decrypt_err == nil {
			session , user := models.LoadSession( guard.Store , credential )
			if user != nil {
				c.Locals( keyUser , user )
				c.Locals( keySession , session )
			}
		}
	}
	err = c.Next()
	return
}

// RequireLogin and RequireAdmin are middleware rather than helpers called at
// the top of each handler, so a new protected route is protected by where it
// is registered -- forgetting the check is not something you can do silently.
func ( guard *Guard ) RequireLogin( c fiber.Ctx ) ( err error ) {
	if UserFrom( c ) == nil {
		err = c.Status( fiber.StatusUnauthorized ).JSON( fiber.Map{ "error": "not authenticated" } )
		return
	}
	err = c.Next()
	return
}

func ( guard *Guard ) RequireAdmin( c fiber.Ctx ) ( err error ) {
	user := UserFrom( c )
	if user == nil {
		err = c.Status( fiber.StatusUnauthorized ).JSON( fiber.Map{ "error": "not authenticated" } )
		return
	}
	if user.IsAdmin() == false {
		err = c.Status( fiber.StatusForbidden ).JSON( fiber.Map{ "error": "forbidden" } )
		return
	}
	err = c.Next()
	return
}

func UserFrom( c fiber.Ctx ) ( user *models.User ) {
	user , _ = c.Locals( keyUser ).( *models.User )
	return
}

func SessionFrom( c fiber.Ctx ) ( session *models.Session ) {
	session , _ = c.Locals( keySession ).( *models.Session )
	return
}

// CheckCSRF guards state-changing requests. SameSite=Strict already blocks
// the common cross-site POST, but it is a browser-side promise: older
// browsers, odd cross-scheme cases, and non-browser clients do not all honour
// it. A per-session token echoed back in the JSON body costs nothing and does
// not depend on the browser behaving.
func ( guard *Guard ) CheckCSRF( c fiber.Ctx , submitted string ) ( ok bool ) {
	session := SessionFrom( c )
	if session == nil || submitted == "" { return }
	ok = encryption.ConstantTimeEqual( session.CSRFToken , submitted )
	return
}
