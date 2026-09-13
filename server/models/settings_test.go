package models

import (
	testing "testing"
)

// A record written before the reveal hold was configurable stored it in
// AutoAdvanceMs, where zero meant "wait for a tap". Zero now means the
// opposite, so the upgrade has to translate rather than carry the value over
// -- otherwise everyone who read the answer at their own pace would find the
// app had silently stopped showing it.
func TestMigrationTranslatesTheOldWaitForATap( t *testing.T ) {
	stored := &Settings{ AutoAdvanceMs: 0 } // schema 0: "until I tap"
	stored.normalise()

	if stored.RevealHoldMs != RevealHoldUntilTap {
		t.Errorf( "reveal hold = %d, wanted %d (until the next answer)" , stored.RevealHoldMs , RevealHoldUntilTap )
	}
	if stored.Schema != settingsSchema {
		t.Errorf( "schema = %d, wanted %d" , stored.Schema , settingsSchema )
	}
	if stored.AutoAdvanceMs != 0 {
		t.Errorf( "the old field is still set to %d" , stored.AutoAdvanceMs )
	}
}

func TestMigrationKeepsAnOldDuration( t *testing.T ) {
	stored := &Settings{ AutoAdvanceMs: 1500 }
	stored.normalise()
	if stored.RevealHoldMs != 1500 {
		t.Errorf( "reveal hold = %d, wanted the 1500 that was stored" , stored.RevealHoldMs )
	}
}

// Zero arriving from the current UI means "do not show the answer at all".
// SaveSettings stamps the schema for exactly this reason: without it the
// value would look like an un-migrated record and be turned into its
// opposite.
func TestSavedZeroHoldIsNotMistakenForAnOldRecord( t *testing.T ) {
	settings := DefaultSettings()
	settings.RevealHoldMs = 0
	settings.Schema = settingsSchema
	settings.normalise()

	if settings.RevealHoldMs != 0 {
		t.Errorf( "reveal hold = %d, wanted 0 (do not show the answer)" , settings.RevealHoldMs )
	}
}

func TestRevealHoldRange( t *testing.T ) {
	cases := []struct {
		given int
		want  int
	}{
		{ given: 0 , want: 0 },
		{ given: 3000 , want: 3000 },
		{ given: maximumRevealHoldMs , want: maximumRevealHoldMs },
		{ given: RevealHoldUntilTap , want: RevealHoldUntilTap },
		{ given: -2 , want: defaultRevealHoldMs },
		{ given: maximumRevealHoldMs + 1 , want: defaultRevealHoldMs },
	}
	for _ , test := range cases {
		settings := DefaultSettings()
		settings.RevealHoldMs = test.given
		settings.normalise()
		if settings.RevealHoldMs != test.want {
			t.Errorf( "hold %d normalised to %d, wanted %d" , test.given , settings.RevealHoldMs , test.want )
		}
	}
}

// Two outcomes on one direction leaves the third unreachable by swiping, so
// the whole mapping resets rather than being patched into something the user
// did not choose.
func TestDirectionsMustBeDistinct( t *testing.T ) {
	settings := DefaultSettings()
	settings.SwipeKnown = DirectionUp
	settings.SwipeSkip = DirectionUp
	settings.normalise()

	if settings.SwipeKnown != DirectionRight || settings.SwipeUnknown != DirectionLeft || settings.SwipeSkip != DirectionUp {
		t.Errorf( "a duplicate direction left the mapping at known=%s unknown=%s skip=%s" ,
			settings.SwipeKnown , settings.SwipeUnknown , settings.SwipeSkip )
	}
}

func TestUnrecognisedDirectionResets( t *testing.T ) {
	settings := DefaultSettings()
	settings.SwipeSkip = "sideways"
	settings.normalise()
	if settings.SwipeSkip != DirectionUp {
		t.Errorf( "skip = %q, wanted the default %q" , settings.SwipeSkip , DirectionUp )
	}
}

// A mapping that uses all four directions, none repeated, is the user's to
// keep -- including putting an answer on down, which nothing used before.
func TestValidMappingIsKept( t *testing.T ) {
	settings := DefaultSettings()
	settings.SwipeUnknown = DirectionUp
	settings.SwipeSkip = DirectionDown
	settings.SwipeKnown = DirectionLeft
	settings.normalise()

	if settings.SwipeUnknown != DirectionUp || settings.SwipeSkip != DirectionDown || settings.SwipeKnown != DirectionLeft {
		t.Errorf( "a valid mapping was rewritten to known=%s unknown=%s skip=%s" ,
			settings.SwipeKnown , settings.SwipeUnknown , settings.SwipeSkip )
	}
}

func TestPracticeModeNormalises( t *testing.T ) {
	settings := DefaultSettings()
	settings.PracticeMode = "everything"
	settings.normalise()
	if settings.PracticeMode != PracticeMixed {
		t.Errorf( "practice mode = %q, wanted %q" , settings.PracticeMode , PracticeMixed )
	}

	settings.PracticeMode = PracticeUnknown
	settings.normalise()
	if settings.PracticeMode != PracticeUnknown {
		t.Errorf( "practice mode = %q, wanted it kept" , settings.PracticeMode )
	}
}

// The reveal is the point of the exercise, so excusing "I know it" from it is
// an opt-in. A record saved before the field existed decodes it as false,
// which is what makes the upgrade invisible to everyone who had one.
func TestKnownSkipsRevealIsOffUnlessAskedFor( t *testing.T ) {
	if DefaultSettings().KnownSkipsReveal {
		t.Error( "the default excuses \"I know it\" from the reveal" )
	}

	stored := &Settings{ Schema: settingsSchema } // as an older record decodes
	stored.normalise()
	if stored.KnownSkipsReveal {
		t.Error( "a record written before the field existed came back with it on" )
	}

	asked := DefaultSettings()
	asked.KnownSkipsReveal = true
	asked.normalise()
	if asked.KnownSkipsReveal == false {
		t.Error( "the choice was normalised away" )
	}
}

// The scoring mode is new, so every record that exists predates it and
// decodes it as "". That has to mean the two-answer card those records were
// saved under -- an upgrade that silently put two more buttons on everyone's
// screen would be a change nobody asked for.
func TestScoringModeDefaultsToTheTwoAnswerCard( t *testing.T ) {
	if DefaultSettings().ScoringMode != ScoringBinary {
		t.Errorf( "default scoring mode = %q, wanted %q" , DefaultSettings().ScoringMode , ScoringBinary )
	}

	stored := &Settings{ Schema: settingsSchema } // as an older record decodes
	stored.normalise()
	if stored.ScoringMode != ScoringBinary {
		t.Errorf( "an older record came back as %q, wanted %q" , stored.ScoringMode , ScoringBinary )
	}

	asked := DefaultSettings()
	asked.ScoringMode = ScoringGraded
	asked.normalise()
	if asked.ScoringMode != ScoringGraded { t.Error( "the choice was normalised away" ) }

	asked.ScoringMode = "five_buttons"
	asked.normalise()
	if asked.ScoringMode != ScoringBinary {
		t.Errorf( "an unrecognised mode became %q, wanted %q" , asked.ScoringMode , ScoringBinary )
	}
}

func TestNewOnlyPracticeModeIsKept( t *testing.T ) {
	settings := DefaultSettings()
	settings.PracticeMode = PracticeNew
	settings.normalise()
	if settings.PracticeMode != PracticeNew {
		t.Errorf( "practice mode = %q, wanted it kept" , settings.PracticeMode )
	}
}
