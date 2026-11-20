// Package db owns the bolt file: opening it, creating buckets, and the
// encrypt-on-write / decrypt-on-read wrapper that every model goes through.
//
// Models never touch bolt or the encryption package directly. That is what
// makes at-rest encryption a single switch instead of something each model
// has to remember, and it means the answer to "where does data get written"
// is one file.
package db

import (
	bytes "bytes"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	io "io"
	log "log"
	os "os"
	time "time"

	bolt "go.etcd.io/bbolt"

	config "vocabtrainer/server/config"
	encryption "vocabtrainer/server/encryption"
)

var ErrNotFound = errors.New( "db: not found" )

// Bucket names. Add new ones here and to bucketNames so they are created at
// startup -- a bucket that only gets created on first write is a nil-pointer
// panic waiting for the first reader.
const (
	BucketUsers       = "users"
	BucketSessions    = "sessions"
	BucketLoginTokens = "login_tokens"
	BucketAPIKeys     = "api_keys"
	BucketInviteLinks = "invite_links"
	BucketSignInChallenges = "sign_in_challenges"
	BucketJobs        = "jobs"
	BucketWebhooks    = "webhooks"
	// BucketWebhookDeliveries is the outbox: see server/webhooks.
	BucketWebhookDeliveries = "webhook_deliveries"
	// BucketFiles describes uploads; the bytes are on disk. See models/file.go.
	BucketFiles       = "files"
	BucketMeta        = "meta"

	// BucketInvites holds the shareable, seat-limited version of a login
	// link -- one record per invite, keyed by its public id. Kept apart from
	// BucketLoginTokens because the two answer different questions: a login
	// token names a user who already exists, an invite creates one.
	BucketInvites = "invites"

	// BucketProgress holds one record per (user, word) the user has acted
	// on. Keys are the user id as 8 big-endian bytes followed by the word,
	// so every card belonging to one person sits in a contiguous run and
	// ForEachPrefix can walk it without touching anyone else's.
	BucketProgress = "progress"

	// BucketSettings is one record per user, keyed by user id.
	BucketSettings = "settings"

	// BucketDailyStats is one record per user per day, keyed by user id
	// followed by "YYYY-MM-DD". Aggregates rather than a review log: a log
	// grows without bound and nothing in the app asks a question that needs
	// individual reviews back.
	BucketDailyStats = "daily_stats"
	// BucketSchema holds the schema version, unencrypted. See migrate.go.
	BucketSchema      = "schema"
)

var bucketNames = []string{
	BucketUsers , BucketSessions , BucketLoginTokens , BucketAPIKeys , BucketInviteLinks , BucketSignInChallenges ,
	BucketJobs , BucketWebhooks , BucketWebhookDeliveries , BucketFiles , BucketMeta , BucketSchema ,
	BucketInvites , BucketProgress , BucketSettings , BucketDailyStats ,
}

type Store struct {
	bolt          *bolt.DB
	// recordKey seals every value, bound to its bucket and key (see encode).
	// It is secret.key's PurposeDB subkey, not secret.key itself.
	recordKey     string
	encryptAtRest bool
}

func newStore( handle *bolt.DB , secret_key string , encrypt_at_rest bool ) ( store *Store ) {
	store = &Store{ bolt: handle , recordKey: encryption.SubKey( secret_key , encryption.PurposeDB ) , encryptAtRest: encrypt_at_rest }
	return
}

// FieldRekey is what moving a record to another key does to the fields a
// model seals on its own, inside the record. models sets it (models.RekeyRecord)
// so that Open can bring a database sealed the old way up to date: db cannot
// import models, which imports db.
var FieldRekey func( old_key string , new_key string ) func( bucket string , plain []byte ) ( []byte , error )

// Open creates the file if needed and makes sure every bucket exists. The
// timeout matters: bolt takes an exclusive flock, so without it a second
// process (a stray `manage` command, a container that did not fully stop)
// blocks forever instead of telling you what is wrong.
func Open( cfg *config.Config ) ( store *Store , err error ) {
	store , err = openWith( cfg , migrations )
	return
}

