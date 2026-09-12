package models

import (
	math "math"
	testing "testing"
	time "time"
)

func at( day int ) ( result time.Time ) {
	result = time.Date( 2026 , 3 , day , 9 , 0 , 0 , 0 , time.UTC )
	return
}

func apply( t *testing.T , card *Card , outcome string , when time.Time ) {
	t.Helper()
	if err := card.applyOutcome( outcome , when ); err != nil {
		t.Fatalf( "applyOutcome(%s): %v" , outcome , err )
	}
}

// Answering "I already know it" should not put the card back in tomorrow's
// pile. The fixed early intervals are what make the first few correct answers
// worth something.
func TestKnownIntervalsGrow( t *testing.T ) {
	card := &Card{ Word: "ephemeral" , Ease: startingEase }

	apply( t , card , OutcomeKnown , at( 1 ) )
	if card.Status != StatusKnown { t.Errorf( "status = %q" , card.Status ) }
	if card.IntervalDays != firstInterval {
		t.Errorf( "first answer gave %v days, wanted %v" , card.IntervalDays , firstInterval )
	}
	if card.DueAt.Before( at( 1 ).Add( 3*24*time.Hour ) ) {
		t.Errorf( "due %v is too soon after a confident first answer" , card.DueAt )
	}

	apply( t , card , OutcomeKnown , at( 5 ) )
	if card.IntervalDays != secondInterval {
		t.Errorf( "second answer gave %v days, wanted %v" , card.IntervalDays , secondInterval )
	}

	// From the third answer on the interval is multiplied by the ease, which
	// has been drifting up with each success.
	easeBefore := card.Ease
	apply( t , card , OutcomeKnown , at( 15 ) )
	expected := secondInterval * easeBefore
	if math.Abs( card.IntervalDays-expected ) > 0.001 {
		t.Errorf( "third answer gave %v days, wanted %v" , card.IntervalDays , expected )
	}
	if card.Ease <= easeBefore { t.Error( "ease should rise with a correct answer" ) }
}

// A lapse has to come back inside the same session. A card due "tomorrow"
// after being missed is a card the user never relearns.
func TestUnknownSchedulesARetryInTheSameSession( t *testing.T ) {
	card := &Card{ Word: "obfuscate" , Ease: startingEase }
	apply( t , card , OutcomeKnown , at( 1 ) )
	apply( t , card , OutcomeKnown , at( 5 ) )

	easeBefore := card.Ease
	now := at( 15 )
	apply( t , card , OutcomeUnknown , now )

	if card.Status != StatusUnknown { t.Errorf( "status = %q" , card.Status ) }
	if card.Lapses != 1 { t.Errorf( "lapses = %d" , card.Lapses ) }
	if card.Reps != 0 { t.Errorf( "reps should restart after a lapse, got %d" , card.Reps ) }
	if card.Ease >= easeBefore { t.Error( "ease should fall after a lapse" ) }
	if got := card.DueAt.Sub( now ); got != lapseDelay {
		t.Errorf( "due in %v, wanted %v" , got , lapseDelay )
	}
	if card.Due( now.Add( lapseDelay+time.Minute ) ) == false {
		t.Error( "the card should be due again once the delay has passed" )
	}
}

func TestEaseStaysInRange( t *testing.T ) {
	card := &Card{ Word: "gregarious" , Ease: startingEase }
	for round := 0; round < 40; round += 1 {
		apply( t , card , OutcomeUnknown , at( 1 ) )
	}
	if card.Ease < minimumEase {
		t.Errorf( "ease fell to %v, below the floor of %v" , card.Ease , minimumEase )
	}
	for round := 0; round < 40; round += 1 {
		apply( t , card , OutcomeKnown , at( 1 ) )
	}
	if card.Ease > maximumEase {
		t.Errorf( "ease rose to %v, above the ceiling of %v" , card.Ease , maximumEase )
	}
	if card.IntervalDays > maximumInterval {
		t.Errorf( "interval reached %v days, past the cap of %v" , card.IntervalDays , maximumInterval )
	}
}

// Skipping is not a judgement, so it must not be treated as one: the card
// leaves rotation and remembers where it came from.
func TestSkipRemembersWhereTheCardCameFrom( t *testing.T ) {
	card := &Card{ Word: "lapwing" , Ease: startingEase }
	apply( t , card , OutcomeUnknown , at( 1 ) )

	before := card.DueAt
	apply( t , card , OutcomeSkip , at( 2 ) )

	if card.Status != StatusSkipped { t.Errorf( "status = %q" , card.Status ) }
	if card.PreviousStatus != StatusUnknown {
		t.Errorf( "previous status = %q, wanted %q" , card.PreviousStatus , StatusUnknown )
	}
	if card.DueAt != before {
		t.Error( "skipping should leave the schedule alone so unskipping resumes it" )
	}
	if card.Due( at( 30 ) ) {
		t.Error( "a skipped card is never due -- that is what taking it out of rotation means" )
	}

	// Skipping twice must not overwrite the remembered origin with "skipped".
	apply( t , card , OutcomeSkip , at( 3 ) )
	if card.PreviousStatus != StatusUnknown {
		t.Errorf( "a second skip lost the origin: %q" , card.PreviousStatus )
	}
}

func TestUnrecognisedOutcomeIsRejected( t *testing.T ) {
	card := &Card{ Word: "heuristic" , Ease: startingEase }
	if err := card.applyOutcome( "maybe" , at( 1 ) ); err != ErrUnknownOutcome {
		t.Errorf( "got %v, wanted ErrUnknownOutcome" , err )
	}
}

func TestStreakSurvivesACheckTheNextDay( t *testing.T ) {
	today := at( 10 )
	days := []*DayStat{
		{ Date: DateKey( at( 7 ) ) , Known: 3 },
		{ Date: DateKey( at( 8 ) ) , Known: 1 },
		{ Date: DateKey( at( 9 ) ) , Unknown: 2 },
	}

	// Nothing done today yet, but yesterday counts -- a streak that died at
	// midnight would punish someone who studies each evening.
	if got := Streak( days , today ); got != 3 {
		t.Errorf( "streak = %d, wanted 3" , got )
	}

	days = append( days , &DayStat{ Date: DateKey( today ) , Skipped: 1 } )
	if got := Streak( days , today ); got != 4 {
		t.Errorf( "streak = %d after studying today, wanted 4" , got )
	}

	// A whole empty day does break it.
	if got := Streak( days , at( 12 ) ); got != 0 {
		t.Errorf( "streak = %d after a missed day, wanted 0" , got )
	}

	// A day with only a skip still counts as activity, but an empty record
	// does not.
	quiet := []*DayStat{ { Date: DateKey( at( 9 ) ) } }
	if got := Streak( quiet , today ); got != 0 {
		t.Errorf( "streak = %d for a day with no reviews, wanted 0" , got )
	}
}
