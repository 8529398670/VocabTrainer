package models

import (
	binary "encoding/binary"
	time "time"

	corpus "vocabtrainer/server/corpus"
	db "vocabtrainer/server/db"
)

// Reveal modes. Which side of the card starts face down is the setting people
// asked for most directly, so it is the first field.
const (
	// RevealDefinition shows the word and hides its meaning until tapped.
	// This is the default: it is the direction that tests recall of meaning,
	// which is what reading comprehension needs.
	RevealDefinition = "definition_hidden"

	// RevealWord shows the meaning and hides the word. Harder, and the
	// direction that helps with writing and recall of the word itself.
	RevealWord = "word_hidden"
)

// The four swipe directions. Which one means what is the user's choice rather
// than this package's -- see SwipeKnown -- so these exist as the shared
// vocabulary between the browser and the scheduler, not as three fixed
// gestures.
const (
	DirectionUp    = "up"
	DirectionDown  = "down"
	DirectionLeft  = "left"
	DirectionRight = "right"
)

// Practice modes: where a run's cards come from.
const (
	// PracticeMixed is the normal deck -- reviews that have fallen due,
	// topped up with words never seen before.
	PracticeMixed = "mixed"

	// PracticeUnknown drills the "not known" list and nothing else: no new
	// words, and no waiting for the scheduler to bring one round. Someone who
	// turns this on is asking to work through their own failures, which is a
	// different activity from studying.
	PracticeUnknown = "unknown_only"
)

// RevealHoldUntilTap keeps the answer on screen until the next input. It is
// negative so that zero keeps its obvious meaning -- no time at all, do not
// show the answer -- which is the other end of the same range.
const RevealHoldUntilTap = -1

const (
	defaultLevel         = 8
	defaultBatchSize     = 20
	defaultDailyNewLimit = 30
	defaultRevealHoldMs  = 3000
	maximumRevealHoldMs  = 60000
)

// settingsSchema versions what the stored fields *mean*, and is bumped only
// when an existing field's values change interpretation. Adding a field never
// needs it: a field that was not stored decodes to its zero value, which
// normalise() already turns into the default.
const settingsSchema = 1

// Settings are per user. Everything here is a display or pacing preference --
// nothing in it affects anyone else, so it needs no admin gate.
type Settings struct {
	// Schema is the version this record was written under. See migrate().
	Schema int `json:"schema"`

	// Level is the reading grade to draw from: 1-12 are school grades, 13,
	// 14 and 15 are undergraduate, graduate and doctoral.
	Level int `json:"level"`

	RevealMode string `json:"reveal_mode"`

	// The direction bound to each outcome. All three are always different,
	// and whichever direction is left over does nothing. Being able to move
	// them is not a cosmetic preference: which way a thumb travels easily
	// depends on the hand, the phone, and the case it is in.
	SwipeKnown   string `json:"swipe_known"`
	SwipeUnknown string `json:"swipe_unknown"`
	SwipeSkip    string `json:"swipe_skip"`

	// PracticeMode chooses between the scheduled deck and drilling the
	// "not known" list.
	PracticeMode string `json:"practice_mode"`

	// RevealHoldMs is how long the answer stays up when a card is answered
	// *before* it was revealed -- the one moment the app shows something the
	// user did not ask to see. Zero skips that reveal entirely and
	// RevealHoldUntilTap leaves it up until the next swipe or tap; anything
	// between is a duration in milliseconds.
	RevealHoldMs int `json:"reveal_hold_ms"`

	// AutoAdvanceMs is schema 0's version of RevealHoldMs, in which zero
	// meant "wait for a tap". It is kept only so migrate() can read it, and
	// is cleared -- and so omitted from the wire -- on the next save.
	AutoAdvanceMs int `json:"auto_advance_ms,omitempty"`

	// DailyNewLimit caps how many words never seen before appear in a day.
	// Reviews are never capped -- refusing to show a card that is due is how
	// a backlog becomes permanent.
	DailyNewLimit int `json:"daily_new_limit"`

	BatchSize    int  `json:"batch_size"`
	ShowExamples bool `json:"show_examples"`
	Haptics      bool `json:"haptics"`

	// Theme is "light" or "dark". Light is the default, and dark is a
	// deliberate choice rather than a reading of the system setting.
	Theme string `json:"theme"`

	UpdatedAt time.Time `json:"updated_at"`
}

func DefaultSettings() ( settings *Settings ) {
	settings = &Settings{
		Schema:        settingsSchema,
		Level:         defaultLevel,
		RevealMode:    RevealDefinition,
		SwipeKnown:    DirectionRight,
		SwipeUnknown:  DirectionLeft,
		SwipeSkip:     DirectionUp,
		PracticeMode:  PracticeMixed,
		RevealHoldMs:  defaultRevealHoldMs,
		DailyNewLimit: defaultDailyNewLimit,
		BatchSize:     defaultBatchSize,
		ShowExamples:  true,
		Haptics:       true,
		Theme:         "light",
	}
	return
}

