package models

import (
	binary "encoding/binary"
	errors "errors"
	math "math"
	sort "sort"
	strings "strings"
	time "time"

	db "vocabtrainer/server/db"
)

// Card statuses. These are the three lists the user actually thinks in --
// words they know, words they did not know, words they set aside -- so they
// are stored as the state rather than derived from the scheduler's numbers.
// The scheduler decides *when* a card comes back; the status decides which
// list it shows up in.
const (
	StatusKnown   = "known"
	StatusUnknown = "unknown"
	StatusSkipped = "skipped"
)

// Review outcomes, one per swipe direction. Which direction carries which
// outcome is the user's to set -- see the swipe_* fields in Settings -- so the
// directions noted here are only the defaults.
const (
	OutcomeKnown   = "known"   // swipe right by default
	OutcomeUnknown = "unknown" // swipe left by default
	OutcomeSkip    = "skip"    // swipe up by default
)

// The two extra grades, sent only by the four-button scoring mode -- see
// ScoringMode in settings.go.
//
// This is not a second scheduler bolted on beside the first. The four grades
// are Again, Hard, Good and Easy, and two of them are outcomes this package
// already had: Again is OutcomeUnknown and Good is OutcomeKnown, under the
// names Anki gives them. These two fill the gap between those and the space
// beyond them, so a card can be answered "I got it, but barely" or "that was
// far too easy" instead of only "yes" and "no".
const (
	OutcomeHard = "hard"
	OutcomeEasy = "easy"
)

var ErrUnknownOutcome = errors.New( "models: unrecognised review outcome" )

// Scheduler constants, adapted from SM-2.
//
// SM-2 expects a 0-5 self-rating; a swipe gives one bit. That turns out to be
// the right trade for vocabulary by default: a finer scale mostly adds
// hesitation, and the interval sequence below recovers most of the benefit by
// treating the first few correct answers specially instead of multiplying
// from the start.
//
// Anyone who would rather grade properly can turn the four-button mode on,
// which adds the two grades either side of the plain yes and no. The numbers
// for all four live here together, because they only make sense relative to
// each other.
const (
	startingEase = 2.5
	minimumEase  = 1.3
	maximumEase  = 3.0

	easeOnKnown = 0.10
	easeOnLapse = 0.20

	// What the two extra grades do to the ease. Both move it further than a
	// plain "good" does, in their own direction: they are the answers where
	// the user has said something specific about the difficulty, and an
	// answer that says more should count for more.
	easeOnHard = 0.15
	easeOnEasy = 0.15

	// From the third correct answer on, the interval is multiplied. "Good"
	// multiplies by the ease alone; "hard" ignores the ease and takes this
	// small fixed step, which is what stops a card the user keeps only just
	// remembering from either running away or standing still; "easy" takes
	// the ease and this bonus on top.
	hardFactor = 1.2
	easyBonus  = 1.3

	// The first two correct answers use fixed jumps. A card answered "I
	// already know this" on first sight should not come back tomorrow --
	// that is the complaint people have about flashcards, and the user
	// asserting knowledge is real evidence.
	firstInterval  = 4.0
	secondInterval = 10.0
	maximumInterval = 365.0

	// The same two fixed jumps for the other two grades. "Hard" on a card
	// seen once means it was recalled, so it does not come back in minutes
	// like a lapse -- but a day is soon enough to be worth it. "Easy" says
	// the fixed sequence is already too slow for this word, so it skips
	// ahead of where "good" would put it.
	hardFirstInterval  = 1.0
	hardSecondInterval = 3.0
	easyFirstInterval  = 8.0
	easySecondInterval = 21.0

	// A lapse comes back inside the same session rather than the same day:
	// seeing the word again shortly after getting it wrong is most of why
	// relearning works, and a card due "today" would otherwise be due after
	// the user has stopped.
	lapseDelay = 10 * time.Minute
)

// Card is one user's history with one word. The word's definitions are not
// copied in -- they live in the corpus, which means rebuilding the word list
// does not leave stale copies behind in the database.
type Card struct {
	Word   string `json:"word"`
	Tier   int    `json:"tier"`
	Status string `json:"status"`

	Ease         float64 `json:"ease"`
	IntervalDays float64 `json:"interval_days"`
	Reps         int     `json:"reps"`
	Lapses       int     `json:"lapses"`

	DueAt       time.Time `json:"due_at"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`

	// PreviousStatus remembers where a card came from when it is skipped, so
	// unskipping puts it back in the list it was in rather than guessing.
	PreviousStatus string `json:"previous_status,omitempty"`
}

