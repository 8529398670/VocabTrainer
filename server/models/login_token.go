package models

import (
	errors "errors"
	fmt "fmt"
	strconv "strconv"
	strings "strings"
	time "time"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

var (
	ErrLoginTokenInvalid = errors.New( "login token is invalid, expired, or already used" )
	ErrBadLoginLink      = errors.New( "max_uses must be 0 (unlimited) or 1-1000, expires_in_minutes 0 (never) or 1-525600" )
)

const (
	LoginLinkMaxUses = 1000
	LoginLinkMaxDays = 365

	// loginLinkRetention is how long a used-up link that never expires is
	// kept, so a late click on it still says "already used". A link with an
	// expiry is kept until then instead.
	loginLinkRetention = 30 * 24 * time.Hour
)

// LoginToken is this project's entire replacement for a signup form and a
// password reset flow. An admin (or the first-run bootstrap) mints one for a
// specific user; redeeming the link is the login.
//
// By default a link works once. It can instead allow up to MaxUses sign-ins,
// or any number (Unlimited), always by the same account -- a link a person
// keeps for a week, say, instead of asking for a new one on every device.
// A zero ExpiresAt means it never expires.
//
// Same "<id>.<secret>" shape as Session, but the secret is bcrypt-hashed
// rather than sha256'd. bcrypt is affordable here because it runs once per
// sign-in, never once per request, and sign-ins are on their own rate budget;
// the id prefix is what keeps that to a single hash comparison -- hashing
// every candidate row would hand anyone a trivial way to pin the CPU.
//
// Rows written before links could be reused have no MaxUses (read as 1) and
// no Uses (read from UsedAt), so they keep meaning "once".
type LoginToken struct {
	UserID     uint64     `json:"user_id"`
	SecretHash string     `json:"secret_hash"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	UsedAt     *time.Time `json:"used_at,omitempty"` // the latest sign-in
	MaxUses    int        `json:"max_uses,omitempty"`
	Unlimited  bool       `json:"unlimited,omitempty"`
	Uses       int        `json:"uses,omitempty"`
	// MintedByKey is the API key that asked for this link, empty when a
	// person did. Revoking or deleting that key voids the link while it can
	// still be used (VoidLinksMintedByKey).
	MintedByKey string `json:"minted_by_key,omitempty"`
	// IssuedBy is the admin who reissued this link for someone else's
	// account, zero when the account, the operator or account creation made
	// it. The session it makes records it too, and the sign-in is logged as
	// "signed in with a link issued by" them: an admin can sign in as anyone
	// below them this way, and the account's own log shows it.
	IssuedBy uint64 `json:"issued_by,omitempty"`
}

// LoginLinkOptions says how a new link may be used. MaxUses is ignored when
// Unlimited is set. A zero TTL means the link never expires.
type LoginLinkOptions struct {
	MaxUses   int
	Unlimited bool
	TTL       time.Duration
	// MintedByKey is the id of the API key asking, if one is. See
	// LoginToken.MintedByKey.
	MintedByKey string
	// ActorID is the account asking for a link to someone else's account,
	// checked against mayActOnTx inside the issuing transaction: a link is a
	// way in, so minting one is as much power over the account as deleting
	// it. Zero is the operator, or an account being created just now.
	ActorID uint64
}

// OnceLink is the classic one-time link.
func OnceLink( ttl time.Duration ) ( options LoginLinkOptions ) {
	options = LoginLinkOptions{ MaxUses: 1 , TTL: ttl }
	return
}

// LoginLinkRequest is what an API caller or the CLI asked for, before the
// configured defaults fill the gaps. MaxUses: nil means once, 0 unlimited.
// ExpiresInMinutes: nil means the configured default, 0 never.
type LoginLinkRequest struct {
	MaxUses          *int `json:"max_uses,omitempty" jsonschema:"minimum=0,maximum=1000" jsonschema_description:"Sign-ins the link allows: 1-1000, or 0 for unlimited. Default 1." error:"max_uses must be 0 (unlimited) or 1-1000, expires_in_minutes 0 (never) or 1-525600"`
	ExpiresInMinutes *int `json:"expires_in_minutes,omitempty" jsonschema:"minimum=0,maximum=525600" jsonschema_description:"0 never expires. Default auth.login_token_ttl_seconds for a link that works once, auth.multi_use_login_token_ttl_seconds otherwise." error:"max_uses must be 0 (unlimited) or 1-1000, expires_in_minutes 0 (never) or 1-525600"`
}

// LoginLinkInfo is a link as the UI shows it. ExpiresAt is nil when it
// never expires.
type LoginLinkInfo struct {
	MaxUses    int        `json:"max_uses" jsonschema_description:"Sign-ins it allows. 0 when unlimited."`
	Unlimited  bool       `json:"unlimited"`
	Uses       int        `json:"uses" jsonschema_description:"Sign-ins so far."`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at" jsonschema_description:"null when it never expires."`
	LastUsedAt *time.Time `json:"last_used_at"`
}

// APIName and APIDoc name it in the API docs (see server/apidoc).
func ( LoginLinkInfo ) APIName() ( name string ) { name = "LoginLink"; return }

func ( LoginLinkInfo ) APIDoc() ( text string ) { text = "A login link that can still be used. One per account at most."; return }

func ( token *LoginToken ) maxUses() ( result int ) {
	result = token.MaxUses
	if result < 1 { result = 1 }
	return
}

func ( token *LoginToken ) uses() ( result int ) {
	result = token.Uses
	if result == 0 && token.UsedAt != nil { result = 1 }
	return
}

// UsedUp is true once a limited link has had all its sign-ins.
func ( token *LoginToken ) UsedUp() ( result bool ) {
	result = token.Unlimited == false && token.uses() >= token.maxUses()
	return
}

func ( token *LoginToken ) Expired( now time.Time ) ( result bool ) {
	result = token.ExpiresAt.IsZero() == false && now.After( token.ExpiresAt )
	return
}

func ( token *LoginToken ) Usable() ( result bool ) {
	result = token.UsedUp() == false && token.Expired( time.Now().UTC() ) == false
	return
}

func ( token *LoginToken ) Info() ( info LoginLinkInfo ) {
	info = LoginLinkInfo{
		MaxUses:    token.maxUses(),
		Unlimited:  token.Unlimited,
		Uses:       token.uses(),
		CreatedAt:  token.CreatedAt,
		LastUsedAt: token.UsedAt,
	}
	if token.Unlimited { info.MaxUses = 0 }
	if token.ExpiresAt.IsZero() == false {
		expires := token.ExpiresAt
		info.ExpiresAt = &expires
	}
	return
}

// ValidLoginLinkOptions holds every link to the same limits, whichever door
// it was made through.
func ValidLoginLinkOptions( options LoginLinkOptions ) ( result bool ) {
	uses_ok := options.Unlimited || ( options.MaxUses >= 1 && options.MaxUses <= LoginLinkMaxUses )
	ttl_ok := options.TTL >= 0 && options.TTL <= LoginLinkMaxDays*24*time.Hour
	result = uses_ok && ttl_ok
	return
}

// ResolveLoginLink turns a request into options. A link that works once
// defaults to once_ttl (auth.login_token_ttl_seconds); one that works more
// than once defaults to multi_ttl (auth.multi_use_login_token_ttl_seconds),
// since a link meant to be kept for a while is no use if it dies in half an
// hour.
func ResolveLoginLink( request LoginLinkRequest , once_ttl time.Duration , multi_ttl time.Duration ) ( options LoginLinkOptions , err error ) {
	options = LoginLinkOptions{ MaxUses: 1 }
	if request.MaxUses != nil {
		if *request.MaxUses == 0 {
			options = LoginLinkOptions{ Unlimited: true }
		} else {
			options.MaxUses = *request.MaxUses
		}
	}
	options.TTL = once_ttl
	if options.Unlimited || options.MaxUses > 1 { options.TTL = multi_ttl }
	if request.ExpiresInMinutes != nil {
		if *request.ExpiresInMinutes < 0 {
			err = ErrBadLoginLink
			return
		}
		options.TTL = time.Duration( *request.ExpiresInMinutes ) * time.Minute
	}
	if ValidLoginLinkOptions( options ) == false {
		err = ErrBadLoginLink
	}
	return
}

// ParseLoginLinkFlags reads the CLI's -uses ("", "3", "unlimited") and
// -expires ("", "30m", "12h", "7d", "never") into a request. Empty means
// the default.
func ParseLoginLinkFlags( uses string , expires string ) ( request LoginLinkRequest , err error ) {
	uses = strings.ToLower( strings.TrimSpace( uses ) )
	expires = strings.ToLower( strings.TrimSpace( expires ) )
	switch uses {
	case "":
	case "unlimited":
		zero := 0
		request.MaxUses = &zero
	default:
		count , parse_err := strconv.Atoi( uses )
		if parse_err != nil || count < 1 || count > LoginLinkMaxUses {
			err = fmt.Errorf( "-uses must be 1-%d or unlimited" , LoginLinkMaxUses )
			return
		}
		request.MaxUses = &count
	}
	if expires == "" { return }
	minutes := 0
	if expires != "never" {
		unit := expires[ len( expires )-1 ]
		amount , parse_err := strconv.Atoi( expires[ :len( expires )-1 ] )
		per := map[byte]int{ 'm': 1 , 'h': 60 , 'd': 24 * 60 }[ unit ]
		if parse_err != nil || amount < 1 || per == 0 || amount*per > LoginLinkMaxDays*24*60 {
			err = fmt.Errorf( "-expires must be like 30m, 12h or 7d (at most %dd), or never" , LoginLinkMaxDays )
			return
		}
		minutes = amount * per
	}
	request.ExpiresInMinutes = &minutes
	return
}

// DescribeLoginLink is the one-line summary the CLI prints under a link.
func DescribeLoginLink( info LoginLinkInfo ) ( result string ) {
	switch {
	case info.Unlimited:
		result = "Works any number of times"
	case info.MaxUses == 1:
		result = "Works once"
	default:
		result = fmt.Sprintf( "Works up to %d times" , info.MaxUses )
	}
	if info.ExpiresAt == nil {
		result += " and never expires."
	} else {
		result += ", until " + info.ExpiresAt.Local().Format( "2006-01-02 15:04" ) + "."
	}
	return
}

// IssueLoginToken mints a link that works once, for ttl. It is
// IssueLoginLink with the classic options.
func IssueLoginToken( store *db.Store , user_id uint64 , ttl time.Duration ) ( credential string , err error ) {
	credential , _ , err = IssueLoginLink( store , user_id , OnceLink( ttl ) )
	return
}

// IssueLoginLink returns the raw credential exactly once. It is never stored
// and cannot be recovered -- if it is lost, mint another one.
//
// An account has at most one live link. Issuing a new one deletes any link
// the account could still use, in the same transaction, so "send them a new
// link" also kills the old one -- which is what you want when the old one
// went to the wrong place, and costs nothing when it simply expired. That
// includes a reusable link that has already been used: what matters is
// whether it would still let someone in.
func IssueLoginLink( store *db.Store , user_id uint64 , options LoginLinkOptions ) ( credential string , info LoginLinkInfo , err error ) {
	if ValidLoginLinkOptions( options ) == false {
		err = ErrBadLoginLink
		return
	}
	token_id := encryption.GenerateURLToken( 12 )
	secret := encryption.GenerateURLToken( 24 )
	// bcrypt outside the transaction, so the single writer is not held for it.
	secret_hash , err := encryption.BcryptHash( secret )
	if err != nil { return }

	now := time.Now().UTC()
	token := &LoginToken{
		UserID:     user_id,
		SecretHash: secret_hash,
		CreatedAt:  now,
		MaxUses:    options.MaxUses,
		Unlimited:  options.Unlimited,
		MintedByKey: options.MintedByKey,
	}
	if options.ActorID != user_id { token.IssuedBy = options.ActorID }
	if token.Unlimited { token.MaxUses = 0 }
	if options.TTL > 0 { token.ExpiresAt = now.Add( options.TTL ) }
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		target := &User{}
		if tx_err = tx.Get( db.BucketUsers , encryption.Uint64ToBytes( user_id ) , target ); tx_err != nil { return }
		if tx_err = mayActOnTx( tx , options.ActorID , target ); tx_err != nil { return }
		if _ , tx_err = deletePendingLoginTokens( tx , user_id ); tx_err != nil { return }
		tx_err = tx.Put( db.BucketLoginTokens , []byte( token_id ) , token )
		return
	} )
	if err != nil { return }
	credential = token_id + "." + secret
	info = token.Info()
	return
}