// openWith is Open with the list of migrations named, so a test can bring its
// own.
func openWith( cfg *config.Config , steps []Migration ) ( store *Store , err error ) {
	fresh := exists( cfg.DatabasePath ) == false
	if fresh == false {
		if err = resealIfOld( cfg ); err != nil { return }
	}
	handle , err := bolt.Open( cfg.DatabasePath , 0o600 , &bolt.Options{ Timeout: 5 * time.Second } )
	if err != nil {
		err = fmt.Errorf( "could not open database at %s (is another instance running?): %w" , cfg.DatabasePath , err )
		return
	}
	store = newStore( handle , cfg.SecretKey , cfg.EncryptAtRest )
	err = handle.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		for _ , name := range bucketNames {
			if _ , tx_err = tx.CreateBucketIfNotExists( []byte( name ) ); tx_err != nil {
				return
			}
		}
		if fresh { tx_err = writeSealing( tx ) }
		return
	} )
	if err == nil {
		err = store.migrate( cfg , fresh , steps )
	}
	if err != nil {
		handle.Close()
		store = nil
	}
	return
}

// Snapshot writes a consistent copy of the database to path while the server
// goes on working: a bolt read transaction sees one fixed moment, and writers
// are not held up by it. The file keeps its encryption, so it needs the same
// key to read.
func ( store *Store ) Snapshot( path string ) ( err error ) {
	err = store.bolt.View( func( tx *bolt.Tx ) ( tx_err error ) {
		tx_err = tx.CopyFile( path , 0o600 )
		return
	} )
	return
}

// ErrInUse means another process holds the database.
var ErrInUse = errors.New( "db: the database is in use" )

// IsLocked says an open failed because another process holds the file, as
// opposed to something being wrong with the file itself.
func IsLocked( err error ) ( result bool ) {
	result = errors.Is( err , bolt.ErrTimeout )
	return
}

// Exclusive takes bolt's lock on a database file, for work that must not
// happen under a live server. release gives it back. A missing file is free,
// and so is a damaged one: bolt refuses to open it, but nothing can be
// serving it either, and repairing it by restoring is the likeliest reason to
// be here.
func Exclusive( path string ) ( release func() , err error ) {
	release = func() {}
	if _ , stat_err := os.Stat( path ); stat_err != nil { return }
	handle , open_err := bolt.Open( path , 0o600 , &bolt.Options{ Timeout: time.Second } )
	switch {
	case open_err == nil:
		release = func() { handle.Close() }
	case errors.Is( open_err , bolt.ErrTimeout ):
		err = ErrInUse
	}
	return
}

// CheckFile opens a database file that nothing is using and decodes every
// record in it, so a backup can be proved readable before it replaces
// anything.
func CheckFile( path string , secret_key string , encrypt_at_rest bool ) ( err error ) {
	handle , err := bolt.Open( path , 0o600 , &bolt.Options{ Timeout: 5 * time.Second , ReadOnly: true } )
	if err != nil { return }
	defer handle.Close()
	reader := newStore( nil , secret_key , encrypt_at_rest )
	err = handle.View( func( tx *bolt.Tx ) ( tx_err error ) {
		if readSealing( tx ) != sealingVersion {
			tx_err = ErrSealedTheOldWay
			return
		}
		tx_err = tx.ForEach( func( name []byte , bucket *bolt.Bucket ) ( bucket_err error ) {
			if string( name ) == BucketSchema { return }
			bucket_err = bucket.ForEach( func( key []byte , value []byte ) ( record_err error ) {
				if value == nil { return }
				var record any
				if record_err = reader.decode( string( name ) , key , value , &record ); record_err != nil {
					record_err = fmt.Errorf( "%s/%q: %w" , name , key , record_err )
				}
				return
			} )
			return
		} )
		return
	} )
	return
}