// Due reports whether the scheduler wants this card now. Skipped cards are
// never due; that is what taking them out of rotation means.
func ( card *Card ) Due( now time.Time ) ( result bool ) {
	if card.Status == StatusSkipped { return }
	result = card.DueAt.Before( now ) || card.DueAt.Equal( now )
	return
}

// progressKey lays out user id first so that one user's cards form a
// contiguous range -- see db.ForEachPrefix.
func progressKey( user_id uint64 , word string ) ( key []byte ) {
	word = strings.ToLower( strings.TrimSpace( word ) )
	key = make( []byte , 8 , 8+len( word ) )
	binary.BigEndian.PutUint64( key , user_id )
	key = append( key , word... )
	return
}

func userPrefix( user_id uint64 ) ( prefix []byte ) {
	prefix = make( []byte , 8 )
	binary.BigEndian.PutUint64( prefix , user_id )
	return
}

func GetCard( store *db.Store , user_id uint64 , word string ) ( card *Card , err error ) {
	card = &Card{}
	err = store.Get( db.BucketProgress , progressKey( user_id , word ) , card )
	if err != nil { card = nil }
	return
}

// ListCards returns every card belonging to one user, newest activity first.
func ListCards( store *db.Store , user_id uint64 ) ( cards []*Card , err error ) {
	cards = []*Card{}
	err = store.ForEachPrefix( db.BucketProgress , userPrefix( user_id ) ,
		func() any { return &Card{} } ,
		func( key []byte , item any ) bool {
			cards = append( cards , item.( *Card ) )
			return true
		} )
	if err != nil { return }
	sort.Slice( cards , func( a int , b int ) bool {
		return cards[ a ].LastSeenAt.After( cards[ b ].LastSeenAt )
	} )
	return
}

// ListCardsByStatus backs the three list screens.
func ListCardsByStatus( store *db.Store , user_id uint64 , status string ) ( cards []*Card , err error ) {
	all , err := ListCards( store , user_id )
	if err != nil { return }
	cards = []*Card{}
	for _ , card := range all {
		if card.Status == status { cards = append( cards , card ) }
	}
	return
}

// CardWords returns the set of words a user has already acted on, for
// excluding them from a fresh draw.
func CardWords( store *db.Store , user_id uint64 ) ( words map[string]bool , err error ) {
	words = map[string]bool{}
	err = store.ForEachPrefix( db.BucketProgress , userPrefix( user_id ) ,
		func() any { return &Card{} } ,
		func( key []byte , item any ) bool {
			words[ item.( *Card ).Word ] = true
			return true
		} )
	return
}

// grade is the three answers that all mean "I remembered it", reduced to the
// four numbers they differ in. Keeping them in a table rather than in three
// branches is what makes them comparable: the whole difference between Hard,
// Good and Easy is one row each, and a row that looks wrong beside the others
// probably is.
//
// In the two-answer mode only the Good row is ever used, and its numbers are
// the ones the scheduler has always had -- so turning the four-button mode on
// and off again changes nothing about how a card is scheduled.
type grade struct {
	first    float64 // days, on the first answer this card has ever had
	second   float64 // days, on the second
	growth   float64 // from the third on: the multiplier, or its bonus
	usesEase bool    // whether growth multiplies the ease or replaces it
	ease     float64 // what this answer moves the ease by
}

var grades = map[string]grade{
	OutcomeHard: {
		first: hardFirstInterval , second: hardSecondInterval,
		growth: hardFactor , usesEase: false , ease: -easeOnHard,
	},
	OutcomeKnown: {
		first: firstInterval , second: secondInterval,
		growth: 1.0 , usesEase: true , ease: easeOnKnown,
	},
	OutcomeEasy: {
		first: easyFirstInterval , second: easySecondInterval,
		growth: easyBonus , usesEase: true , ease: easeOnEasy,
	},
}