// RedeemLoginToken spends one use of a login link and creates the session it
// grants, returning the session credential for the cookie.
//
// The bcrypt comparison runs before the write transaction, so bolt's single
// writer is never held for the deliberately slow part -- otherwise anyone
// holding a valid token id could stall every writer by submitting wrong
// secrets. Everything that decides the outcome is then re-checked inside one
// transaction: the token still has a use left and is unexpired, it is the
// same token that was compared, and the account exists and is enabled.
// Counting the use and storing the session happen there too. So two requests
// racing for a link's last use cannot both get a session, and a link redeemed
// while the account is being disabled either lands first (and
// SetUserDisabled deletes its session and the link) or lands second (and is
// refused).
//
// With two-factor sign-in on, the use is counted when the code is asked for,
// not when it is entered.
func RedeemLoginToken( store *db.Store , credential string , rules SignInRules ) ( signed *SignIn , err error ) {
	token_id , secret , ok := splitCredential( credential )
	if ok == false {
		err = ErrLoginTokenInvalid
		return
	}

	found := &LoginToken{}
	if store.Get( db.BucketLoginTokens , []byte( token_id ) , found ) != nil || found.Usable() == false {
		err = ErrLoginTokenInvalid
		return
	}
	if encryption.BcryptCompare( found.SecretHash , secret ) == false {
		err = ErrLoginTokenInvalid
		return
	}

	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		current := &LoginToken{}
		if tx.Get( db.BucketLoginTokens , []byte( token_id ) , current ) != nil ||
			current.Usable() == false ||
			encryption.ConstantTimeEqual( current.SecretHash , found.SecretHash ) == false {
			tx_err = ErrLoginTokenInvalid
			return
		}
		owner := &User{}
		if tx.Get( db.BucketUsers , encryption.Uint64ToBytes( current.UserID ) , owner ) != nil || owner.Disabled() {
			tx_err = ErrLoginTokenInvalid
			return
		}

		now := time.Now().UTC()
		current.Uses = current.uses() + 1
		current.UsedAt = &now
		if tx_err = tx.Put( db.BucketLoginTokens , []byte( token_id ) , current ); tx_err != nil { return }

		rules.MintedByKey = current.MintedByKey
		rules.IssuedBy = current.IssuedBy
		signed , tx_err = startSignInTx( tx , owner , rules )
		return
	} )
	if err != nil { signed = nil }
	return
}

