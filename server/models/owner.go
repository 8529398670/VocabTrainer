package models

import (
	errors "errors"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

// The owner is the one admin who outranks the others. It exists for a single
// decision the admin role cannot settle by itself: who may permanently delete
// an admin. Without it, any admin can delete any other, and the last one to
// act wins.
//
// # A marker, not a third role
//
// The owner is recorded as one id in the meta bucket rather than as a role
// beside admin and user. Roles are also what API keys carry and what
// NarrowRole compares, so a third one would spread into key scoping, role
// validation, and every role check in the app -- all to express something
// that only matters to a few account-management routes. As a marker, the
// owner is an ordinary admin everywhere else.
//
// # The invariant
//
// The owner is always an existing, enabled admin, or there is no owner
// because there are no admins. Every path keeps it: the first admin created
// becomes the owner (CreateUser), nothing may delete or disable the owner
// (DeleteUser, SetUserDisabled), and ownership only moves to another enabled
// admin (TransferOwner). That is also why the first-run bootstrap never fires
// again once an owner exists -- there is always an enabled admin.

const metaOwnerKey = "owner_user_id"

var (
	// ErrOwnerProtected is the refusal for deleting or disabling the owner.
	// Move ownership first with TransferOwner (manage transfer-owner).
	ErrOwnerProtected = errors.New( "the owner account cannot be deleted, disabled, or have its access revoked -- transfer ownership first (manage transfer-owner)" )

	// ErrOwnerRequired is the refusal for an admin who is not the owner
	// trying to act on another admin (see mayActOnTx).
	ErrOwnerRequired = errors.New( "only the owner can do this to an admin account" )

	// ErrNotAdmin is the refusal for making a non-admin, or a disabled
	// admin, the owner.
	ErrNotAdmin = errors.New( "the owner must be an enabled admin" )
	// ErrServiceAccount refuses a way in, or the owner's seat, to a service
	// account (KindService): it has API keys only, and is never signed into.
	ErrServiceAccount = errors.New( "a service account has API keys only, and is never signed into" )

	// ErrDeleteSelf is the refusal for deleting your own account, which
	// would sign you out with no record left to sign back in to.
	ErrDeleteSelf = errors.New( "you cannot delete your own account" )

	// ErrRoleSelf is the refusal for changing your own role: it could only
	// take your own admin rights away, or sign you out.
	ErrRoleSelf = errors.New( "you cannot change your own role" )

	// ErrExpirySelf is the refusal for setting your own account's expiry:
	// whoever set it would be undone by the person it was meant to stop.
	ErrExpirySelf = errors.New( "you cannot change when your own account expires" )

)

// ownerIDTx reads the marker inside the caller's transaction. Zero means no
// owner is recorded.
func ownerIDTx( tx *db.Tx ) ( owner_id uint64 , err error ) {
	err = tx.Get( db.BucketMeta , []byte( metaOwnerKey ) , &owner_id )
	if errors.Is( err , db.ErrNotFound ) {
		owner_id , err = 0 , nil
	}
	return
}

func setOwnerTx( tx *db.Tx , user_id uint64 ) ( err error ) {
	err = tx.Put( db.BucketMeta , []byte( metaOwnerKey ) , user_id )
	return
}

// refuseOwnerTx is the guard every owner-protected operation runs inside its
// own transaction, so ownership cannot move between the check and the change.
func refuseOwnerTx( tx *db.Tx , user_id uint64 ) ( err error ) {
	owner_id , err := ownerIDTx( tx )
	if err != nil { return }
	if owner_id == user_id {
		err = ErrOwnerProtected
	}
	return
}

// mayActOnTx is the one rule for an admin acting on someone else's account,
// read inside the caller's transaction so ownership cannot move between the
// check and the change:
//
//   - nobody acts on the owner (ErrOwnerProtected);
//   - only the owner acts on another admin (ErrOwnerRequired).
//
// An actor_id of zero is the operator, through the CLI or the control socket,
// and acting on your own account is your own business; neither is refused
// here. A route that also refuses acting on yourself (deleting, changing your
// own role) checks that before calling this, with its own error.
//
// Every route that changes another account goes through this -- deleting,
// role, 2FA reset, login links, disabling, revoking, ending sessions, and
// minting, revoking, deleting or restoring an API key for someone else -- so
// the rule cannot be forgotten on one door and kept on the others.
func mayActOnTx( tx *db.Tx , actor_id uint64 , target *User ) ( err error ) {
	if actor_id == 0 || actor_id == target.ID { return }
	owner_id , err := ownerIDTx( tx )
	if err != nil { return }
	if target.ID == owner_id {
		err = ErrOwnerProtected
		return
	}
	if target.IsAdmin() && actor_id != owner_id {
		err = ErrOwnerRequired
	}
	return
}

// mayActOnIDTx is mayActOnTx for a caller holding only the target's id. A
// missing account is db.ErrNotFound.
func mayActOnIDTx( tx *db.Tx , actor_id uint64 , user_id uint64 ) ( err error ) {
	if actor_id == 0 || actor_id == user_id { return }
	target := &User{}
	if err = tx.Get( db.BucketUsers , encryption.Uint64ToBytes( user_id ) , target ); err != nil { return }
	err = mayActOnTx( tx , actor_id , target )
	return
}

// MayActOn is mayActOnTx on its own read, for a route that must refuse before
// it does anything else. Where the change itself is a write, check inside it.
func MayActOn( store *db.Store , actor_id uint64 , user_id uint64 ) ( err error ) {
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		tx_err = mayActOnIDTx( tx , actor_id , user_id )
		return
	} )
	return
}

