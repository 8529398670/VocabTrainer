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

	errors "errors"

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

	// Intervals is what each answer would do to this card, in seconds --
	// what the four-button mode prints under each grade. Absent in the
	// two-answer mode, where nothing reads it: see deckView.
	Intervals map[string]int64 `json:"intervals,omitempty"`

	Reps   int `json:"reps,omitempty"`
	Lapses int `json:"lapses,omitempty"`

	DueAt      *time.Time `json:"due_at,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

func ( cardView ) APIName() ( name string ) { name = "Card"; return }

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

// deckView is viewFromWord plus the interval preview each grade is labelled
// with, for a card that is about to be dealt.
//
// The previews are left off entirely in the two-answer mode. Nothing reads
// them there, and a deck carries twenty cards -- four numbers apiece is not
// much, but it is not nothing either, and a field on the wire that no client
// uses is a field somebody eventually believes in.
func deckView( word *corpus.Word , card *models.Card , settings *models.Settings , now time.Time ) ( view cardView ) {
	view = viewFromWord( word , card )
	if settings.ScoringMode == models.ScoringGraded {
		view.Intervals = models.Preview( card , now )
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

// maximumExtraNewWords bounds what one press of the "more words" button can
// ask for. The button asks for ten; this is only here so that a hand-written
// URL cannot ask for the whole corpus in one response.
const maximumExtraNewWords = 100

// localDay is the ?date= every route that counts days takes: the browser's
// own date, which localDate checks against the server's.
type localDay struct {
	Date string `json:"date,omitempty" query:"date" jsonschema_description:"Today in the browser's time zone, YYYY-MM-DD. Streaks and the daily new-word limit count local days; a date more than a day off the server's is ignored."`
}

// wordRequest names one word in the corpus.
type wordRequest struct {
	Word string `json:"word" jsonschema:"minLength=1,maxLength=100" error:"word is required"`
}

type deckRequest struct {
	localDay
	More int `json:"more,omitempty" query:"more" jsonschema:"minimum=0,maximum=100" jsonschema_description:"New words to add over and above the daily limit: what the empty screen's More button asks for." error:"more must be 0-100"`
}

type deckReply struct {
	Cards         []cardView       `json:"cards"`
	Settings      *models.Settings `json:"settings"`
	Counts        models.Counts    `json:"counts"`
	NewRemaining  int              `json:"new_remaining"`
	DailyNewLimit int              `json:"daily_new_limit"`
	// PracticePool is how many words the not-known practice mode is drawing
	// from, sent only in that mode.
	PracticePool *int `json:"practice_pool,omitempty"`
}

func ( deckReply ) APIName() ( name string ) { name = "Deck"; return }

// getDeck builds the next run of cards.
//
// Due reviews come first and are never withheld -- capping them is how a
// backlog turns permanent -- and new words fill whatever is left, up to the
// daily limit the user set.
//
// ?more=N is the exception, and the one the empty screen's button uses: N new
// words regardless of the limit. That is not a hole in the pacing. The limit
// exists to stop someone being handed more new words than they meant to take
// on; a person who has been told there is nothing left and pressed a button
// asking for ten more has meant to take them on.
//
// @route("GET /api/deck", summary="The next run of cards",
//     tag="Training",
//     description="Due reviews first, then new words up to the daily limit, as the practice mode in the "
//         "account's settings says.",
// )
func ( handlers *Handlers ) getDeck( c fiber.Ctx , in *deckRequest ) ( reply *deckReply , err error ) {
	user := security.UserFrom( c )
	now := time.Now().UTC()
	today := localDate( in.Date , now )

	settings , err := models.GetSettings( handlers.Store , user.ID )
	if err != nil { return }

	// One pass over this user's cards answers both questions a deck needs:
	// what is due, and what to leave out of the new draw. It is a prefix scan
	// over one user's range rather than the whole bucket -- linear in their
	// own history, which stays small enough to be cheaper than keeping a
	// separate index in step.
	cards , err := models.ListCards( handlers.Store , user.ID )
	if err != nil { return }

	// Two of the three practice modes build a deck the scheduler has no say
	// in, so they are answered before any of the scheduling below runs.
	switch settings.PracticeMode {
	case models.PracticeUnknown:
		reply , err = handlers.unknownDeck( user.ID , settings , cards , now )
		return
	case models.PracticeNew:
		reply , err = handlers.newDeck( user.ID , settings , cards , now )
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
		views = append( views , deckView( word , card , settings , now ) )
	}

	introduced := 0
	if settings.DailyNewLimit > 0 {
		stat , stat_err := models.GetDayStat( handlers.Store , user.ID , today )
		if stat_err != nil {
			err = stat_err
			return
		}
		introduced = stat.New
	}

	room := settings.BatchSize - len( views )
	allowance := settings.DailyNewLimit - introduced
	if settings.DailyNewLimit == 0 { allowance = room }
	if allowance < 0 { allowance = 0 }
	if room > allowance { room = allowance }

	// An explicit request overrides both caps, the batch size along with the
	// daily limit: the number on the button is the number that arrives, and a
	// run that is a few cards longer than usual is what was asked for.
	if in.More > 0 { room = min( in.More , maximumExtraNewWords ) }

	if room > 0 {
		random := rand.New( rand.NewSource( now.UnixNano() ) )
		fresh := handlers.Corpus.Draw( settings.Level , room ,
			func( word string ) bool { return seen[ word ] } , random )
		for _ , word := range fresh {
			views = append( views , deckView( word , nil , settings , now ) )
		}
	}

	counts , err := models.CountCards( handlers.Store , user.ID , now )
	if err != nil { return }

	reply = &deckReply{
		Cards:         views,
		Settings:      settings,
		Counts:        counts,
		NewRemaining:  maxInt( settings.DailyNewLimit-introduced , 0 ),
		DailyNewLimit: settings.DailyNewLimit,
	}
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
func ( handlers *Handlers ) unknownDeck( user_id uint64 , settings *models.Settings , cards []*models.Card , now time.Time ) ( reply *deckReply , err error ) {
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
		views = append( views , deckView( word , card , settings , now ) )
	}

	counts , err := models.CountCards( handlers.Store , user_id , now )
	if err != nil { return }

	pool_size := len( pool )
	reply = &deckReply{
		Cards:         views,
		Settings:      settings,
		Counts:        counts,
		NewRemaining:  0,
		DailyNewLimit: settings.DailyNewLimit,
		PracticePool:  &pool_size,
	}
	return
}

// newDeck builds a run from words the user has never seen, and nothing else.
//
// No reviews, no daily limit, and no bottom: the run is one batch, and
// running out of it simply asks for the next one, so the screen never says
// "come back later". Someone who has chosen this mode has said they would
// rather meet words than be paced through them, and almost everything else in
// this file exists to do the opposite.
//
// Swipes are still recorded. That is not a contradiction: the record is what
// files the word in a list and what keeps it out of the next draw, so without
// it the same twenty words would come back forever. What the record no longer
// does is bring the word round again.
func ( handlers *Handlers ) newDeck( user_id uint64 , settings *models.Settings , cards []*models.Card , now time.Time ) ( reply *deckReply , err error ) {
	seen := map[string]bool{}
	for _ , card := range cards { seen[ card.Word ] = true }

	random := rand.New( rand.NewSource( now.UnixNano() ) )
	fresh := handlers.Corpus.Draw( settings.Level , settings.BatchSize ,
		func( word string ) bool { return seen[ word ] } , random )

	views := []cardView{}
	for _ , word := range fresh {
		views = append( views , deckView( word , nil , settings , now ) )
	}

	counts , err := models.CountCards( handlers.Store , user_id , now )
	if err != nil { return }

	// new_remaining is zero because there is no allowance to have left: this
	// deck does not consult the daily limit at all. An empty deck here means
	// the reading level itself is spent, which is the one thing the empty
	// screen has to say.
	reply = &deckReply{
		Cards:         views,
		Settings:      settings,
		Counts:        counts,
		NewRemaining:  0,
		DailyNewLimit: settings.DailyNewLimit,
	}
	return
}

func maxInt( a int , b int ) ( result int ) {
	result = a
	if b > a { result = b }
	return
}

type reviewRequest struct {
	wordRequest
	localDay
	Outcome string `json:"outcome" jsonschema:"enum=known,enum=unknown,enum=skip,enum=hard,enum=easy" error:"unrecognised outcome"`
}

type reviewReply struct {
	Card   cardView      `json:"card"`
	Counts models.Counts `json:"counts"`
	WasNew bool          `json:"was_new"`
}

// postReview records one swipe.
//
// The two extra grades are accepted whatever the user's scoring mode says.
// Checking it here would only be able to reject a swipe that was already on
// its way when the setting changed -- a card answered honestly, thrown away
// for a reason the user would never see.
//
// @route("POST /api/review", summary="Record one answer",
//     tag="Training", errors=(404,),
//     description="One swipe or button press on a card: known, unknown, skip, or the graded mode's hard and easy.",
// )
func ( handlers *Handlers ) postReview( c fiber.Ctx , in *reviewRequest ) ( reply *reviewReply , err error ) {
	user := security.UserFrom( c )
	word , found := handlers.Corpus.Lookup( in.Word )
	if found == false {
		err = refuse( fiber.StatusNotFound , "no such word" )
		return
	}

	now := time.Now().UTC()
	card , created , err := models.RecordReview( handlers.Store , user.ID , word.Word , word.Tier , in.Outcome , now )
	if err != nil { return }

	// A failed statistic must not fail the review -- the swipe is the thing
	// the user cares about, and the counter is a nicety.
	_ = models.RecordActivity( handlers.Store , user.ID , localDate( in.Date , now ) , in.Outcome , created )

	counts , err := models.CountCards( handlers.Store , user.ID , now )
	if err != nil { return }

	reply = &reviewReply{ Card: viewFromWord( word , card ) , Counts: counts , WasNew: created }
	return
}

type cardsRequest struct {
	Status string `json:"status,omitempty" query:"status" jsonschema:"enum=known,enum=unknown,enum=skipped,default=unknown" jsonschema_description:"Which list. Default unknown." error:"unrecognised status"`
}

type cardsReply struct {
	Cards  []cardView `json:"cards"`
	Status string     `json:"status"`
}

// getCards lists one of the three word lists.
//
// @route("GET /api/cards", summary="One of the three word lists",
//     tag="Training",
// )
func ( handlers *Handlers ) getCards( c fiber.Ctx , in *cardsRequest ) ( reply *cardsReply , err error ) {
	user := security.UserFrom( c )
	status := in.Status
	if status == "" { status = models.StatusUnknown }

	cards , err := models.ListCardsByStatus( handlers.Store , user.ID , status )
	if err != nil { return }

	views := []cardView{}
	for _ , card := range cards {
		word , found := handlers.Corpus.Lookup( card.Word )
		if found == false { continue }
		views = append( views , viewFromWord( word , card ) )
	}
	reply = &cardsReply{ Cards: views , Status: status }
	return
}

type cardStatusRequest struct {
	wordRequest
	Status string `json:"status" jsonschema:"enum=known,enum=unknown,enum=skipped" error:"unrecognised status"`
}

type cardReply struct {
	Card cardView `json:"card"`
}

// progressNotFound is the 404 for a word the user has no record of.
func progressNotFound( cause error ) ( err error ) {
	err = cause
	if errors.Is( cause , db.ErrNotFound ) { err = refuse( fiber.StatusNotFound , "no progress for that word" ) }
	return
}

// postCardStatus is the manual move from a list -- "actually I know this
// one", or setting one aside from the unknown list.
//
// @route("POST /api/cards/status", summary="Move a word to another list",
//     tag="Training", errors=(404,),
// )
func ( handlers *Handlers ) postCardStatus( c fiber.Ctx , in *cardStatusRequest ) ( reply *cardReply , err error ) {
	user := security.UserFrom( c )
	word , found := handlers.Corpus.Lookup( in.Word )
	if found == false {
		err = refuse( fiber.StatusNotFound , "no such word" )
		return
	}
	card , set_err := models.SetCardStatus( handlers.Store , user.ID , word.Word , in.Status , time.Now().UTC() )
	if set_err != nil {
		err = progressNotFound( set_err )
		return
	}
	reply = &cardReply{ Card: viewFromWord( word , card ) }
	return
}

type unskipReply struct {
	Card          cardView `json:"card"`
	ReturnedToNew bool     `json:"returned_to_new"`
}

// postUnskip takes a word back out of the skip list, returning it to
// whatever list it was in before -- or to the new pool if it was skipped on
// sight.
//
// @route("POST /api/cards/unskip", summary="Take a word off the skipped list",
//     tag="Training", errors=(404,),
// )
func ( handlers *Handlers ) postUnskip( c fiber.Ctx , in *wordRequest ) ( reply *unskipReply , err error ) {
	user := security.UserFrom( c )
	word , found := handlers.Corpus.Lookup( in.Word )
	if found == false {
		err = refuse( fiber.StatusNotFound , "no such word" )
		return
	}
	card , removed , unskip_err := models.Unskip( handlers.Store , user.ID , word.Word , time.Now().UTC() )
	if unskip_err != nil {
		err = progressNotFound( unskip_err )
		return
	}
	if removed {
		reply = &unskipReply{ Card: viewFromWord( word , nil ) , ReturnedToNew: true }
		return
	}
	reply = &unskipReply{ Card: viewFromWord( word , card ) , ReturnedToNew: false }
	return
}

// postForget drops a card entirely, putting the word back in the draw.
//
// @route("POST /api/cards/forget", summary="Forget a word",
//     tag="Training",
//     description="Drop everything recorded about a word, so it can be drawn as new again.",
// )
func ( handlers *Handlers ) postForget( c fiber.Ctx , in *wordRequest ) ( reply *okReply , err error ) {
	user := security.UserFrom( c )
	if err = models.ForgetCard( handlers.Store , user.ID , in.Word ); err != nil { return }
	reply = &okReply{ Ok: true }
	return
}

type searchRequest struct {
	Q string `json:"q,omitempty" query:"q" jsonschema:"maxLength=100" jsonschema_description:"The spelling to look for." error:"q is too long"`
}

type searchReply struct {
	Cards []cardView `json:"cards"`
}

// getSearch finds words by spelling, so someone can look up a word and file
// it directly into a list.
//
// @route("GET /api/search", summary="Look a word up",
//     tag="Training",
// )
func ( handlers *Handlers ) getSearch( c fiber.Ctx , in *searchRequest ) ( reply *searchReply , err error ) {
	user := security.UserFrom( c )
	matches := handlers.Corpus.Search( in.Q , 25 )

	views := []cardView{}
	for _ , word := range matches {
		card , card_err := models.GetCard( handlers.Store , user.ID , word.Word )
		if card_err != nil { card = nil }
		views = append( views , viewFromWord( word , card ) )
	}
	reply = &searchReply{ Cards: views }
	return
}