// remembered files a card the user got right, at whichever of the three
// grades they gave it.
func ( card *Card ) remembered( rule grade , now time.Time ) {
	card.Status = StatusKnown
	card.PreviousStatus = ""
	card.Reps += 1
	switch card.Reps {
	case 1:
		card.IntervalDays = rule.first
	case 2:
		card.IntervalDays = rule.second
	default:
		multiplier := rule.growth
		if rule.usesEase { multiplier = card.Ease * rule.growth }
		card.IntervalDays = card.IntervalDays * multiplier
	}
	card.IntervalDays = math.Min( card.IntervalDays , maximumInterval )
	// The ease moves after the interval has been worked out, so an answer
	// affects the card it is given to and the ones after it rather than
	// doubling up on this one.
	card.Ease = math.Min( math.Max( card.Ease+rule.ease , minimumEase ) , maximumEase )
	card.DueAt = now.Add( time.Duration( card.IntervalDays * float64( 24*time.Hour ) ) )
	return
}

// applyOutcome is the scheduler. It is a method on Card and mutates in place
// so that it can run inside the read-modify-write transaction below, which is
// what stops two rapid swipes on the same card from losing one of the
// updates.
func ( card *Card ) applyOutcome( outcome string , now time.Time ) ( err error ) {
	card.LastSeenAt = now
	if card.FirstSeenAt.IsZero() { card.FirstSeenAt = now }
	if card.Ease == 0 { card.Ease = startingEase }

	switch outcome {
	case OutcomeKnown , OutcomeHard , OutcomeEasy:
		card.remembered( grades[ outcome ] , now )

	case OutcomeUnknown:
		card.Status = StatusUnknown
		card.PreviousStatus = ""
		card.Lapses += 1
		// Reps resets so the card climbs the fixed early intervals again
		// rather than resuming a long multiplied one it clearly has not
		// earned.
		card.Reps = 0
		card.IntervalDays = 0
		card.Ease = math.Max( card.Ease-easeOnLapse , minimumEase )
		card.DueAt = now.Add( lapseDelay )

	case OutcomeSkip:
		if card.Status != StatusSkipped { card.PreviousStatus = card.Status }
		card.Status = StatusSkipped
		// DueAt is left alone: if the card is ever unskipped, it resumes on
		// the schedule it had rather than flooding back in all at once.

	default:
		err = ErrUnknownOutcome
	}
	return
}

// Preview reports what each answer would do to this card, as the number of
// seconds it would be put away for. Pass a nil card for a word never seen
// before.
//
// This is what the four-button mode prints under each grade, and it is the
// one thing Anki's grading genuinely needs: a grade whose consequence you
// cannot see is a grade you are guessing at, and "Easy" meaning three weeks
// on one card and nine months on another is not something anyone can hold in
// their head.
//
// The numbers come from running the real scheduler on a copy of the card
// rather than from a second implementation of it, so a button cannot promise
// one thing and the scheduler then do another. Card holds no pointers or
// slices, so the copy really is one.
//
// Skip is not previewed: setting a word aside leaves the schedule exactly as
// it was, so there is no interval to show.
func Preview( card *Card , now time.Time ) ( intervals map[string]int64 ) {
	source := Card{}
	if card != nil { source = *card }
	if source.Ease == 0 { source.Ease = startingEase }

	intervals = map[string]int64{}
	for _ , outcome := range []string{ OutcomeUnknown , OutcomeHard , OutcomeKnown , OutcomeEasy } {
		trial := source
		if trial.applyOutcome( outcome , now ) != nil { continue }
		seconds := int64( trial.DueAt.Sub( now ) / time.Second )
		if seconds < 0 { seconds = 0 }
		intervals[ outcome ] = seconds
	}
	return
}

// RecordReview applies one swipe, creating the card if this is the first time
// the user has seen the word.
//
// The create and the update are separate paths because bolt has no upsert:
// PutIfAbsent claims the key if it is new, and UpdateValue does the
// read-modify-write if it is not. Both are single transactions, so a card
// cannot be created twice and an update cannot be lost.
func RecordReview( store *db.Store , user_id uint64 , word string , tier int , outcome string , now time.Time ) ( card *Card , created bool , err error ) {
	word = strings.ToLower( strings.TrimSpace( word ) )
	key := progressKey( user_id , word )

	fresh := &Card{ Word: word , Tier: tier , Ease: startingEase }
	if err = fresh.applyOutcome( outcome , now ); err != nil { return }

	written , err := store.PutIfAbsent( db.BucketProgress , key , fresh )
	if err != nil { return }
	if written {
		card , created = fresh , true
		return
	}

	err = store.UpdateValue( db.BucketProgress , key ,
		func() any { return &Card{} } ,
		func( item any ) ( mutate_err error ) {
			existing := item.( *Card )
			if existing.Tier == 0 { existing.Tier = tier }
			if mutate_err = existing.applyOutcome( outcome , now ); mutate_err != nil { return }
			card = existing
			return
		} )
	if err != nil { card = nil }
	return
}