// LiveLoginLink is the link the account could still sign in with, if any.
func LiveLoginLink( store *db.Store , user_id uint64 ) ( info *LoginLinkInfo , err error ) {
	links , err := LiveLoginLinks( store )
	if err != nil { return }
	if found , ok := links[ user_id ]; ok { info = &found }
	return
}

// LiveLoginLinks maps each account with a usable link to it, in one pass, for
// the admin user list. There is at most one per account (see IssueLoginLink).
func LiveLoginLinks( store *db.Store ) ( links map[uint64]LoginLinkInfo , err error ) {
	links = map[uint64]LoginLinkInfo{}
	err = store.ForEach( db.BucketLoginTokens , func() any { return &LoginToken{} } ,
		func( key []byte , item any ) bool {
			token := item.( *LoginToken )
			if token.Usable() { links[ token.UserID ] = token.Info() }
			return true
		} )
	return
}

// CancelLoginLink stops the account's live link working. Sessions it already
// made stay signed in, as with reissuing. actor_id is who is asking, held to
// mayActOnTx: zero for the operator, the account itself for its own link.
func CancelLoginLink( store *db.Store , user_id uint64 , actor_id uint64 ) ( removed int , err error ) {
	err = store.Write( func( tx *db.Tx ) ( tx_err error ) {
		if tx_err = mayActOnIDTx( tx , actor_id , user_id ); tx_err != nil { return }
		removed , tx_err = deletePendingLoginTokens( tx , user_id )
		return
	} )
	return
}

