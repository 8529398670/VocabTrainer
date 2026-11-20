package models

import (
	time "time"

	db "vocabtrainer/server/db"
)

// The one place a proven first factor turns into a signed-in browser.
//
// A login link, an invite link and a password all end here, inside the
// transaction that spent them. An account with two-factor on gets a
// short-lived challenge instead of a session, so a way of signing in cannot
// forget to ask for the code: there is no other path to a session for an
// existing account. (Joining through an invite link makes a new account,
// which has no 2FA yet.)
//
// A challenge is shaped "<id>.<secret>" like every other credential, and it
// is sha256'd for the same reason: the secret is random, there is nothing to
// brute force. It lives for ChallengeTTL and is used once.


// SignInRules is what decides how a sign-in ends, from the running
// configuration.
type SignInRules struct {
	SessionTTL time.Duration
	// MintedByKey is the API key that made the link being redeemed, carried
	// to the session (and through a two-factor challenge) so revoking the
	// key ends it. RedeemLoginToken fills it in; callers leave it empty.
	MintedByKey string
	// IssuedBy is the admin who reissued that link (LoginToken.IssuedBy),
	// carried the same way. RedeemLoginToken fills it in too.
	IssuedBy uint64
}

// SignIn is a first factor that was accepted. Exactly one of
// SessionCredential and Challenge is set.
type SignIn struct {
	UserID            uint64
	SessionCredential string
	// IssuedBy: see SignInRules.IssuedBy. The route logs it.
	IssuedBy uint64
}



// startSignInTx stores a session, or a challenge if this account has to give
// a code.
func startSignInTx( tx *db.Tx , user *User , rules SignInRules ) ( signed *SignIn , err error ) {
	session_id , credential , session := newSession( user.ID , rules.SessionTTL )
	session.MintedByKey = rules.MintedByKey
	session.IssuedBy = rules.IssuedBy
	if err = tx.Put( db.BucketSessions , []byte( session_id ) , session ); err != nil { return }
	signed = &SignIn{ UserID: user.ID , SessionCredential: credential , IssuedBy: rules.IssuedBy }
	return
}




