package models

import (
	errors "errors"
	testing "testing"

)

// Deleting an API key or invite link is undoable for RestoreWindow (see
// soft_delete.go). What matters, and what these pin down: a deleted row is
// dead at once, only the right people can bring it back, only for a while,
// and the purge removes it once the window has passed.






func TestRenameExpect( t *testing.T ) {
	store := testStore( t )
	alice , _ := CreateUser( store , "Alice" , RoleUser )
	expect := "Alice"
	if err := RenameUser( store , alice.ID , "Alice B" , &expect ); err != nil { t.Fatal( err ) }
	stale := "Alice C"
	err := RenameUser( store , alice.ID , "Alice" , &stale )
	var refusal *RenameConflictError
	if errors.As( err , &refusal ) == false || refusal.Current != "Alice B" {
		t.Fatalf( "a stale expect: got %v" , err )
	}
	if loaded , _ := GetUser( store , alice.ID ); loaded.DisplayName != "Alice B" {
		t.Fatal( "a refused rename still changed the name" )
	}
}