// deletePendingLoginTokens removes every link an account could still sign in
// with, inside the caller's transaction -- unused ones, and reusable ones
// with uses left. Used-up and expired ones are left for the `purge` job, so
// a second click on a spent link still fails as "already used".
func deletePendingLoginTokens( tx *db.Tx , user_id uint64 ) ( removed int , err error ) {
	removed , err = tx.DeleteWhere( db.BucketLoginTokens ,
		func() any { return &LoginToken{} } ,
		func( key []byte , item any ) bool {
			token := item.( *LoginToken )
			return token.UserID == user_id && token.Usable()
		} )
	return
}

// VoidLinksMintedByKeyTx is what keeps revoking a key meaningful now that a key
// may hand out login links and invite links: inside the caller's transaction
// (the one revoking or deleting the key), every login link the key made that
// could still sign someone in is deleted, every browser session one of those
// links signed in is ended, along with a sign-in still waiting on its
// two-factor code, and every invite link it made is switched off. Every API
// key it made (APIKey.MintedByKey) is revoked too, and the same is done for
// each of those, down the whole chain. Restoring a deleted key brings none of
// them back.
//
// Sessions made through an invite link the key made stay: those are the
// accounts of the people who joined, not a way into an existing one.
func VoidLinksMintedByKeyTx( tx *db.Tx , key_id string ) ( voided int , err error ) {
	if key_id == "" { return }
	voided , err = tx.DeleteWhere( db.BucketLoginTokens ,
		func() any { return &LoginToken{} } ,
		func( key []byte , item any ) bool {
			token := item.( *LoginToken )
			return token.MintedByKey == key_id && token.Usable()
		} )
	if err != nil { return }
	ended , err := tx.DeleteWhere( db.BucketSessions ,
		func() any { return &Session{} } ,
		func( key []byte , item any ) bool {
			return item.( *Session ).MintedByKey == key_id
		} )
	voided += ended
	if err != nil { return }
	// And the keys it made, with what each of those made, down the chain.
	minted , err := revokeKeysMintedByTx( tx , key_id )
	voided += minted
	return
}