func settingsKey( user_id uint64 ) ( key []byte ) {
	key = make( []byte , 8 )
	binary.BigEndian.PutUint64( key , user_id )
	return
}

// GetSettings never fails for a user who has not saved any: it returns the
// defaults. A missing record and an unconfigured user are the same thing, and
// making callers handle ErrNotFound would mean every one of them re-deriving
// the defaults.
func GetSettings( store *db.Store , user_id uint64 ) ( settings *Settings , err error ) {
	stored := &Settings{}
	get_err := store.Get( db.BucketSettings , settingsKey( user_id ) , stored )
	if get_err != nil {
		if get_err == db.ErrNotFound {
			settings = DefaultSettings()
			return
		}
		err = get_err
		return
	}
	stored.normalise()
	settings = stored
	return
}

// normalise repairs a record written by an older version, or one whose
// defaults have since changed. Doing it on read rather than with a migration
// keeps upgrades from needing one.
func ( settings *Settings ) normalise() {
	settings.migrate()

	if settings.Level < corpus.TierMin || settings.Level > corpus.TierMax {
		settings.Level = defaultLevel
	}
	if settings.RevealMode != RevealDefinition && settings.RevealMode != RevealWord {
		settings.RevealMode = RevealDefinition
	}
	if settings.PracticeMode != PracticeMixed && settings.PracticeMode != PracticeUnknown {
		settings.PracticeMode = PracticeMixed
	}
	settings.normaliseDirections()
	if settings.BatchSize < 5 || settings.BatchSize > 100 { settings.BatchSize = defaultBatchSize }
	if settings.DailyNewLimit < 0 || settings.DailyNewLimit > 500 { settings.DailyNewLimit = defaultDailyNewLimit }
	if settings.RevealHoldMs < RevealHoldUntilTap || settings.RevealHoldMs > maximumRevealHoldMs { settings.RevealHoldMs = defaultRevealHoldMs }
	if settings.Theme != "light" && settings.Theme != "dark" { settings.Theme = "light" }
	return
}

// normaliseDirections keeps the three outcomes on three different directions.
// A record with a duplicate or an unrecognised direction has an outcome that
// cannot be reached by swiping, so the whole mapping returns to the defaults
// rather than being patched in place: a mapping that has visibly reset is
// easier to understand than one repaired halfway.
func ( settings *Settings ) normaliseDirections() {
	valid := map[string]bool{
		DirectionUp: true , DirectionDown: true , DirectionLeft: true , DirectionRight: true,
	}
	seen := map[string]bool{}
	for _ , direction := range []string{ settings.SwipeKnown , settings.SwipeUnknown , settings.SwipeSkip } {
		if valid[ direction ] == false || seen[ direction ] {
			settings.SwipeKnown = DirectionRight
			settings.SwipeUnknown = DirectionLeft
			settings.SwipeSkip = DirectionUp
			return
		}
		seen[ direction ] = true
	}
	return
}

// migrate brings a record written by an older build up to the current schema.
// It runs on read, so an upgrade needs no migration pass at startup and no
// rewrite of records belonging to people who never sign in again.
func ( settings *Settings ) migrate() {
	if settings.Schema >= settingsSchema { return }

	// Schema 0 kept the reveal time in AutoAdvanceMs, where zero meant "wait
	// for a tap". Zero now means the opposite -- do not show the answer at
	// all -- so the old value is translated rather than carried across.
	// Without this, everyone who chose to read the answer at their own pace
	// would find the app had stopped showing it.
	settings.RevealHoldMs = settings.AutoAdvanceMs
	if settings.AutoAdvanceMs == 0 { settings.RevealHoldMs = RevealHoldUntilTap }
	settings.AutoAdvanceMs = 0
	settings.Schema = settingsSchema
	return
}

// SaveSettings replaces a user's record.
//
// The incoming record is stamped with the current schema before normalising,
// because anything arriving here was built by the current UI. Without that, a
// browser sending a reveal hold of zero -- "do not show the answer" -- would
// be mistaken for an un-migrated record and silently turned into "wait for a
// tap", which is the one value it cannot be.
func SaveSettings( store *db.Store , user_id uint64 , settings *Settings ) ( err error ) {
	settings.Schema = settingsSchema
	settings.AutoAdvanceMs = 0
	settings.normalise()
	settings.UpdatedAt = time.Now().UTC()
	err = store.Put( db.BucketSettings , settingsKey( user_id ) , settings )
	return
}
