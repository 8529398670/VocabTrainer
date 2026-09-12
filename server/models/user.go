// Package models holds one file per stored record type. There is no ORM and
// no query builder -- each file is a small set of plain functions over the db
// store, which is enough for a bolt key/value file and keeps the storage
// layer readable end to end.
package models

import (
	strings "strings"
	time "time"

	bolt "go.etcd.io/bbolt"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// User has no password field, and adding one would undo the entire design
// here: identity is proven only by redeeming a login link or presenting a
// session cookie. See references/architecture.md before changing that.
type User struct {
	ID          uint64     `json:"id"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	CreatedAt   time.Time  `json:"created_at"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
}

func ( user *User ) Disabled() ( result bool ) {
	result = user.DisabledAt != nil
	return
}

func ( user *User ) IsAdmin() ( result bool ) {
	result = user.Role == RoleAdmin
	return
}

func ValidRole( role string ) ( result bool ) {
	result = role == RoleAdmin || role == RoleUser
	return
}

// ValidDisplayName bounds the one piece of free text a user controls. The cap
// is about keeping the database and the UI sane; escaping for display is the
// frontend's job (see static/js/dom.js, which never uses innerHTML for
// user-supplied values).
func ValidDisplayName( display_name string ) ( result bool ) {
	trimmed := strings.TrimSpace( display_name )
	result = len( trimmed ) >= 1 && len( trimmed ) <= 80
	return
}

func CreateUser( store *db.Store , display_name string , role string ) ( user *User , err error ) {
	next_id , err := store.NextSequence( db.BucketUsers )
	if err != nil { return }
	user = &User{
		ID:          next_id,
		DisplayName: strings.TrimSpace( display_name ),
		Role:        role,
		CreatedAt:   time.Now().UTC(),
	}
	err = store.Put( db.BucketUsers , encryption.Uint64ToBytes( user.ID ) , user )
	if err != nil { user = nil }
	return
}

// createUserTx is CreateUser's body running inside a transaction the caller
// already holds.
//
// Claiming an invite has to create the account and spend the seat as one
// indivisible step, and every Store helper -- Put, NextSequence -- opens a
// write transaction of its own. Calling one from inside another would block
// forever on bolt's single writer, so the one operation that needs both has
// to be handed the transaction instead.
//
// Kept here rather than in invite.go so that "how a user record is made"
// stays answerable from this file alone: the two paths must not drift.
func createUserTx( store *db.Store , tx *bolt.Tx , display_name string , role string ) ( user *User , err error ) {
	bucket := tx.Bucket( []byte( db.BucketUsers ) )
	next_id , sequence_err := bucket.NextSequence()
	if sequence_err != nil {
		err = sequence_err
		return
	}
	candidate := &User{
		ID:          next_id,
		DisplayName: strings.TrimSpace( display_name ),
		Role:        role,
		CreatedAt:   time.Now().UTC(),
	}
	encoded , encode_err := store.EncodeValue( candidate )
	if encode_err != nil {
		err = encode_err
		return
	}
	if err = bucket.Put( encryption.Uint64ToBytes( candidate.ID ) , encoded ); err != nil {
		return
	}
	user = candidate
	return
}

func GetUser( store *db.Store , user_id uint64 ) ( user *User , err error ) {
	user = &User{}
	err = store.Get( db.BucketUsers , encryption.Uint64ToBytes( user_id ) , user )
	if err != nil { user = nil }
	return
}

func ListUsers( store *db.Store ) ( users []*User , err error ) {
	users = []*User{}
	err = store.ForEach( db.BucketUsers ,
		func() any { return &User{} } ,
		func( key []byte , item any ) bool {
			users = append( users , item.( *User ) )
			return true
		} )
	return
}

func SaveUser( store *db.Store , user *User ) ( err error ) {
	err = store.Put( db.BucketUsers , encryption.Uint64ToBytes( user.ID ) , user )
	return
}

// RenameUser and SetUserDisabled both edit a record that already exists, so
// they go through UpdateValue rather than GetUser followed by SaveUser. The
// read and the write land in one transaction, which is what stops two
// concurrent admin requests from each reading the old row and the slower one
// silently discarding the other's change.
func RenameUser( store *db.Store , user_id uint64 , display_name string ) ( err error ) {
	err = store.UpdateValue( db.BucketUsers , encryption.Uint64ToBytes( user_id ) ,
		func() any { return &User{} } ,
		func( item any ) ( mutate_err error ) {
			item.( *User ).DisplayName = strings.TrimSpace( display_name )
			return
		} )
	return
}

func SetUserDisabled( store *db.Store , user_id uint64 , disabled bool ) ( err error ) {
	err = store.UpdateValue( db.BucketUsers , encryption.Uint64ToBytes( user_id ) ,
		func() any { return &User{} } ,
		func( item any ) ( mutate_err error ) {
			user := item.( *User )
			if disabled {
				now := time.Now().UTC()
				user.DisabledAt = &now
			} else {
				user.DisabledAt = nil
			}
			return
		} )
	return
}

// AnyAdminExists decides whether the server prints a first-run login link on
// boot. Once one admin exists, that bootstrap path stays closed.
func AnyAdminExists( store *db.Store ) ( result bool , err error ) {
	err = store.ForEach( db.BucketUsers ,
		func() any { return &User{} } ,
		func( key []byte , item any ) bool {
			user := item.( *User )
			if user.IsAdmin() && user.Disabled() == false {
				result = true
				return false
			}
			return true
		} )
	return
}
