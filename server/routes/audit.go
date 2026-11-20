package routes

import (
	fiber "github.com/gofiber/fiber/v3"

	logs "vocabtrainer/server/logs"
	middleware "vocabtrainer/server/middleware"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

// What a route says when it does something worth keeping a record of. Every
// route calls these, so they stay in a project without the activity log (see
// server/logs), where they do nothing.

// logUser is the Actor for an account named by id, for an entry made before
// the request has an actor of its own (a sign-in).
func ( handlers *Handlers ) logUser( user_id uint64 , method string ) ( actor *logs.Actor ) {
	actor = &logs.Actor{ UserID: user_id , Method: method }
	if user , err := models.GetUser( handlers.Store , user_id ); err == nil {
		actor.Name = user.DisplayName
		actor.Role = user.Role
	}
	return
}

// audit logs an auth or admin action by whoever made this request. detail
// says what it touched: ids and names, never a credential.
func ( handlers *Handlers ) audit( c fiber.Ctx , kind string , message string , detail fiber.Map ) {
	handlers.auditAs( c , security.LogActor( security.ActorFrom( c ) ) , kind , logs.LevelInfo , message , detail )
}

// auditAs is audit for a request whose actor is not the signed-in one: a
// sign-in (nobody yet), or a failed one.
func ( handlers *Handlers ) auditAs( c fiber.Ctx , actor *logs.Actor , kind string , level string , message string , detail fiber.Map ) {
	if handlers.Logs.Enabled() == false { return }
	handlers.Logs.Log( logs.Entry{
		Kind:   kind,
		Level:  level,
		RequestID: middleware.RequestIDFrom( c ),
		Actor:  actor,
		IP:     c.IP(),
		UA:     logs.Truncate( c.Get( fiber.HeaderUserAgent ) , 300 ),
		Msg:    message,
		Detail: detail,
	} )
}