// deleteLoginTokensForUser removes every link an account holds, used ones
// included, inside the caller's transaction. Only DeleteUser wants this: once
// the account is gone, "already used" has nothing left to refer to.
func deleteLoginTokensForUser( tx *db.Tx , user_id uint64 ) ( removed int , err error ) {
	removed , err = tx.DeleteWhere( db.BucketLoginTokens ,
		func() any { return &LoginToken{} } ,
		func( key []byte , item any ) bool {
			return item.( *LoginToken ).UserID == user_id
		} )
	return
}

// PurgeExpiredLoginTokens drops spent and stale rows. Used tokens are kept
// until they expire rather than deleted on redemption, so that a second click
// on a link fails as "already used" instead of looking like it never existed.
// A used-up link that never expires is kept for loginLinkRetention after its
// last use. One transaction, for the same reason as PurgeExpiredSessions.
func PurgeExpiredLoginTokens( store *db.Store ) ( removed int , err error ) {
	now := time.Now().UTC()
	removed , err = store.DeleteWhere( db.BucketLoginTokens ,
		func() any { return &LoginToken{} } ,
		func( key []byte , item any ) bool {
			token := item.( *LoginToken )
			if token.Expired( now ) { return true }
			return token.ExpiresAt.IsZero() && token.UsedUp() &&
				token.UsedAt != nil && now.Sub( *token.UsedAt ) > loginLinkRetention
		} )
	return
}