// Rekey rewrites a database file that nothing is using from old_key to
// new_key, in one transaction, so it is all or nothing. Every value goes
// through rewrite as plain JSON, for the fields a model encrypts a second
// time on its own (see models.RekeyRecord). With encrypt_at_rest off only
// rewrite has anything to change.
//
// A file sealed the old way (before records were bound to their place, see
// encode) is read the old way and written the new way, so the same call that
// rotates a key also brings an old database or backup up to date; old_key and
// new_key may then be the same key.
func Rekey( path string , old_key string , new_key string , encrypt_at_rest bool , rewrite func( bucket string , plain []byte ) ( []byte , error ) ) ( err error ) {
	handle , err := bolt.Open( path , 0o600 , &bolt.Options{ Timeout: 5 * time.Second } )
	if err != nil { return }
	defer handle.Close()
	reader := newStore( nil , old_key , encrypt_at_rest )
	writer := newStore( nil , new_key , encrypt_at_rest )
	err = handle.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		old_way := readSealing( tx ) != sealingVersion
		tx_err = tx.ForEach( func( name []byte , bucket *bolt.Bucket ) ( bucket_err error ) {
			if string( name ) == BucketSchema { return }
			// Collected first: changing a bucket under its own cursor is the
			// one thing bolt warns about.
			changed := map[string][]byte{}
			bucket_err = bucket.ForEach( func( key []byte , value []byte ) ( record_err error ) {
				if value == nil { return }
				var plain []byte
				if old_way {
					plain , record_err = openTheOldWay( old_key , encrypt_at_rest , value )
				} else {
					plain , record_err = reader.open( string( name ) , key , value )
				}
				if record_err != nil {
					record_err = fmt.Errorf( "could not decrypt %s/%q with the old key: %w" , name , key , record_err )
					return
				}
				if plain , record_err = rewrite( string( name ) , plain ); record_err != nil {
					record_err = fmt.Errorf( "%s/%q: %w" , name , key , record_err )
					return
				}
				if plain , record_err = writer.seal( string( name ) , key , plain ); record_err != nil { return }
				// A copy: plain can still be bolt's own memory here.
				changed[ string( key ) ] = append( []byte( nil ) , plain... )
				return
			} )
			if bucket_err != nil { return }
			for key , value := range changed {
				if bucket_err = bucket.Put( []byte( key ) , value ); bucket_err != nil { return }
			}
			return
		} )
		if tx_err == nil { tx_err = writeSealing( tx ) }
		return
	} )
	return
}

// ErrSealedTheOldWay is a database file whose records are not yet bound to
// their place. Open brings it up to date by itself; CheckFile refuses it.
var ErrSealedTheOldWay = errors.New( "db: the database is sealed the old way, before records were bound to their place -- open it with this version once (the server does it at start) or pass it through Rekey" )

// sealingKey, in the schema bucket, says how values are sealed. Missing is
// the old way: secret.key itself, bound to nothing.
var sealingKey = []byte( "sealing" )

const sealingVersion = "2"

func readSealing( tx *bolt.Tx ) ( result string ) {
	bucket := tx.Bucket( []byte( BucketSchema ) )
	if bucket == nil { return }
	result = string( bucket.Get( sealingKey ) )
	return
}

func writeSealing( tx *bolt.Tx ) ( err error ) {
	bucket , err := tx.CreateBucketIfNotExists( []byte( BucketSchema ) )
	if err != nil { return }
	err = bucket.Put( sealingKey , []byte( sealingVersion ) )
	return
}

// SealedTheOldWay says whether the database file at path still needs
// bringing up to date. A file that cannot be read says no: whatever opens it
// next reports the real problem.
func SealedTheOldWay( path string ) ( result bool ) {
	handle , err := bolt.Open( path , 0o600 , &bolt.Options{ Timeout: 5 * time.Second , ReadOnly: true } )
	if err != nil { return }
	defer handle.Close()
	handle.View( func( tx *bolt.Tx ) ( tx_err error ) {
		result = readSealing( tx ) != sealingVersion
		return
	} )
	return
}

// resealIfOld brings a database sealed the old way up to date, once, before
// anything reads it: every record is opened with secret.key, bound to nothing,
// and sealed again with the PurposeDB subkey, bound to its bucket and key, and
// the fields models seal on their own move to their own subkeys. The file
// before is kept beside it, as a migration keeps it.
func resealIfOld( cfg *config.Config ) ( err error ) {
	if SealedTheOldWay( cfg.DatabasePath ) == false { return }
	kept := fmt.Sprintf( "%s.before-reseal-%s" , cfg.DatabasePath , time.Now().UTC().Format( "20060102-150405" ) )
	if err = copyFile( cfg.DatabasePath , kept ); err != nil {
		err = fmt.Errorf( "could not copy the database before re-sealing it: %w" , err )
		return
	}
	rewrite := func( bucket string , plain []byte ) ( []byte , error ) { return plain , nil }
	if FieldRekey != nil { rewrite = FieldRekey( cfg.SecretKey , cfg.SecretKey ) }
	if err = Rekey( cfg.DatabasePath , cfg.SecretKey , cfg.SecretKey , cfg.EncryptAtRest , rewrite ); err != nil {
		err = fmt.Errorf( "could not re-seal the database, it is unchanged (a copy is at %s): %w" , kept , err )
		return
	}
	log.Printf( "[reseal] every record is now bound to its place; the database before is kept at %s" , kept )
	return
}