// SetCardStatus is the manual move from a list screen -- "actually, I know
// this one", or unskipping. It does not touch the scheduler beyond what the
// status change implies, because the user is correcting a label rather than
// answering a card.
func SetCardStatus( store *db.Store , user_id uint64 , word string , status string , now time.Time ) ( card *Card , err error ) {
	err = store.UpdateValue( db.BucketProgress , progressKey( user_id , word ) ,
		func() any { return &Card{} } ,
		func( item any ) ( mutate_err error ) {
			existing := item.( *Card )
			switch status {
			case StatusKnown:
				existing.PreviousStatus = ""
				existing.Status = StatusKnown
				if existing.IntervalDays < firstInterval { existing.IntervalDays = firstInterval }
				if existing.Reps == 0 { existing.Reps = 1 }
				existing.DueAt = now.Add( time.Duration( existing.IntervalDays * float64( 24*time.Hour ) ) )
			case StatusUnknown:
				existing.PreviousStatus = ""
				existing.Status = StatusUnknown
				existing.IntervalDays = 0
				existing.DueAt = now
			case StatusSkipped:
				if existing.Status != StatusSkipped { existing.PreviousStatus = existing.Status }
				existing.Status = StatusSkipped
			default:
				mutate_err = ErrUnknownOutcome
				return
			}
			existing.LastSeenAt = now
			card = existing
			return
		} )
	if err != nil { card = nil }
	return
}

// Unskip restores a skipped card to whichever list it was in. A card skipped
// before it was ever answered has nowhere to return to, so it becomes new
// again -- the record is deleted and the word goes back in the draw.
func Unskip( store *db.Store , user_id uint64 , word string , now time.Time ) ( card *Card , removed bool , err error ) {
	existing , err := GetCard( store , user_id , word )
	if err != nil { return }
	if existing.Status != StatusSkipped { card = existing; return }

	if existing.PreviousStatus == "" {
		err = store.Delete( db.BucketProgress , progressKey( user_id , word ) )
		removed = err == nil
		return
	}
	card , err = SetCardStatus( store , user_id , word , existing.PreviousStatus , now )
	return
}

// ForgetCard removes a card entirely, putting the word back in the new pool.
func ForgetCard( store *db.Store , user_id uint64 , word string ) ( err error ) {
	err = store.Delete( db.BucketProgress , progressKey( user_id , word ) )
	return
}

// ResetProgress deletes everything one user has done. Used by the account
// screen and by admin account removal.
func ResetProgress( store *db.Store , user_id uint64 ) ( removed int , err error ) {
	prefix := userPrefix( user_id )
	removed , err = store.DeleteWhere( db.BucketProgress ,
		func() any { return &Card{} } ,
		func( key []byte , item any ) bool {
			return len( key ) >= 8 && string( key[ :8 ] ) == string( prefix )
		} )
	return
}

// Counts summarises a user's cards for the progress screen.
type Counts struct {
	Known   int `json:"known"`
	Unknown int `json:"unknown"`
	Skipped int `json:"skipped"`
	Due     int `json:"due"`
	Total   int `json:"total"`
}

func CountCards( store *db.Store , user_id uint64 , now time.Time ) ( counts Counts , err error ) {
	err = store.ForEachPrefix( db.BucketProgress , userPrefix( user_id ) ,
		func() any { return &Card{} } ,
		func( key []byte , item any ) bool {
			card := item.( *Card )
			counts.Total += 1
			switch card.Status {
			case StatusKnown:
				counts.Known += 1
			case StatusUnknown:
				counts.Unknown += 1
			case StatusSkipped:
				counts.Skipped += 1
			}
			if card.Due( now ) { counts.Due += 1 }
			return true
		} )
	return
}
