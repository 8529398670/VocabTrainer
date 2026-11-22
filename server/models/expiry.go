package models

import (
	errors "errors"
	time "time"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

// Account expiry: an account that works until a set time, for a guest, a
// contractor, a trial. Portal contract v2 (capability account_expiry).
//
// Once ExpiresAt passes the account counts as disabled (User.Disabled), so
// sign-in, its sessions and its keys stop at that moment, read on every
// request. The housekeeping job then makes it real (ExpireAccounts): it
// disables the account and revokes its access, as an admin would. Moving the
// expiry later does not switch an account the job already disabled back on;
// enabling it is the separate, deliberate step.

// ErrExpiryPast refuses an expiry that is not in the future: expiring an
// account now is disabling it, which has its own route.
var ErrExpiryPast = errors.New( "expires_at must be in the future" )

// CreateUserExpiring is CreateUser for an account that stops working at
// expires_at (nil for never), set in the same transaction so there is no
// moment the account exists without it.
func CreateUserExpiring( store *db.Store , display_name string , role string , expires_at *time.Time ) ( user *User , err error ) {
	if expires_at != nil && expires_at.After( time.Now() ) == false {
		err = ErrExpiryPast
		return
	}
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		if user , tx_err = createUserTx( tx , display_name , role ); tx_err != nil { return }
		if expires_at == nil { return }
		if tx_err = refuseOwnerTx( tx , user.ID ); tx_err != nil { return }
		at := expires_at.UTC()
		user.ExpiresAt = &at
		tx_err = tx.Put( db.BucketUsers , encryption.Uint64ToBytes( user.ID ) , user )
		return
	} )
	if err != nil { user = nil }
	return
}

// SetUserExpiry sets when an account stops working, nil for never. It must
// be in the future (ErrExpiryPast). The owner never expires
// (ErrOwnerProtected), and only the owner sets an admin's (mayActOnTx).
func SetUserExpiry( store *db.Store , user_id uint64 , actor_id uint64 , expires_at *time.Time ) ( user *User , err error ) {
	if expires_at != nil && expires_at.After( time.Now() ) == false {
		err = ErrExpiryPast
		return
	}
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		key := encryption.Uint64ToBytes( user_id )
		found := &User{}
		if tx_err = tx.Get( db.BucketUsers , key , found ); tx_err != nil { return }
		if tx_err = refuseOwnerTx( tx , user_id ); tx_err != nil { return }
		if tx_err = mayActOnTx( tx , actor_id , found ); tx_err != nil { return }
		found.ExpiresAt = nil
		if expires_at != nil {
			at := expires_at.UTC()
			found.ExpiresAt = &at
		}
		if tx_err = tx.Put( db.BucketUsers , key , found ); tx_err != nil { return }
		user = found
		return
	} )
	if err != nil { user = nil }
	return
}

// ExpireAccounts is the housekeeping half of expiry: every account whose
// expiry has passed and that is not yet disabled is disabled, and its access
// revoked (RevokeAllCredentials): sessions, login links, password, API keys,
// and what those keys made. It answers how many it expired. One account
// failing does not stop the others.
func ExpireAccounts( store *db.Store ) ( expired int , err error ) {
	users , err := ListUsers( store )
	if err != nil { return }
	owner_id , err := OwnerID( store )
	if err != nil { return }
	for _ , user := range users {
		if user.Expired() == false || user.DisabledAt != nil || user.ID == owner_id { continue }
		if disable_err := SetUserDisabled( store , user.ID , true ); disable_err != nil {
			err = errors.Join( err , disable_err )
			continue
		}
		if _ , revoke_err := RevokeAllCredentials( store , user.ID , false ); revoke_err != nil {
			err = errors.Join( err , revoke_err )
			continue
		}
		expired += 1
	}
	return
}