func copyFile( source string , target string ) ( err error ) {
	input , err := os.Open( source )
	if err != nil { return }
	defer input.Close()
	output , err := os.OpenFile( target , os.O_WRONLY|os.O_CREATE|os.O_EXCL , 0o600 )
	if err != nil { return }
	defer output.Close()
	if _ , err = io.Copy( output , input ); err != nil { return }
	if err = output.Sync(); err != nil { return }
	err = output.Close()
	return
}

// openTheOldWay is how a value was opened before records were bound.
func openTheOldWay( secret_key string , encrypt_at_rest bool , raw []byte ) ( plain []byte , err error ) {
	plain = raw
	if encrypt_at_rest == false { return }
	plain , err = encryption.ChaChaDecryptBytes( secret_key , raw )
	return
}

func ( store *Store ) Close() ( err error ) {
	err = store.bolt.Close()
	return
}

// boundTo is what a value is sealed to besides the key: its bucket, a zero
// byte, and its key. A value copied to another key or bucket then fails to
// open, instead of being read as that record: with the owner's users record
// copied over another account's, that account's cookie would pass as the
// owner's. Someone who can write app.db but has no secret.key (a backup, a
// restore pipeline, shared storage) can still delete records, or put back an
// older copy of the same one, but not move one.
func boundTo( bucket string , key []byte ) ( bound []byte ) {
	bound = make( []byte , 0 , len( bucket ) + 1 + len( key ) )
	bound = append( bound , bucket... )
	bound = append( bound , 0 )
	bound = append( bound , key... )
	return
}

func ( store *Store ) encode( bucket string , key []byte , value any ) ( result []byte , err error ) {
	raw , err := json.Marshal( value )
	if err != nil { return }
	result , err = store.seal( bucket , key , raw )
	return
}

// seal is encode without the JSON.
func ( store *Store ) seal( bucket string , key []byte , raw []byte ) ( result []byte , err error ) {
	result = raw
	if store.encryptAtRest == false { return }
	result , err = encryption.ChaChaEncryptBound( store.recordKey , raw , boundTo( bucket , key ) )
	return
}

func ( store *Store ) decode( bucket string , key []byte , raw []byte , out any ) ( err error ) {
	plain , err := store.open( bucket , key , raw )
	if err != nil { return }
	err = json.Unmarshal( plain , out )
	return
}

// open is decode without the JSON: a stored value as its plain bytes.
func ( store *Store ) open( bucket string , key []byte , raw []byte ) ( plain []byte , err error ) {
	plain = raw
	if store.encryptAtRest == false { return }
	plain , err = encryption.ChaChaDecryptBound( store.recordKey , raw , boundTo( bucket , key ) )
	if err != nil {
		err = fmt.Errorf( "could not decrypt the stored record %s/%q -- is secret.key the same key it was written with, and is the record where it was written?: %w" , bucket , key , err )
	}
	return
}

// Put writes value under key. Callers pass a struct; JSON encoding and
// encryption happen here so no model has to think about either.
func ( store *Store ) Put( bucket string , key []byte , value any ) ( err error ) {
	encoded , err := store.encode( bucket , key , value )
	if err != nil { return }
	err = store.bolt.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		tx_err = tx.Bucket( []byte( bucket ) ).Put( key , encoded )
		return
	} )
	return
}

// Get returns ErrNotFound rather than leaving out untouched, so callers can
// tell "no such record" apart from "record exists and is zero-valued".
func ( store *Store ) Get( bucket string , key []byte , out any ) ( err error ) {
	var raw []byte
	err = store.bolt.View( func( tx *bolt.Tx ) ( tx_err error ) {
		value := tx.Bucket( []byte( bucket ) ).Get( key )
		if value == nil {
			tx_err = ErrNotFound
			return
		}
		raw = append( []byte( nil ) , value... ) // bolt's slice is only valid inside the tx
		return
	} )
	if err != nil { return }
	err = store.decode( bucket , key , raw , out )
	return
}

func ( store *Store ) Delete( bucket string , key []byte ) ( err error ) {
	err = store.bolt.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		tx_err = tx.Bucket( []byte( bucket ) ).Delete( key )
		return
	} )
	return
}

