package models

import (
	sync "sync"
	time "time"
)

// AttemptGuard limits wrong guesses at anything a person types: a PIN, a
// password, a two-factor code. Six digits is a million guesses, and four is
// ten thousand, so without this those are decoration. max_failures misses
// lock a key for AttemptLockout.
//
// In memory, like the rate limiter -- so it resets on restart and is per
// replica. It is a brake on guessing, not a record of anything.
//
// # Reserve, then settle
//
// A route calls Reserve before it checks the guess and Settle after, with how
// it went. Reserve counts the attempt in flight, under the lock, and refuses
// once the misses so far plus the attempts in flight reach max_failures. So
// guesses sent in parallel cannot all slip past a check that only counts
// finished ones: twenty at once still get max_failures evaluated, no more.
// Checking Locked and calling Record afterwards has exactly that race, which
// is why the routes do not.
type AttemptGuard[ K comparable ] struct {
	mu           sync.Mutex
	max_failures int
	failures     map[ K ]int
	pending      map[ K ]int
	until        map[ K ]time.Time
}

// AttemptOutcome is what Settle is told about a reserved attempt.
type AttemptOutcome int

const (
	// AttemptVoid: the guess was never judged (a stale challenge, a server
	// error). It costs nothing.
	AttemptVoid AttemptOutcome = iota
	// AttemptFailed: a wrong guess. It counts toward the lockout.
	AttemptFailed
	// AttemptPassed: a right guess. The misses so far are forgiven.
	AttemptPassed
)

const (
	AttemptMaxFailures = 5
	AttemptLockout     = 15 * time.Minute
	// Keys can be anything a stranger typed, so what is remembered has to be
	// bounded. Past this an arbitrary count is forgotten.
	attemptMaxEntries = 10000
)

func NewAttemptGuard[ K comparable ]( max_failures int ) ( guard *AttemptGuard[ K ] ) {
	guard = &AttemptGuard[ K ]{ max_failures: max_failures , failures: map[ K ]int{} , pending: map[ K ]int{} , until: map[ K ]time.Time{} }
	return
}

// Reserve claims one attempt for key before the guess is checked, or says no
// when key is locked or its misses plus the attempts in flight have reached
// the limit. Every true must be followed by exactly one Settle.
func ( guard *AttemptGuard[ K ] ) Reserve( key K ) ( ok bool ) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if until , found := guard.until[ key ]; found {
		if time.Now().Before( until ) { return }
		delete( guard.until , key )
	}
	if guard.failures[ key ] + guard.pending[ key ] >= guard.max_failures { return }
	guard.pending[ key ]++
	ok = true
	return
}

// Settle ends an attempt Reserve claimed.
func ( guard *AttemptGuard[ K ] ) Settle( key K , outcome AttemptOutcome ) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.pending[ key ] > 1 {
		guard.pending[ key ]--
	} else {
		delete( guard.pending , key )
	}
	switch outcome {
	case AttemptPassed:
		delete( guard.failures , key )
	case AttemptFailed:
		guard.recordFailure( key )
	}
}

func ( guard *AttemptGuard[ K ] ) Locked( key K ) ( result bool ) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	until , found := guard.until[ key ]
	if found == false { return }
	result = time.Now().Before( until )
	if result == false {
		delete( guard.until , key )
	}
	return
}

// Record is the result of an attempt that was never reserved. The routes
// use Reserve and Settle instead (see the type's comment); this stays for
// code that counts outside a request.
func ( guard *AttemptGuard[ K ] ) Record( key K , ok bool ) {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if ok {
		delete( guard.failures , key )
		return
	}
	guard.recordFailure( key )
}

func ( guard *AttemptGuard[ K ] ) recordFailure( key K ) {
	guard.failures[ key ]++
	if guard.failures[ key ] >= guard.max_failures {
		guard.until[ key ] = time.Now().Add( AttemptLockout )
		delete( guard.failures , key )
	}
	guard.trim()
}

func ( guard *AttemptGuard[ K ] ) trim() {
	if len( guard.until ) > attemptMaxEntries {
		now := time.Now()
		for key , until := range guard.until {
			if until.Before( now ) {
				delete( guard.until , key )
			}
		}
	}
	for key := range guard.failures {
		if len( guard.failures ) <= attemptMaxEntries { break }
		delete( guard.failures , key )
	}
	for key := range guard.until {
		if len( guard.until ) <= attemptMaxEntries { break }
		delete( guard.until , key )
	}
}
