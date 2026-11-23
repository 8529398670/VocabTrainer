package models

import (
	errors "errors"
	time "time"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

// ProjectPortal's own account (portal contract v3): a service account the
// owner connects from the API keys screen (or `manage portal-connect`), so
// the portal never holds the owner's key. It is an admin, and never the
// owner. It cannot be signed into (startSignInTx, IssueLoginLink); it has
// keys only, each one an admin key that may make keys (MayMintKeys) and
// never sign-in links. The ladder treats it as an admin, so only the owner
// acts on it: no other admin can cut the portal off. Revoking its keys
// (DisconnectPortal) revokes everything they made, down the chain.

// DefaultPortalName is the account Connect makes when no name is given.
const DefaultPortalName = "ProjectPortal"

var (
	// ErrNotService is the refusal for disconnecting an account that is not
	// a service account.
	ErrNotService = errors.New( "that account is not a service account" )
	// ErrBadPortalName is a service account name that is not 1-80 characters.
	ErrBadPortalName = errors.New( "name must be 1-80 characters" )
)

// ConnectPortal finds the service account named name (making it when there is
// none), and gives it one more admin key that may make keys, lasting ttl.
// Only the owner may: ErrOwnerRequired for anyone else, actor 0 is a shell.
// The credential is shown once, by the caller.
func ConnectPortal( store *db.Store , actor_id uint64 , name string , ttl time.Duration ) ( credential string , key *APIKey , user *User , err error ) {
	if ValidDisplayName( name ) == false {
		err = ErrBadPortalName
		return
	}
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		owner_id , tx_err := ownerIDTx( tx )
		if tx_err != nil { return }
		// With no owner yet, the first admin made would become it.
		if owner_id == 0 || ( actor_id != 0 && actor_id != owner_id ) {
			tx_err = ErrOwnerRequired
			return
		}
		tx_err = tx.ForEach( db.BucketUsers , func() any { return &User{} } , func( _ []byte , item any ) ( more bool ) {
			found := item.( *User )
			more = true
			if found.IsService() && found.DisplayName == name {
				user , more = found , false
			}
			return
		} )
		if tx_err != nil || user != nil { return }
		user , tx_err = createUserTx( tx , name , RoleAdmin )
		if tx_err != nil { return }
		user.Kind = KindService
		tx_err = tx.Put( db.BucketUsers , encryption.Uint64ToBytes( user.ID ) , user )
		return
	} )
	if err != nil { return }
	credential , key , err = IssueAPIKey( store , user.ID , name , RoleAdmin , ttl )
	if err != nil { return }
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		stored := &APIKey{}
		if tx_err = tx.Get( db.BucketAPIKeys , []byte( key.ID ) , stored ); tx_err != nil { return }
		stored.MayMintKeys = true
		tx_err = tx.Put( db.BucketAPIKeys , []byte( key.ID ) , stored )
		key = stored
		return
	} )
	return
}

// PortalAccount is one service account and its keys.
type PortalAccount struct {
	User *User
	Keys []*APIKey
}

// PortalAccounts is every service account, oldest first, with its live keys.
func PortalAccounts( store *db.Store ) ( accounts []PortalAccount , err error ) {
	users , err := ListUsers( store )
	if err != nil { return }
	accounts = []PortalAccount{}
	for _ , user := range users {
		if user.IsService() == false { continue }
		keys , list_err := ListAPIKeysForUser( store , user.ID )
		if list_err != nil {
			err = list_err
			return
		}
		live := []*APIKey{}
		for _ , key := range keys {
			if key.Live() { live = append( live , key ) }
		}
		accounts = append( accounts , PortalAccount{ User: user , Keys: live } )
	}
	return
}

// DisconnectPortal revokes every live key of the service account user_id,
// which revokes the keys and voids the links they made. It answers how many
// of its own keys it revoked. ErrNotService for an account that is not one.
func DisconnectPortal( store *db.Store , actor_id uint64 , user_id uint64 ) ( revoked int , err error ) {
	user , err := GetUser( store , user_id )
	if err != nil { return }
	if user.IsService() == false {
		err = ErrNotService
		return
	}
	if err = MayActOn( store , actor_id , user_id ); err != nil { return }
	keys , err := ListAPIKeysForUser( store , user_id )
	if err != nil { return }
	for _ , key := range keys {
		if key.Live() == false { continue }
		if err = RevokeAPIKey( store , key.ID ); err != nil { return }
		revoked++
	}
	return
}