// ForEach walks a bucket in key order, decoding each value into a fresh
// instance produced by newItem. Iteration stops early if visit returns false.
func ( store *Store ) ForEach( bucket string , newItem func() any , visit func( key []byte , item any ) bool ) ( err error ) {
	err = store.bolt.View( func( tx *bolt.Tx ) ( tx_err error ) {
		cursor := tx.Bucket( []byte( bucket ) ).Cursor()
		for key , value := cursor.First(); key != nil; key , value = cursor.Next() {
			item := newItem()
			if decode_err := store.decode( bucket , key , value , item ); decode_err != nil {
				tx_err = decode_err
				return
			}
			key_copy := append( []byte( nil ) , key... )
			if visit( key_copy , item ) == false {
				return
			}
		}
		return
	} )
	return
}

// ForEachPrefix walks only the keys beginning with prefix, in key order.
//
// This is what makes per-user data cheap in a single bolt file: seeking to
// the prefix and stopping when it stops matching reads one user's records
// rather than decoding (and decrypting) every record in the bucket to throw
// most of them away.
func ( store *Store ) ForEachPrefix( bucket string , prefix []byte , newItem func() any , visit func( key []byte , item any ) bool ) ( err error ) {
	err = store.bolt.View( func( tx *bolt.Tx ) ( tx_err error ) {
		cursor := tx.Bucket( []byte( bucket ) ).Cursor()
		for key , value := cursor.Seek( prefix ); key != nil && bytes.HasPrefix( key , prefix ); key , value = cursor.Next() {
			item := newItem()
			if decode_err := store.decode( bucket , key , value , item ); decode_err != nil {
				tx_err = decode_err
				return
			}
			key_copy := append( []byte( nil ) , key... )
			if visit( key_copy , item ) == false {
				return
			}
		}
		return
	} )
	return
}

// PutIfAbsent writes value only when key does not already exist, reporting
// whether it wrote. The check and the write share one transaction, so two
// concurrent callers cannot both see "absent" and both write.
func ( store *Store ) PutIfAbsent( bucket string , key []byte , value any ) ( written bool , err error ) {
	encoded , err := store.encode( bucket , key , value )
	if err != nil { return }
	err = store.bolt.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		target := tx.Bucket( []byte( bucket ) )
		if target.Get( key ) != nil { return }
		if tx_err = target.Put( key , encoded ); tx_err != nil { return }
		written = true
		return
	} )
	if err != nil { written = false }
	return
}

// NextSequence hands out monotonic ids inside bolt's own transaction, so two
// concurrent creates can never collide on the same id.
func ( store *Store ) NextSequence( bucket string ) ( result uint64 , err error ) {
	err = store.bolt.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		result , tx_err = tx.Bucket( []byte( bucket ) ).NextSequence()
		return
	} )
	return
}

// UpdateValue reads a record, hands it to mutate, and writes the result back
// inside a single write transaction.
//
// The obvious alternative -- Get, change the struct, Put -- has a lost update
// hiding in the gap between the two transactions: two concurrent callers both
// read the old record and the second Put silently discards the first one's
// change. Anything that edits an existing record should come through here.
func ( store *Store ) UpdateValue( bucket string , key []byte , newItem func() any , mutate func( item any ) error ) ( err error ) {
	err = store.bolt.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		target := tx.Bucket( []byte( bucket ) )
		raw := target.Get( key )
		if raw == nil {
			tx_err = ErrNotFound
			return
		}
		item := newItem()
		if tx_err = store.decode( bucket , key , raw , item ); tx_err != nil { return }
		if tx_err = mutate( item ); tx_err != nil { return }
		encoded , encode_err := store.encode( bucket , key , item )
		if encode_err != nil {
			tx_err = encode_err
			return
		}
		tx_err = target.Put( key , encoded )
		return
	} )
	return
}

// DeleteWhere removes every record in a bucket that match reports true for,
// inside a single write transaction.
//
// Written the obvious way -- a read pass that collects keys, then one Delete
// per key -- this is two problems at once. It is N write transactions and N
// fsyncs where one would do, and because bolt allows a single writer at a
// time, a purge over a large bucket becomes a queue that every request
// handler waiting to write sits behind. It is also wrong: a record that
// starts matching between the read pass and the deletes is missed, which for
// session revocation means a session created mid-sweep outlives the account
// it belongs to.
func ( store *Store ) DeleteWhere( bucket string , newItem func() any , match func( key []byte , item any ) bool ) ( removed int , err error ) {
	err = store.bolt.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		target := tx.Bucket( []byte( bucket ) )
		doomed := [][]byte{}
		cursor := target.Cursor()
		for key , value := cursor.First(); key != nil; key , value = cursor.Next() {
			item := newItem()
			if decode_err := store.decode( bucket , key , value , item ); decode_err != nil {
				tx_err = decode_err
				return
			}
			if match( key , item ) {
				doomed = append( doomed , append( []byte( nil ) , key... ) )
			}
		}
		// Deleting through the cursor while iterating is legal in bolt, but
		// where the cursor lands afterwards is subtle enough to be worth
		// avoiding. Collecting first costs one slice and keeps this
		// obviously correct -- and it is still one transaction either way.
		for _ , key := range doomed {
			if tx_err = target.Delete( key ); tx_err != nil { return }
		}
		removed = len( doomed )
		return
	} )
	if err != nil { removed = 0 }
	return
}

