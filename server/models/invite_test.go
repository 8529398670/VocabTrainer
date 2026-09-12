package models

import (
	errors "errors"
	filepath "path/filepath"
	sync "sync"
	testing "testing"
	time "time"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

// The other tests in this package are pure logic over a struct. These need a
// real bolt file, because the thing under test is the transaction: an invite's
// seat count is only trustworthy if the check and the increment cannot be
// separated, and that is a property of the store, not of the struct.
//
// Encryption is left on, matching the default configuration -- the claim path
// encodes and decodes a record inside a transaction it does not own, which is
// the part most likely to break if that wrapper ever changes.
func newInviteStore( t *testing.T ) ( store *db.Store ) {
	t.Helper()
	cfg := &config.Config{
		DatabasePath:  filepath.Join( t.TempDir() , "test.db" ),
		SecretKey:     encryption.GenerateKeyHex(),
		EncryptAtRest: true,
	}
	store , err := db.Open( cfg )
	if err != nil { t.Fatalf( "could not open test database: %v" , err ) }
	t.Cleanup( func() { store.Close() } )
	return
}

func TestInviteAdmitsExactlyItsSeatCount( t *testing.T ) {
	store := newInviteStore( t )
	_ , credential , err := IssueInvite( store , 1 , RoleUser , "group chat" , 3 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	for seat := 1; seat <= 3; seat += 1 {
		user , claim_err := ClaimInvite( store , credential , "Joiner" )
		if claim_err != nil { t.Fatalf( "seat %d was refused: %v" , seat , claim_err ) }
		if user == nil { t.Fatalf( "seat %d returned no user" , seat ) }
		if user.Role != RoleUser { t.Errorf( "seat %d got role %q, wanted %q" , seat , user.Role , RoleUser ) }
	}

	// The fourth is the whole point of the feature.
	if _ , claim_err := ClaimInvite( store , credential , "One Too Many" ); errors.Is( claim_err , ErrInviteFull ) == false {
		t.Errorf( "fourth claim returned %v, wanted ErrInviteFull" , claim_err )
	}
}

// The reason ClaimInvite re-checks inside its write transaction rather than
// trusting what InspectInvite just told it. Everyone races for the last seat
// at once; bolt serialises the writers, so exactly one may win.
func TestConcurrentClaimsCannotOverspendTheLastSeat( t *testing.T ) {
	store := newInviteStore( t )
	_ , credential , err := IssueInvite( store , 1 , RoleUser , "" , 1 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	const claimants = 8
	var wait sync.WaitGroup
	var mutex sync.Mutex
	granted := []uint64{}

	start := make( chan struct{} )
	for index := 0; index < claimants; index += 1 {
		wait.Add( 1 )
		go func() {
			defer wait.Done()
			<-start
			user , claim_err := ClaimInvite( store , credential , "Racer" )
			if claim_err != nil { return }
			mutex.Lock()
			granted = append( granted , user.ID )
			mutex.Unlock()
		}()
	}
	close( start )
	wait.Wait()

	if len( granted ) != 1 {
		t.Fatalf( "%d of %d claimants got in on a one-seat invite, wanted exactly 1" , len( granted ) , claimants )
	}

	// And the seat that was spent is the one that was recorded: a user
	// without a seat, or a seat without a user, would both show up here.
	users , list_err := ListUsers( store )
	if list_err != nil { t.Fatalf( "could not list users: %v" , list_err ) }
	if len( users ) != 1 {
		t.Errorf( "%d users exist after one successful claim" , len( users ) )
	}
}

// Opening the link has to be free, or every chat app's link preview spends a
// seat before a person sees the page. See routes/invite.go.
func TestInspectingAnInviteSpendsNothing( t *testing.T ) {
	store := newInviteStore( t )
	_ , credential , err := IssueInvite( store , 1 , RoleUser , "" , 2 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	for probe := 0; probe < 5; probe += 1 {
		invite , inspect_err := InspectInvite( store , credential )
		if inspect_err != nil { t.Fatalf( "inspect %d failed: %v" , probe , inspect_err ) }
		if invite.UsedCount != 0 { t.Fatalf( "inspect %d had spent %d seats" , probe , invite.UsedCount ) }
		if invite.SeatsLeft() != 2 { t.Fatalf( "inspect %d reported %d seats left, wanted 2" , probe , invite.SeatsLeft() ) }
	}

	users , _ := ListUsers( store )
	if len( users ) != 0 { t.Errorf( "inspecting created %d users" , len( users ) ) }
}

// A wrong secret must be indistinguishable from a link that never existed:
// invite comes back nil, so a caller has nothing to report but "invalid".
func TestWrongSecretRevealsNothing( t *testing.T ) {
	store := newInviteStore( t )
	invite_id , _ , err := IssueInvite( store , 1 , RoleUser , "" , 3 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	for _ , credential := range []string{
		invite_id + ".wrong-secret",         // real id, wrong secret
		"no-such-invite.wrong-secret",       // nothing to find
		"malformed-without-a-dot",           // not even the right shape
		"",                                  // empty
	} {
		invite , inspect_err := InspectInvite( store , credential )
		if invite != nil {
			t.Errorf( "credential %q returned an invite record" , credential )
		}
		if errors.Is( inspect_err , ErrInviteInvalid ) == false {
			t.Errorf( "credential %q returned %v, wanted ErrInviteInvalid" , credential , inspect_err )
		}
		if _ , claim_err := ClaimInvite( store , credential , "Nobody" ); claim_err == nil {
			t.Errorf( "credential %q was claimable" , credential )
		}
	}
}

func TestRevokedInviteStopsWorkingAndSaysSo( t *testing.T ) {
	store := newInviteStore( t )
	invite_id , credential , err := IssueInvite( store , 1 , RoleUser , "" , 3 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	if _ , claim_err := ClaimInvite( store , credential , "First In" ); claim_err != nil {
		t.Fatalf( "first claim failed: %v" , claim_err )
	}
	if revoke_err := RevokeInvite( store , invite_id ); revoke_err != nil {
		t.Fatalf( "could not revoke: %v" , revoke_err )
	}

	if _ , claim_err := ClaimInvite( store , credential , "Too Late" ); errors.Is( claim_err , ErrInviteRevoked ) == false {
		t.Errorf( "claim after revoke returned %v, wanted ErrInviteRevoked" , claim_err )
	}

	// Revocation is a mark, not a delete: whoever came in through the link
	// is still on the record, and the seats they took are still counted.
	invite , get_err := GetInvite( store , invite_id )
	if get_err != nil { t.Fatalf( "revoked invite is gone: %v" , get_err ) }
	if invite.UsedCount != 1 { t.Errorf( "used count = %d, wanted 1" , invite.UsedCount ) }
	if len( invite.JoinedUserIDs ) != 1 { t.Errorf( "%d joiners recorded, wanted 1" , len( invite.JoinedUserIDs ) ) }

	// Revoking twice keeps the original timestamp.
	first_revoked := *invite.RevokedAt
	if revoke_err := RevokeInvite( store , invite_id ); revoke_err != nil {
		t.Fatalf( "second revoke failed: %v" , revoke_err )
	}
	again , _ := GetInvite( store , invite_id )
	if again.RevokedAt.Equal( first_revoked ) == false {
		t.Errorf( "revoked at moved from %v to %v" , first_revoked , *again.RevokedAt )
	}
}

func TestExpiredInviteIsRefusedButStillExplainsItself( t *testing.T ) {
	store := newInviteStore( t )
	invite_id , credential , err := IssueInvite( store , 1 , RoleUser , "" , 3 , -time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	invite , inspect_err := InspectInvite( store , credential )
	if invite == nil { t.Fatal( "an expired invite should still be described to whoever holds it" ) }
	if errors.Is( inspect_err , ErrInviteExpired ) == false {
		t.Errorf( "inspect returned %v, wanted ErrInviteExpired" , inspect_err )
	}
	if _ , claim_err := ClaimInvite( store , credential , "Late" ); errors.Is( claim_err , ErrInviteExpired ) == false {
		t.Errorf( "claim returned %v, wanted ErrInviteExpired" , claim_err )
	}

	// Only now is the row cleared, which is what stops an expired link from
	// being reported as one that never existed while it is still in hand.
	removed , purge_err := PurgeExpiredInvites( store )
	if purge_err != nil { t.Fatalf( "purge failed: %v" , purge_err ) }
	if removed != 1 { t.Errorf( "purge removed %d rows, wanted 1" , removed ) }
	if _ , get_err := GetInvite( store , invite_id ); get_err == nil {
		t.Error( "the expired invite survived the purge" )
	}
}

// A full or revoked invite is kept until it expires on purpose: the fourth
// person to click a three-seat link should be told it is full.
func TestFullInviteIsKeptUntilItExpires( t *testing.T ) {
	store := newInviteStore( t )
	_ , credential , err := IssueInvite( store , 1 , RoleUser , "" , 1 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }
	if _ , claim_err := ClaimInvite( store , credential , "Only One" ); claim_err != nil {
		t.Fatalf( "claim failed: %v" , claim_err )
	}

	removed , purge_err := PurgeExpiredInvites( store )
	if purge_err != nil { t.Fatalf( "purge failed: %v" , purge_err ) }
	if removed != 0 { t.Errorf( "purge removed a full but unexpired invite" ) }

	invite , inspect_err := InspectInvite( store , credential )
	if invite == nil { t.Fatal( "a full invite should still be described to whoever holds it" ) }
	if errors.Is( inspect_err , ErrInviteFull ) == false {
		t.Errorf( "inspect returned %v, wanted ErrInviteFull" , inspect_err )
	}
}

// The role is fixed when the invite is minted, not chosen by whoever redeems
// it: the link grants exactly what the admin decided to hand out.
func TestClaimedAccountTakesTheInvitesRole( t *testing.T ) {
	store := newInviteStore( t )
	_ , credential , err := IssueInvite( store , 1 , RoleAdmin , "" , 1 , time.Hour )
	if err != nil { t.Fatalf( "could not issue invite: %v" , err ) }

	user , claim_err := ClaimInvite( store , credential , "  Padded Name  " )
	if claim_err != nil { t.Fatalf( "claim failed: %v" , claim_err ) }
	if user.Role != RoleAdmin { t.Errorf( "role = %q, wanted %q" , user.Role , RoleAdmin ) }
	if user.DisplayName != "Padded Name" { t.Errorf( "display name = %q, wanted it trimmed" , user.DisplayName ) }

	// And it is a real stored user, not just the struct that came back.
	stored , get_err := GetUser( store , user.ID )
	if get_err != nil { t.Fatalf( "claimed user was not stored: %v" , get_err ) }
	if stored.DisplayName != "Padded Name" { t.Errorf( "stored name = %q" , stored.DisplayName ) }
}

func TestInviteSeatBoundsAreEnforcedAtTheEdge( t *testing.T ) {
	for _ , row := range []struct{ uses int; valid bool }{
		{ 0 , false } , { -1 , false } , { InviteMinUses , true } ,
		{ 3 , true } , { InviteMaxUses , true } , { InviteMaxUses + 1 , false },
	} {
		if ValidInviteUses( row.uses ) != row.valid {
			t.Errorf( "ValidInviteUses(%d) = %v, wanted %v" , row.uses , !row.valid , row.valid )
		}
	}
}
