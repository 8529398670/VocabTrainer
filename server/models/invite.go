package models

import (
	errors "errors"
	strings "strings"
	time "time"

	bolt "go.etcd.io/bbolt"

	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
)

// The four ways an invite can fail to work. They are separate errors rather
// than one blanket "invalid" because, unlike a login token, the caller has
// already had to prove it holds the secret before any of the last three can
// be reported -- see InspectInvite on why that makes naming the reason safe.
var (
	ErrInviteInvalid = errors.New( "invite link is invalid" )
	ErrInviteExpired = errors.New( "invite link has expired" )
	ErrInviteRevoked = errors.New( "invite link was revoked" )
	ErrInviteFull    = errors.New( "invite link has been used the maximum number of times" )
)

// Bounds on the seat count. One is allowed because a one-seat invite is a
// genuinely useful thing -- a login link for someone whose name you do not
// know yet, which is the gap LoginToken cannot fill.
const (
	InviteMinUses = 1
	InviteMaxUses = 100
)

// Invite is a login link that is not bound to a user, and that works a fixed
// number of times instead of once.
//
// LoginToken answers "let this specific person in". An invite answers "let
// the next N people in", which is the shape you need for a link posted to a
// group chat: the admin does not know who will take it, or in what order, and
// should not have to mint one link per person to find out.
//
// The credential is the same "<id>.<secret>" as LoginToken and Session -- the
// id is the bolt key so verification is one fetch and one hash comparison,
// and only the hash is stored, so a stolen database yields no working invite.
//
// UsedCount is the whole mechanism, and it is why claiming has to be atomic
// (see ClaimInvite): a counter that is read, incremented and written in three
// steps is a counter that lets the last seat be taken twice.
type Invite struct {
	SecretHash string `json:"secret_hash"`

	// Role is what accounts claimed through this link are created with,
	// fixed when the invite is minted. The person redeeming it has no say --
	// the link grants exactly what the admin decided to hand out.
	Role string `json:"role"`

	// Label is an admin-only note ("book club chat"), so that two live
	// invites are tellable apart in the list. Never shown to whoever
	// redeems the link.
	Label string `json:"label,omitempty"`

	MaxUses   int `json:"max_uses"`
	UsedCount int `json:"used_count"`

	CreatedBy uint64     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`

	// JoinedUserIDs records who actually came in through this link. It is
	// what turns "2 of 3 used" into something an admin can act on when a
	// link leaks further than intended.
	JoinedUserIDs []uint64 `json:"joined_user_ids,omitempty"`
}

func ( invite *Invite ) Revoked() ( result bool ) {
	result = invite.RevokedAt != nil
	return
}

// SeatsLeft never reports negative, so a caller can print it without
// special-casing an invite that is already full.
func ( invite *Invite ) SeatsLeft() ( result int ) {
	result = invite.MaxUses - invite.UsedCount
	if result < 0 {
		result = 0
	}
	return
}

// usable is the single definition of "this link still works", so the join
// page, the claim, and the admin list cannot disagree about it.
//
// Revocation is checked first: it is the deliberate act, and an admin who
// has just killed a leaked link wants to be told it is revoked rather than
// that it happens to also be full.
func ( invite *Invite ) usable( now time.Time ) ( err error ) {
	switch {
	case invite.Revoked():
		err = ErrInviteRevoked
	case now.After( invite.ExpiresAt ):
		err = ErrInviteExpired
	case invite.UsedCount >= invite.MaxUses:
		err = ErrInviteFull
	}
	return
}

// Usable reports why an invite would be refused right now, or nil if it would
// be accepted. Exported for the admin list, which has the record in hand and
// no credential to inspect.
func ( invite *Invite ) Usable() ( err error ) {
	err = invite.usable( time.Now().UTC() )
	return
}

func ValidInviteUses( max_uses int ) ( result bool ) {
	result = max_uses >= InviteMinUses && max_uses <= InviteMaxUses
	return
}

// ValidInviteLabel allows empty -- the label is a convenience, not a
// requirement. The cap matches ValidDisplayName for the same reason.
func ValidInviteLabel( label string ) ( result bool ) {
	result = len( strings.TrimSpace( label ) ) <= 80
	return
}

// InviteRecord pairs a stored invite with its bolt key, which is also the id
// in its URL. Callers need the key to revoke, and the record does not carry
// it -- storing an id inside the value it is keyed by is one more thing that
// can disagree with itself.
type InviteRecord struct {
	ID     string
	Invite *Invite
}

// IssueInvite returns the raw credential exactly once; only its hash is
// stored. There is deliberately no way to recover it later -- the same rule
// as a login link, and the reason an admin who loses the link revokes it and
// mints another rather than looking it up.
func IssueInvite( store *db.Store , created_by uint64 , role string , label string , max_uses int , ttl time.Duration ) ( invite_id string , credential string , err error ) {
	candidate_id := encryption.GenerateURLToken( 12 )
	secret := encryption.GenerateURLToken( 24 )
	secret_hash , hash_err := encryption.BcryptHash( secret )
	if hash_err != nil {
		err = hash_err
		return
	}

	now := time.Now().UTC()
	invite := &Invite{
		SecretHash: secret_hash,
		Role:       role,
		Label:      strings.TrimSpace( label ),
		MaxUses:    max_uses,
		CreatedBy:  created_by,
		CreatedAt:  now,
		ExpiresAt:  now.Add( ttl ),
	}
	if err = store.Put( db.BucketInvites , []byte( candidate_id ) , invite ); err != nil {
		return
	}
	invite_id , credential = candidate_id , candidate_id+"."+secret
	return
}

// InspectInvite verifies a credential and reports the invite's state without
// touching it. It is what lets the join page say "2 of 3 seats left" -- and,
// more importantly, what lets opening the link be free.
//
// invite is non-nil exactly when the secret checked out, independently of
// err. So a caller can distinguish the two questions it actually has:
//
//	invite == nil            this is not a real invite; say nothing more
//	invite != nil, err != nil real invite, but not usable (err says why)
//	invite != nil, err == nil usable right now -- though see ClaimInvite,
//	                          which re-checks, because "right now" expires
//
// Naming the reason is safe only in the second case, and that is the whole
// point of splitting them: ErrInviteInvalid covers every guess at a link
// that does not exist, so there is nothing to probe. Once someone has
// presented the real secret they already hold the link, and telling them it
// is full rather than "invalid" leaks nothing they could not work out by
// asking the person who posted it.
func InspectInvite( store *db.Store , credential string ) ( invite *Invite , err error ) {
	invite_id , secret , ok := splitCredential( credential )
	if ok == false {
		err = ErrInviteInvalid
		return
	}

	found := &Invite{}
	if get_err := store.Get( db.BucketInvites , []byte( invite_id ) , found ); get_err != nil {
		err = ErrInviteInvalid
		return
	}
	if encryption.BcryptCompare( found.SecretHash , secret ) == false {
		err = ErrInviteInvalid
		return
	}

	invite = found
	err = found.usable( time.Now().UTC() )
	return
}

// ClaimInvite spends one seat and creates the account that took it, returning
// the new user. The caller then starts a session for them exactly as it would
// after redeeming a login token.
//
// This must only ever be reached from a POST. A GET that claimed a seat would
// be spent by anything that fetches a URL to see what is behind it -- every
// chat app's link preview, some mobile browsers' prefetch, a corporate mail
// scanner -- which for a link posted to a group chat means the seats are gone
// before a person has clicked. See routes/invite.go.
func ClaimInvite( store *db.Store , credential string , display_name string ) ( user *User , err error ) {
	// Phase one: verify the secret outside any write transaction.
	//
	// bcrypt is deliberately slow and bolt allows a single writer at a time,
	// so hashing inside the Update below would hold the write lock for the
	// whole comparison and let a stream of wrong guesses stall every other
	// write in the process. Splitting it is safe because an invite's
	// SecretHash is written once at mint and never edited: what phase one
	// proved cannot have changed by phase two.
	if _ , err = InspectInvite( store , credential ); err != nil {
		return
	}
	invite_id , _ , _ := splitCredential( credential )

	// Phase two: the seat and the account in one transaction.
	//
	// Everything phase one decided about *availability* is already stale --
	// two people opening the last seat at the same moment both passed it --
	// so the state check is repeated here, where it is authoritative. Bolt
	// gives one writer at a time, which is what makes that re-check a real
	// guarantee rather than a narrower race. Creating the user in the same
	// transaction means a failure anywhere rolls back both: no account
	// without a spent seat, no spent seat without an account.
	err = store.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		bucket := tx.Bucket( []byte( db.BucketInvites ) )
		raw := bucket.Get( []byte( invite_id ) )
		if raw == nil {
			tx_err = ErrInviteInvalid
			return
		}

		invite := &Invite{}
		if decode_err := store.DecodeValue( raw , invite ); decode_err != nil {
			tx_err = decode_err
			return
		}
		if tx_err = invite.usable( time.Now().UTC() ); tx_err != nil {
			return
		}

		created , create_err := createUserTx( store , tx , display_name , invite.Role )
		if create_err != nil {
			tx_err = create_err
			return
		}

		invite.UsedCount += 1
		invite.JoinedUserIDs = append( invite.JoinedUserIDs , created.ID )
		encoded , encode_err := store.EncodeValue( invite )
		if encode_err != nil {
			tx_err = encode_err
			return
		}
		if tx_err = bucket.Put( []byte( invite_id ) , encoded ); tx_err != nil {
			return
		}

		user = created
		return
	} )
	if err != nil {
		user = nil
	}
	return
}

func ListInvites( store *db.Store ) ( records []*InviteRecord , err error ) {
	records = []*InviteRecord{}
	err = store.ForEach( db.BucketInvites ,
		func() any { return &Invite{} } ,
		func( key []byte , item any ) bool {
			records = append( records , &InviteRecord{ ID: string( key ) , Invite: item.( *Invite ) } )
			return true
		} )
	return
}

func GetInvite( store *db.Store , invite_id string ) ( invite *Invite , err error ) {
	invite = &Invite{}
	err = store.Get( db.BucketInvites , []byte( invite_id ) , invite )
	if err != nil { invite = nil }
	return
}

// RevokeInvite kills a link that has spread further than intended, which is
// the failure mode a shareable invite has and a one-time link does not.
//
// It marks rather than deletes, for the same reason a spent login token is
// kept until it expires: someone still holding the link gets "this was
// revoked" instead of a bare "invalid", and the admin list keeps showing who
// came in through it. Revoking twice keeps the first timestamp -- when it was
// withdrawn is the fact worth holding on to.
func RevokeInvite( store *db.Store , invite_id string ) ( err error ) {
	err = store.UpdateValue( db.BucketInvites , []byte( invite_id ) ,
		func() any { return &Invite{} } ,
		func( item any ) ( mutate_err error ) {
			invite := item.( *Invite )
			if invite.Revoked() == false {
				now := time.Now().UTC()
				invite.RevokedAt = &now
			}
			return
		} )
	return
}

// PurgeExpiredInvites drops rows only once they are past ExpiresAt. An invite
// that is merely full or revoked stays until then on purpose: it is what
// answers the fourth person to click a three-seat link, and deleting it early
// would tell them the link never existed.
func PurgeExpiredInvites( store *db.Store ) ( removed int , err error ) {
	now := time.Now().UTC()
	removed , err = store.DeleteWhere( db.BucketInvites ,
		func() any { return &Invite{} } ,
		func( key []byte , item any ) bool {
			return now.After( item.( *Invite ).ExpiresAt )
		} )
	return
}