// Update exposes a raw bolt read/write transaction. Prefer Write, which
// gives the same atomicity with encoding and encryption handled for you.
func ( store *Store ) Update( fn func( tx *bolt.Tx ) error ) ( err error ) {
	err = store.bolt.Update( fn )
	return
}

// Tx is one write transaction with the same record API as Store, for the
// operations that have to be atomic across several records or buckets:
// turning a login link into a session, disabling an account together with
// every credential it holds, checking an API key quota and inserting the key.
//
// bolt allows a single writer at a time, so whatever runs inside Write sees
// no other write interleave with it. That is the whole point -- and also the
// cost: keep slow work (bcrypt, network calls) outside, because every other
// writer waits for this one to finish.
type Tx struct {
	store *Store
	tx    *bolt.Tx
}

func ( store *Store ) Write( fn func( tx *Tx ) error ) ( err error ) {
	err = store.bolt.Update( func( raw *bolt.Tx ) ( tx_err error ) {
		tx_err = fn( &Tx{ store: store , tx: raw } )
		return
	} )
	return
}

func ( tx *Tx ) Get( bucket string , key []byte , out any ) ( err error ) {
	value := tx.tx.Bucket( []byte( bucket ) ).Get( key )
	if value == nil {
		err = ErrNotFound
		return
	}
	err = tx.store.decode( bucket , key , value , out )
	return
}

func ( tx *Tx ) Put( bucket string , key []byte , value any ) ( err error ) {
	encoded , err := tx.store.encode( bucket , key , value )
	if err != nil { return }
	err = tx.tx.Bucket( []byte( bucket ) ).Put( key , encoded )
	return
}

func ( tx *Tx ) Delete( bucket string , key []byte ) ( err error ) {
	err = tx.tx.Bucket( []byte( bucket ) ).Delete( key )
	return
}

// NextSequence is Store.NextSequence inside this transaction, for a record
// that has to be created together with something else -- a user and the
// owner marker that names it, for one.
func ( tx *Tx ) NextSequence( bucket string ) ( result uint64 , err error ) {
	result , err = tx.tx.Bucket( []byte( bucket ) ).NextSequence()
	return
}

// ForEach is Store.ForEach inside this transaction.
func ( tx *Tx ) ForEach( bucket string , newItem func() any , visit func( key []byte , item any ) bool ) ( err error ) {
	cursor := tx.tx.Bucket( []byte( bucket ) ).Cursor()
	for key , value := cursor.First(); key != nil; key , value = cursor.Next() {
		item := newItem()
		if err = tx.store.decode( bucket , key , value , item ); err != nil { return }
		if visit( append( []byte( nil ) , key... ) , item ) == false { return }
	}
	return
}

// DeleteWhere is Store.DeleteWhere inside this transaction.
func ( tx *Tx ) DeleteWhere( bucket string , newItem func() any , match func( key []byte , item any ) bool ) ( removed int , err error ) {
	doomed := [][]byte{}
	err = tx.ForEach( bucket , newItem , func( key []byte , item any ) bool {
		if match( key , item ) {
			doomed = append( doomed , key )
		}
		return true
	} )
	if err != nil { return }
	target := tx.tx.Bucket( []byte( bucket ) )
	for _ , key := range doomed {
		if err = target.Delete( key ); err != nil { return }
	}
	removed = len( doomed )
	return
}

// EncodeValue and DecodeValue are encode and decode for a raw bolt
// transaction (Update): the value is sealed to bucket and key, so it must be
// written exactly there.
func ( store *Store ) EncodeValue( bucket string , key []byte , value any ) ( result []byte , err error ) {
	result , err = store.encode( bucket , key , value )
	return
}

func ( store *Store ) DecodeValue( bucket string , key []byte , raw []byte , out any ) ( err error ) {
	err = store.decode( bucket , key , raw , out )
	return
}
