package models

import (
	errors "errors"
	time "time"

	bolt "go.etcd.io/bbolt"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

var ErrLoginTokenInvalid = errors.New( "login token is invalid, expired, or already used" )

// LoginToken is this project's entire replacement for a signup form and a
// password reset flow. An admin (or the first-run bootstrap) mints one for a
// specific user; visiting the link is the login.
//
// Same "<id>.<secret>" shape as Session, but the secret is bcrypt-hashed
// rather than sha256'd. bcrypt is affordable here precisely because it runs
// once per redemption instead of once per request, and the id prefix is what
// keeps that to a single hash comparison -- hashing every candidate row would
// hand anyone a trivial way to pin the CPU.
type LoginToken struct {
	UserID     uint64     `json:"user_id"`
	SecretHash string     `json:"secret_hash"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at,omitempty"`
}

// IssueLoginToken returns the raw credential exactly once. It is never stored
// and cannot be recovered -- if it is lost, mint another one.
func IssueLoginToken( store *db.Store , user_id uint64 , ttl time.Duration ) ( credential string , err error ) {
	token_id := encryption.GenerateURLToken( 12 )
	secret := encryption.GenerateURLToken( 24 )
	secret_hash , err := encryption.BcryptHash( secret )
	if err != nil { return }

	now := time.Now().UTC()
	token := &LoginToken{
		UserID:     user_id,
		SecretHash: secret_hash,
		CreatedAt:  now,
		ExpiresAt:  now.Add( ttl ),
	}
	if err = store.Put( db.BucketLoginTokens , []byte( token_id ) , token ); err != nil { return }
	credential = token_id + "." + secret
	return
}

// RedeemLoginToken validates and consumes a token in one bolt write
// transaction. Doing the check and the mark-as-used separately would leave a
// window where two requests racing the same link both see it unused and both
// get a session; bolt gives a single writer at a time, so doing both inside
// Update closes that window for free.
func RedeemLoginToken( store *db.Store , credential string ) ( user_id uint64 , err error ) {
	token_id , secret , ok := splitCredential( credential )
	if ok == false {
		err = ErrLoginTokenInvalid
		return
	}

	err = store.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		bucket := tx.Bucket( []byte( db.BucketLoginTokens ) )
		raw := bucket.Get( []byte( token_id ) )
		if raw == nil {
			tx_err = ErrLoginTokenInvalid
			return
		}

		token := &LoginToken{}
		if decode_err := store.DecodeValue( raw , token ); decode_err != nil {
			tx_err = decode_err
			return
		}

		now := time.Now().UTC()
		if token.UsedAt != nil || now.After( token.ExpiresAt ) {
			tx_err = ErrLoginTokenInvalid
			return
		}
		if encryption.BcryptCompare( token.SecretHash , secret ) == false {
			tx_err = ErrLoginTokenInvalid
			return
		}

		token.UsedAt = &now
		encoded , encode_err := store.EncodeValue( token )
		if encode_err != nil {
			tx_err = encode_err
			return
		}
		if tx_err = bucket.Put( []byte( token_id ) , encoded ); tx_err != nil { return }

		user_id = token.UserID
		return
	} )
	return
}

// PurgeExpiredLoginTokens drops spent and stale rows. Used tokens are kept
// until they expire rather than deleted on redemption, so that a second click
// on a link fails as "already used" instead of looking like it never existed.
// One transaction, for the same reason as PurgeExpiredSessions.
func PurgeExpiredLoginTokens( store *db.Store ) ( removed int , err error ) {
	now := time.Now().UTC()
	removed , err = store.DeleteWhere( db.BucketLoginTokens ,
		func() any { return &LoginToken{} } ,
		func( key []byte , item any ) bool {
			return now.After( item.( *LoginToken ).ExpiresAt )
		} )
	return
}
