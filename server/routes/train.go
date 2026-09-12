// Training endpoints: handing out a deck, recording swipes, and the three
// word lists those swipes build up.
//
// Everything here is per-user and needs nothing more than being signed in --
// admin in this app means "can decide who gets in", not "can study harder".
package routes

import (
	rand "math/rand"
	sort "sort"
	strings "strings"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	corpus "vocabtrainer/server/corpus"
	db "vocabtrainer/server/db"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
)

// cardView is what a card looks like on the wire: the word plus enough of the
// user's history for the UI to label it. Definitions come from the corpus at
// request time rather than from the stored record, so regenerating the word
// list never leaves stale copies behind.
type cardView struct {
	Word    string         `json:"word"`
	Tier    int            `json:"tier"`
	Senses  []corpus.Sense `json:"senses"`
	Zipf    float64        `json:"zipf,omitempty"`

	Status string `json:"status,omitempty"`
	IsNew  bool   `json:"is_new"`

	Reps   int `json:"reps,omitempty"`
	Lapses int `json:"lapses,omitempty"`

	DueAt      *time.Time `json:"due_at,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

func viewFromWord( word *corpus.Word , card *models.Card ) ( view cardView ) {
	view = cardView{
		Word:   word.Word,
		Tier:   word.Tier,
		Senses: word.Senses,
		Zipf:   word.Zipf,
		IsNew:  card == nil,
	}
	if card == nil { return }
	view.Status = card.Status
	view.Reps = card.Reps
	view.Lapses = card.Lapses
	if card.DueAt.IsZero() == false {
		due := card.DueAt
		view.DueAt = &due
	}
	if card.LastSeenAt.IsZero() == false {
		seen := card.LastSeenAt
		view.LastSeenAt = &seen
	}
	return
}

// localDate validates the browser-supplied day used for streaks and the
// daily new-word limit.
//
// The client sends it because a day is a local thing: a session at 11pm
// belongs to that evening, and a streak that rolled over at midnight UTC
// would be wrong for most of the world. It is accepted only within a day of
// the server's own date -- the value only ever affects the sender's own
// statistics, so this is a sanity check on a clock, not a security boundary.
func localDate( submitted string , now time.Time ) ( result string ) {
	result = models.DateKey( now )
	submitted = strings.TrimSpace( submitted )
	if len( submitted ) != 10 { return }
	parsed , err := time.Parse( "2006-01-02" , submitted )
	if err != nil { return }
	difference := parsed.Sub( now.Truncate( 24*time.Hour ) )
	if difference < -36*time.Hour || difference > 36*time.Hour { return }
	result = submitted
	return
}

// GetDeck builds the next run of cards.
//
// Due reviews come first and are never withheld -- capping them is how a
// backlog turns permanent -- and new words fill whatever is left, up to the
// daily limit the user set.
func ( handlers *Handlers ) GetDeck( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	now := time.Now().UTC()
	today := localDate( c.Query( "date" ) , now )

	settings , err := models.GetSettings( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }

	// One pass over this user's cards answers both questions a deck needs:
	// what is due, and what to leave out of the new draw. It is a prefix scan
	// over one user's range rather than the whole bucket -- linear in their
	// own history, which stays small enough to be cheaper than keeping a
	// separate index in step.
	cards , err := models.ListCards( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }

	// Drilling the "not known" list is a different deck entirely, so it is
	// answered before any of the scheduling below runs.
	if settings.PracticeMode == models.PracticeUnknown {
		err = handlers.unknownDeck( c , user.ID , settings , cards , now )
		return
	}

	seen := map[string]bool{}
	due := []*models.Card{}
	for _ , card := range cards {
		seen[ card.Word ] = true
		if card.Due( now ) { due = append( due , card ) }
	}
	sort.Slice( due , func( a int , b int ) bool { return due[ a ].DueAt.Before( due[ b ].DueAt ) } )

	views := []cardView{}
	for _ , card := range due {
		if len( views ) >= settings.BatchSize { break }
		word , found := handlers.Corpus.Lookup( card.Word )
		if found == false {
			// The word left the corpus in a rebuild. Skipping it here keeps
			// the deck working; the record stays, so it reappears if the
			// word comes back.
			continue
		}
		views = append( views , viewFromWord( word , card ) )
	}

	introduced := 0
	if settings.DailyNewLimit > 0 {
		stat , stat_err := models.GetDayStat( handlers.Store , user.ID , today )
		if stat_err != nil { return serverError( c ) }
		introduced = stat.New
	}

	room := settings.BatchSize - len( views )
	allowance := settings.DailyNewLimit - introduced
	if settings.DailyNewLimit == 0 { allowance = room }
	if allowance < 0 { allowance = 0 }
	if room > allowance { room = allowance }

	if room > 0 {
		random := rand.New( rand.NewSource( now.UnixNano() ) )
		fresh := handlers.Corpus.Draw( settings.Level , room ,
			func( word string ) bool { return seen[ word ] } , random )
		for _ , word := range fresh {
			views = append( views , viewFromWord( word , nil ) )
		}
	}

	counts , err := models.CountCards( handlers.Store , user.ID , now )
	if err != nil { return serverError( c ) }

	err = c.JSON( fiber.Map{
		"cards":            views,
		"settings":         settings,
		"counts":           counts,
		"new_remaining":    maxInt( settings.DailyNewLimit-introduced , 0 ),
		"daily_new_limit":  settings.DailyNewLimit,
	} )
	return
}

// unknownDeck builds a run from nothing but the words the user has marked as
// not known.
//
// The scheduler is deliberately ignored here. Someone who has turned this on
// is asking to work through their own failures now, and telling them a word
// they got wrong is "not due yet" would be answering a question they did not
// ask. Nothing new is drawn either: the point of the mode is that the deck
// has a bottom.
func ( handlers *Handlers ) unknownDeck( c fiber.Ctx , user_id uint64 , settings *models.Settings , cards []*models.Card , now time.Time ) ( err error ) {
	pool := []*models.Card{}
	for _ , card := range cards {
		if card.Status == models.StatusUnknown { pool = append( pool , card ) }
	}
	// Longest overdue first, so a run that stops early has still covered the
	// words that have waited longest.
	sort.Slice( pool , func( a int , b int ) bool { return pool[ a ].DueAt.Before( pool[ b ].DueAt ) } )

	views := []cardView{}
	for _ , card := range pool {
		if len( views ) >= settings.BatchSize { break }
		word , found := handlers.Corpus.Lookup( card.Word )
		if found == false { continue }
		views = append( views , viewFromWord( word , card ) )
	}

	counts , err := models.CountCards( handlers.Store , user_id , now )
	if err != nil { return serverError( c ) }

	err = c.JSON( fiber.Map{
		"cards":           views,
		"settings":        settings,
		"counts":          counts,
		"new_remaining":   0,
		"daily_new_limit": settings.DailyNewLimit,
		"practice_pool":   len( pool ),
	} )
	return
}

func maxInt( a int , b int ) ( result int ) {
	result = a
	if b > a { result = b }
	return
}

type reviewRequest struct {
	CSRFToken string `json:"csrf_token"`
	Word      string `json:"word"`
	Outcome   string `json:"outcome"`
	Date      string `json:"date"`
}

// PostReview records one swipe.
func ( handlers *Handlers ) PostReview( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	request := reviewRequest{}
	if err = c.Bind().Body( &request ); err != nil { return badRequest( c , "malformed request" ) }
	if handlers.Guard.CheckCSRF( c , request.CSRFToken ) == false { return forbidden( c , "bad csrf token" ) }

	switch request.Outcome {
	case models.OutcomeKnown , models.OutcomeUnknown , models.OutcomeSkip:
	default:
		return badRequest( c , "unrecognised outcome" )
	}

	word , found := handlers.Corpus.Lookup( request.Word )
	if found == false { return notFound( c , "no such word" ) }

	now := time.Now().UTC()
	card , created , err := models.RecordReview( handlers.Store , user.ID , word.Word , word.Tier , request.Outcome , now )
	if err != nil { return serverError( c ) }

	// A failed statistic must not fail the review -- the swipe is the thing
	// the user cares about, and the counter is a nicety.
	_ = models.RecordActivity( handlers.Store , user.ID , localDate( request.Date , now ) , request.Outcome , created )

	counts , err := models.CountCards( handlers.Store , user.ID , now )
	if err != nil { return serverError( c ) }

	err = c.JSON( fiber.Map{ "card": viewFromWord( word , card ) , "counts": counts , "was_new": created } )
	return
}

// GetCards lists one of the three word lists.
func ( handlers *Handlers ) GetCards( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	status := c.Query( "status" , models.StatusUnknown )
	switch status {
	case models.StatusKnown , models.StatusUnknown , models.StatusSkipped:
	default:
		return badRequest( c , "unrecognised status" )
	}

	cards , err := models.ListCardsByStatus( handlers.Store , user.ID , status )
	if err != nil { return serverError( c ) }

	views := []cardView{}
	for _ , card := range cards {
		word , found := handlers.Corpus.Lookup( card.Word )
		if found == false { continue }
		views = append( views , viewFromWord( word , card ) )
	}
	err = c.JSON( fiber.Map{ "cards": views , "status": status } )
	return
}

type cardActionRequest struct {
	CSRFToken string `json:"csrf_token"`
	Word      string `json:"word"`
	Status    string `json:"status"`
}

// PostCardStatus is the manual move from a list -- "actually I know this one",
// or setting one aside from the unknown list.
func ( handlers *Handlers ) PostCardStatus( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	request := cardActionRequest{}
	if err = c.Bind().Body( &request ); err != nil { return badRequest( c , "malformed request" ) }
	if handlers.Guard.CheckCSRF( c , request.CSRFToken ) == false { return forbidden( c , "bad csrf token" ) }

	switch request.Status {
	case models.StatusKnown , models.StatusUnknown , models.StatusSkipped:
	default:
		return badRequest( c , "unrecognised status" )
	}

	word , found := handlers.Corpus.Lookup( request.Word )
	if found == false { return notFound( c , "no such word" ) }

	card , err := models.SetCardStatus( handlers.Store , user.ID , word.Word , request.Status , time.Now().UTC() )
	if err != nil {
		if err == db.ErrNotFound { return notFound( c , "no progress for that word" ) }
		return serverError( c )
	}
	err = c.JSON( fiber.Map{ "card": viewFromWord( word , card ) } )
	return
}

// PostUnskip takes a word back out of the skip list, returning it to whatever
// list it was in before -- or to the new pool if it was skipped on sight.
func ( handlers *Handlers ) PostUnskip( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	request := cardActionRequest{}
	if err = c.Bind().Body( &request ); err != nil { return badRequest( c , "malformed request" ) }
	if handlers.Guard.CheckCSRF( c , request.CSRFToken ) == false { return forbidden( c , "bad csrf token" ) }

	word , found := handlers.Corpus.Lookup( request.Word )
	if found == false { return notFound( c , "no such word" ) }

	card , removed , err := models.Unskip( handlers.Store , user.ID , word.Word , time.Now().UTC() )
	if err != nil {
		if err == db.ErrNotFound { return notFound( c , "no progress for that word" ) }
		return serverError( c )
	}
	if removed {
		err = c.JSON( fiber.Map{ "card": viewFromWord( word , nil ) , "returned_to_new": true } )
		return
	}
	err = c.JSON( fiber.Map{ "card": viewFromWord( word , card ) , "returned_to_new": false } )
	return
}

// PostForget drops a card entirely, putting the word back in the draw.
func ( handlers *Handlers ) PostForget( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	request := cardActionRequest{}
	if err = c.Bind().Body( &request ); err != nil { return badRequest( c , "malformed request" ) }
	if handlers.Guard.CheckCSRF( c , request.CSRFToken ) == false { return forbidden( c , "bad csrf token" ) }

	if err = models.ForgetCard( handlers.Store , user.ID , request.Word ); err != nil {
		return serverError( c )
	}
	err = c.JSON( fiber.Map{ "ok": true } )
	return
}

// GetSearch finds words by spelling, so someone can look up a word and file
// it directly into a list.
func ( handlers *Handlers ) GetSearch( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	matches := handlers.Corpus.Search( c.Query( "q" ) , 25 )

	views := []cardView{}
	for _ , word := range matches {
		card , card_err := models.GetCard( handlers.Store , user.ID , word.Word )
		if card_err != nil { card = nil }
		views = append( views , viewFromWord( word , card ) )
	}
	err = c.JSON( fiber.Map{ "cards": views } )
	return
}
