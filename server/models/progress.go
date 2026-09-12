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

var ErrUnknownOutcome = errors.New( "models: unrecognised review outcome" )

// Scheduler constants, adapted from SM-2.
//
// SM-2 expects a 0-5 self-rating; the swipe gives one bit. That turns out to
// be the right trade for vocabulary: a finer scale mostly adds hesitation,
// and the interval sequence below recovers most of the benefit by treating
// the first few correct answers specially instead of multiplying from the
// start.
const (
	startingEase = 2.5
	minimumEase  = 1.3
	maximumEase  = 3.0

	easeOnKnown = 0.10
	easeOnLapse = 0.20

	// The first two correct answers use fixed jumps. A card answered "I
	// already know this" on first sight should not come back tomorrow --
	// that is the complaint people have about flashcards, and the user
	// asserting knowledge is real evidence.
	firstInterval  = 4.0
	secondInterval = 10.0
	maximumInterval = 365.0

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

// applyOutcome is the scheduler. It is a method on Card and mutates in place
// so that it can run inside the read-modify-write transaction below, which is
// what stops two rapid swipes on the same card from losing one of the
// updates.
func ( card *Card ) applyOutcome( outcome string , now time.Time ) ( err error ) {
	card.LastSeenAt = now
	if card.FirstSeenAt.IsZero() { card.FirstSeenAt = now }
	if card.Ease == 0 { card.Ease = startingEase }

	switch outcome {
	case OutcomeKnown:
		card.Status = StatusKnown
		card.PreviousStatus = ""
		card.Reps += 1
		switch card.Reps {
		case 1:
			card.IntervalDays = firstInterval
		case 2:
			card.IntervalDays = secondInterval
		default:
			card.IntervalDays = card.IntervalDays * card.Ease
		}
		card.IntervalDays = math.Min( card.IntervalDays , maximumInterval )
		card.Ease = math.Min( card.Ease+easeOnKnown , maximumEase )
		card.DueAt = now.Add( time.Duration( card.IntervalDays * float64( 24*time.Hour ) ) )

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
