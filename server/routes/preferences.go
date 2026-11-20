// Per-user settings and progress statistics.
package routes

import (
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	apidoc "vocabtrainer/server/apidoc"
	corpus "vocabtrainer/server/corpus"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

type settingsReply struct {
	Settings *models.Settings `json:"settings"`
}

// getTrainingSettings is this account's training settings.
//
// @route("GET /api/settings", summary="Your training settings",
//     tag="Training",
// )
func ( handlers *Handlers ) getTrainingSettings( c fiber.Ctx , in *apidoc.None ) ( reply *settingsReply , err error ) {
	user := security.UserFrom( c )
	settings , err := models.GetSettings( handlers.Store , user.ID )
	if err != nil { return }
	reply = &settingsReply{ Settings: settings }
	return
}

// settingsInput is what a person can change: models.Settings less its
// schema and updated_at, which the server keeps. Every field is optional, as
// it always was: one left out comes back as its default (see SaveSettings).
type settingsInput struct {
	Level            int    `json:"level,omitempty"`
	RevealMode       string `json:"reveal_mode,omitempty"`
	SwipeKnown       string `json:"swipe_known,omitempty"`
	SwipeUnknown     string `json:"swipe_unknown,omitempty"`
	SwipeSkip        string `json:"swipe_skip,omitempty"`
	PracticeMode     string `json:"practice_mode,omitempty"`
	ScoringMode      string `json:"scoring_mode,omitempty"`
	RevealHoldMs     int    `json:"reveal_hold_ms,omitempty"`
	KnownSkipsReveal bool   `json:"known_skips_reveal,omitempty"`
	SpeechVoice      string `json:"speech_voice,omitempty"`
	AutoAdvanceMs    int    `json:"auto_advance_ms,omitempty"`
	DailyNewLimit    int    `json:"daily_new_limit,omitempty"`
	BatchSize        int    `json:"batch_size,omitempty"`
	ShowExamples     bool   `json:"show_examples,omitempty"`
	Haptics          bool   `json:"haptics,omitempty"`
	HideLevelBadge   bool   `json:"hide_level_badge,omitempty"`
	Theme            string `json:"theme,omitempty"`
	// Sent back by a page that saves what it was given. Ignored.
	Schema    int        `json:"schema,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

func ( settingsInput ) APIName() ( name string ) { name = "TrainingSettingsInput"; return }

type settingsRequest struct {
	Settings settingsInput `json:"settings"`
}

// postTrainingSettings replaces the whole settings record. The model normalises every
// field on the way in, so an out-of-range level or an unknown reveal mode
// becomes the default rather than an error the UI has to explain.
//
// @route("POST /api/settings", summary="Save your training settings",
//     tag="Training",
//     description="Replaces the whole record. A value out of range becomes its default rather than an error.",
// )
func ( handlers *Handlers ) postTrainingSettings( c fiber.Ctx , in *settingsRequest ) ( reply *settingsReply , err error ) {
	user := security.UserFrom( c )
	given := in.Settings
	settings := models.Settings{
		Level:            given.Level,
		RevealMode:       given.RevealMode,
		SwipeKnown:       given.SwipeKnown,
		SwipeUnknown:     given.SwipeUnknown,
		SwipeSkip:        given.SwipeSkip,
		PracticeMode:     given.PracticeMode,
		ScoringMode:      given.ScoringMode,
		RevealHoldMs:     given.RevealHoldMs,
		KnownSkipsReveal: given.KnownSkipsReveal,
		SpeechVoice:      given.SpeechVoice,
		AutoAdvanceMs:    given.AutoAdvanceMs,
		DailyNewLimit:    given.DailyNewLimit,
		BatchSize:        given.BatchSize,
		ShowExamples:     given.ShowExamples,
		Haptics:          given.Haptics,
		HideLevelBadge:   given.HideLevelBadge,
		Theme:            given.Theme,
	}
	if err = models.SaveSettings( handlers.Store , user.ID , &settings ); err != nil { return }
	reply = &settingsReply{ Settings: &settings }
	return
}

type levelsReply struct {
	Min    int         `json:"min"`
	Max    int         `json:"max"`
	Counts []int `json:"counts" jsonschema_description:"Words at each reading level, from min to max."`
	Total  int         `json:"total"`
}

// getLevels reports how many words each reading level holds, so the level
// picker can show real numbers. The labels for the levels are not here --
// they are user-facing text and live in language.yaml.
//
// @route("GET /api/levels", summary="The reading levels and their sizes",
//     tag="Training",
// )
func ( handlers *Handlers ) getLevels( c fiber.Ctx , in *apidoc.None ) ( reply *levelsReply , err error ) {
	reply = &levelsReply{
		Min:    corpus.TierMin,
		Max:    corpus.TierMax,
		Counts: handlers.Corpus.TierCounts(),
		Total:  handlers.Corpus.Size(),
	}
	return
}

type statsReply struct {
	Counts  models.Counts     `json:"counts"`
	Days    []*models.DayStat `json:"days"`
	Today   *models.DayStat   `json:"today"`
	Streak  int               `json:"streak"`
	Level   int               `json:"level"`
	TierMax int               `json:"tier_max"`
}

// getStats backs the progress screen: the three list sizes, the streak, and
// enough daily history to draw a chart.
//
// @route("GET /api/stats", summary="Your progress",
//     tag="Training",
//     description="The three list sizes, the streak, and up to 120 days of history.",
// )
func ( handlers *Handlers ) getStats( c fiber.Ctx , in *localDay ) ( reply *statsReply , err error ) {
	user := security.UserFrom( c )
	now := time.Now().UTC()
	today := localDate( in.Date , now )

	counts , err := models.CountCards( handlers.Store , user.ID , now )
	if err != nil { return }
	days , err := models.ListDayStats( handlers.Store , user.ID )
	if err != nil { return }
	today_stat , err := models.GetDayStat( handlers.Store , user.ID , today )
	if err != nil { return }
	settings , err := models.GetSettings( handlers.Store , user.ID )
	if err != nil { return }

	// Only the recent window is sent. The chart shows weeks, not years, and
	// a long-running account would otherwise grow this response without
	// anything on screen changing.
	if len( days ) > 120 { days = days[ len( days )-120: ] }

	parsed_today , parse_err := time.Parse( "2006-01-02" , today )
	if parse_err != nil { parsed_today = now }

	reply = &statsReply{
		Counts:  counts,
		Days:    days,
		Today:   today_stat,
		Streak:  models.Streak( days , parsed_today ),
		Level:   settings.Level,
		TierMax: corpus.TierMax,
	}
	return
}

type resetRequest struct {
	Confirm string `json:"confirm" jsonschema:"enum=reset" jsonschema_description:"The word reset, so a stray tap cannot do this." error:"confirmation required"`
}

type resetReply struct {
	Removed int `json:"removed"`
}

// postResetProgress wipes one user's cards and history. It is irreversible,
// so it wants the word "reset" echoed back rather than trusting a button that
// could be hit by accident.
//
// @route("POST /api/progress/reset", summary="Wipe your progress",
//     tag="Training",
//     description="Every card and every day of history for this account. Cannot be undone.",
// )
func ( handlers *Handlers ) postResetProgress( c fiber.Ctx , in *resetRequest ) ( reply *resetReply , err error ) {
	user := security.UserFrom( c )
	cards , err := models.ResetProgress( handlers.Store , user.ID )
	if err != nil { return }
	if _ , err = models.ResetStats( handlers.Store , user.ID ); err != nil { return }
	reply = &resetReply{ Removed: cards }
	return
}
