package models

import (
	sync "sync"
	testing "testing"
)

// S4: guesses sent in parallel are reserved before they are judged, so twenty
// at once still get at most AttemptMaxFailures evaluated.
func TestParallelGuessesCannotPassTheLimit( t *testing.T ) {
	guard := NewAttemptGuard[ uint64 ]( AttemptMaxFailures )
	start := make( chan struct{} )
	var evaluated sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for range 20 {
		evaluated.Add( 1 )
		go func() {
			defer evaluated.Done()
			<-start
			if guard.Reserve( 7 ) == false { return }
			mu.Lock()
			count++
			mu.Unlock()
			guard.Settle( 7 , AttemptFailed )
		}()
	}
	close( start )
	evaluated.Wait()
	if count > AttemptMaxFailures { t.Fatalf( "%d guesses were evaluated, limit %d" , count , AttemptMaxFailures ) }
	if guard.Reserve( 7 ) { t.Fatal( "not locked after the limit" ) }
}

func TestARightGuessForgivesAndAVoidOneCostsNothing( t *testing.T ) {
	guard := NewAttemptGuard[ uint64 ]( 3 )
	for range 2 {
		guard.Reserve( 1 )
		guard.Settle( 1 , AttemptFailed )
	}
	guard.Reserve( 1 )
	guard.Settle( 1 , AttemptPassed )
	for range 10 {
		if guard.Reserve( 1 ) == false { t.Fatal( "a void attempt was counted" ) }
		guard.Settle( 1 , AttemptVoid )
	}
	for range 2 {
		guard.Reserve( 1 )
		guard.Settle( 1 , AttemptFailed )
	}
	if guard.Reserve( 1 ) == false { t.Fatal( "a right guess did not forgive the misses before it" ) }
	guard.Settle( 1 , AttemptFailed )
	if guard.Reserve( 1 ) { t.Fatal( "three misses did not lock" ) }
}
