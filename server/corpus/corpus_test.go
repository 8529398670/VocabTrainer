package corpus

import (
	rand "math/rand"
	testing "testing"
)

func load( t *testing.T ) ( corpus *Corpus ) {
	t.Helper()
	corpus , err := Load()
	if err != nil { t.Fatalf( "Load: %v" , err ) }
	return
}

func TestLoadPopulatesEveryTier( t *testing.T ) {
	corpus := load( t )
	if corpus.Size() < 10000 {
		t.Fatalf( "only %d words loaded -- has cmd/builddata been run?" , corpus.Size() )
	}
	for tier := TierMin; tier <= TierMax; tier += 1 {
		if corpus.CountByTier( tier ) == 0 {
			t.Errorf( "tier %d is empty; the level picker would offer a level with nothing in it" , tier )
		}
	}
}

func TestDrawStaysNearTheRequestedLevel( t *testing.T ) {
	corpus := load( t )
	random := rand.New( rand.NewSource( 1 ) )

	for _ , tier := range []int{ TierMin , 8 , TierMax } {
		words := corpus.Draw( tier , 20 , nil , random )
		if len( words ) != 20 {
			t.Fatalf( "tier %d: drew %d words, wanted 20" , tier , len( words ) )
		}
		seen := map[string]bool{}
		for _ , word := range words {
			if seen[ word.Word ] {
				t.Errorf( "tier %d: %q drawn twice in one batch" , tier , word.Word )
			}
			seen[ word.Word ] = true

			distance := word.Tier - tier
			if distance < -1 || distance > 1 {
				t.Errorf( "tier %d: drew %q from tier %d, more than one level away" , tier , word.Word , word.Tier )
			}
		}
	}
}

// drawFromTier walks a tier with a random stride, which only reaches the
// whole pool when the stride is coprime with its length -- a stride of 2 over
// an even-sized tier visits half the words and never the other half.
//
// This tests drawFromTier rather than Draw on purpose. Draw calls it several
// times with a fresh stride each time, which hides the problem well enough
// that a test at that level passes either way; the invariant belongs to the
// walk itself. Tier 1 has an even number of words, so the bad strides are
// reachable.
func TestDrawFromTierReachesEveryWord( t *testing.T ) {
	corpus := load( t )

	const tier = TierMin
	pool := corpus.byTier[ tier ]
	if len( pool ) < 100 { t.Fatalf( "tier %d too small to be a useful test" , tier ) }

	for seed := int64( 0 ); seed < 60; seed += 1 {
		random := rand.New( rand.NewSource( seed ) )
		found := []*Word{}
		corpus.drawFromTier( tier , len( pool ) , nil , map[string]bool{} , random , &found )
		if len( found ) != len( pool ) {
			t.Fatalf( "seed %d: the walk reached %d of the %d words in tier %d" ,
				seed , len( found ) , len( pool ) , tier )
		}
	}
}

func TestLookupAndSearch( t *testing.T ) {
	corpus := load( t )

	word , found := corpus.Lookup( "  Ephemeral " )
	if found == false { t.Fatal( "Lookup should trim and lowercase its argument" ) }
	if word.Word != "ephemeral" { t.Errorf( "got %q" , word.Word ) }
	if len( word.Senses ) == 0 || word.Senses[ 0 ].Definition == "" {
		t.Error( "a word with no definition is not usable as a card" )
	}

	if _ , found = corpus.Lookup( "zzzznotaword" ); found {
		t.Error( "Lookup invented a word" )
	}

	matches := corpus.Search( "ephem" , 10 )
	if len( matches ) == 0 { t.Fatal( "Search found nothing for a real prefix" ) }
	if matches[ 0 ].Word != "ephemera" && matches[ 0 ].Word != "ephemeral" {
		t.Errorf( "prefix matches should rank first, got %q" , matches[ 0 ].Word )
	}
	if len( corpus.Search( "" , 10 ) ) != 0 {
		t.Error( "an empty query should match nothing, not everything" )
	}
}