// OwnerID returns the owner's user id, or zero when there is none.
func OwnerID( store *db.Store ) ( owner_id uint64 , err error ) {
	err = store.Get( db.BucketMeta , []byte( metaOwnerKey ) , &owner_id )
	if errors.Is( err , db.ErrNotFound ) {
		owner_id , err = 0 , nil
	}
	return
}

// EnsureOwner adopts an owner for a database that predates the marker: the
// lowest-id enabled admin, which on any database that started from the
// first-run bootstrap is the account that bootstrap created. It is a no-op
// once an owner is recorded, so it is safe to call on every boot.
func EnsureOwner( store *db.Store ) ( owner_id uint64 , err error ) {
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		if owner_id , tx_err = ownerIDTx( tx ); tx_err != nil || owner_id != 0 { return }
		tx_err = tx.ForEach( db.BucketUsers ,
			func() any { return &User{} } ,
			func( key []byte , item any ) bool {
				user := item.( *User )
				if user.IsAdmin() && user.Disabled() == false {
					owner_id = user.ID
					return false // key order is id order, so the first match is the lowest
				}
				return true
			} )
		if tx_err != nil || owner_id == 0 { return }
		tx_err = setOwnerTx( tx , owner_id )
		return
	} )
	if err != nil { owner_id = 0 }
	return
}

// TransferOwner hands ownership to another enabled admin. It is reachable
// only from the CLI and the control socket: ownership is the one thing the
// web UI cannot change about itself, so a compromised admin session cannot
// promote itself past the other admins.
func TransferOwner( store *db.Store , user_id uint64 ) ( err error ) {
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		target := &User{}
		if tx_err = tx.Get( db.BucketUsers , encryption.Uint64ToBytes( user_id ) , target ); tx_err != nil { return }
		if target.IsService() {
			tx_err = ErrServiceAccount
			return
		}
		if target.IsAdmin() == false || target.Disabled() {
			tx_err = ErrNotAdmin
			return
		}
		// The owner never expires (SetUserExpiry), so the new one stops having
		// an expiry here.
		if target.ExpiresAt != nil {
			target.ExpiresAt = nil
			if tx_err = tx.Put( db.BucketUsers , encryption.Uint64ToBytes( user_id ) , target ); tx_err != nil { return }
		}
		tx_err = setOwnerTx( tx , user_id )
		return
	} )
	return
}
