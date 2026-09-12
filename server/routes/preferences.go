// Per-user settings and progress statistics.
package routes

import (
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	corpus "vocabtrainer/server/corpus"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

func ( handlers *Handlers ) GetSettings( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	settings , err := models.GetSettings( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }
	err = c.JSON( fiber.Map{ "settings": settings } )
	return
}

type settingsRequest struct {
	CSRFToken string           `json:"csrf_token"`
	Settings  models.Settings  `json:"settings"`
}

// PostSettings replaces the whole settings record. The model normalises every
// field on the way in, so an out-of-range level or an unknown reveal mode
// becomes the default rather than an error the UI has to explain.
func ( handlers *Handlers ) PostSettings( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	request := settingsRequest{}
	if err = c.Bind().Body( &request ); err != nil { return badRequest( c , "malformed request" ) }
	if handlers.Guard.CheckCSRF( c , request.CSRFToken ) == false { return forbidden( c , "bad csrf token" ) }

	settings := request.Settings
	if err = models.SaveSettings( handlers.Store , user.ID , &settings ); err != nil {
		return serverError( c )
	}
	err = c.JSON( fiber.Map{ "settings": settings } )
	return
}

// GetLevels reports how many words each reading level holds, so the level
// picker can show real numbers. The labels for the levels are not here --
// they are user-facing text and live in language.yaml.
func ( handlers *Handlers ) GetLevels( c fiber.Ctx ) ( err error ) {
	err = c.JSON( fiber.Map{
		"min":    corpus.TierMin,
		"max":    corpus.TierMax,
		"counts": handlers.Corpus.TierCounts(),
		"total":  handlers.Corpus.Size(),
	} )
	return
}

// GetStats backs the progress screen: the three list sizes, the streak, and
// enough daily history to draw a chart.
func ( handlers *Handlers ) GetStats( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	now := time.Now().UTC()
	today := localDate( c.Query( "date" ) , now )

	counts , err := models.CountCards( handlers.Store , user.ID , now )
	if err != nil { return serverError( c ) }

	days , err := models.ListDayStats( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }

	todayStat , err := models.GetDayStat( handlers.Store , user.ID , today )
	if err != nil { return serverError( c ) }

	settings , err := models.GetSettings( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }

	// Only the recent window is sent. The chart shows weeks, not years, and
	// a long-running account would otherwise grow this response without
	// anything on screen changing.
	if len( days ) > 120 { days = days[ len( days )-120: ] }

	parsedToday , parse_err := time.Parse( "2006-01-02" , today )
	if parse_err != nil { parsedToday = now }

	err = c.JSON( fiber.Map{
		"counts":   counts,
		"days":     days,
		"today":    todayStat,
		"streak":   models.Streak( days , parsedToday ),
		"level":    settings.Level,
		"tier_max": corpus.TierMax,
	} )
	return
}

type resetRequest struct {
	CSRFToken string `json:"csrf_token"`
	Confirm   string `json:"confirm"`
}

// PostResetProgress wipes one user's cards and history. It is irreversible,
// so it wants the word "reset" echoed back rather than trusting a button that
// could be hit by accident.
func ( handlers *Handlers ) PostResetProgress( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	request := resetRequest{}
	if err = c.Bind().Body( &request ); err != nil { return badRequest( c , "malformed request" ) }
	if handlers.Guard.CheckCSRF( c , request.CSRFToken ) == false { return forbidden( c , "bad csrf token" ) }
	if request.Confirm != "reset" { return badRequest( c , "confirmation required" ) }

	cards , err := models.ResetProgress( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }
	if _ , err = models.ResetStats( handlers.Store , user.ID ); err != nil { return serverError( c ) }

	err = c.JSON( fiber.Map{ "removed": cards } )
	return
}
