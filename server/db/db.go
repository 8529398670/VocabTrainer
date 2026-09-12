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
	BucketMeta        = "meta"

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
)

var bucketNames = []string{
	BucketUsers , BucketSessions , BucketLoginTokens , BucketMeta ,
	BucketProgress , BucketSettings , BucketDailyStats ,
}

type Store struct {
	bolt          *bolt.DB
	secretKey     string
	encryptAtRest bool
}

// Open creates the file if needed and makes sure every bucket exists. The
// timeout matters: bolt takes an exclusive flock, so without it a second
// process (a stray `manage` command, a container that did not fully stop)
// blocks forever instead of telling you what is wrong.
func Open( cfg *config.Config ) ( store *Store , err error ) {
	handle , err := bolt.Open( cfg.DatabasePath , 0o600 , &bolt.Options{ Timeout: 5 * time.Second } )
	if err != nil {
		err = fmt.Errorf( "could not open database at %s (is another instance running?): %w" , cfg.DatabasePath , err )
		return
	}
	store = &Store{
		bolt:          handle,
		secretKey:     cfg.SecretKey,
		encryptAtRest: cfg.EncryptAtRest,
	}
	err = handle.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		for _ , name := range bucketNames {
			if _ , tx_err = tx.CreateBucketIfNotExists( []byte( name ) ); tx_err != nil {
				return
			}
		}
		return
	} )
	if err != nil {
		handle.Close()
		store = nil
	}
	return
}

func ( store *Store ) Close() ( err error ) {
	err = store.bolt.Close()
	return
}

func ( store *Store ) encode( value any ) ( result []byte , err error ) {
	raw , err := json.Marshal( value )
	if err != nil { return }
	if store.encryptAtRest == false {
		result = raw
		return
	}
	result , err = encryption.ChaChaEncryptBytes( store.secretKey , raw )
	return
}

func ( store *Store ) decode( raw []byte , out any ) ( err error ) {
	plain := raw
	if store.encryptAtRest {
		plain , err = encryption.ChaChaDecryptBytes( store.secretKey , raw )
		if err != nil {
			err = fmt.Errorf( "could not decrypt a stored record -- is SECRET_KEY the same one it was written with?: %w" , err )
			return
		}
	}
	err = json.Unmarshal( plain , out )
	return
}

// Put writes value under key. Callers pass a struct; JSON encoding and
// encryption happen here so no model has to think about either.
func ( store *Store ) Put( bucket string , key []byte , value any ) ( err error ) {
	encoded , err := store.encode( value )
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
	err = store.decode( raw , out )
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
			if decode_err := store.decode( value , item ); decode_err != nil {
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
			if decode_err := store.decode( value , item ); decode_err != nil {
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
	encoded , err := store.encode( value )
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
		if tx_err = store.decode( raw , item ); tx_err != nil { return }
		if tx_err = mutate( item ); tx_err != nil { return }
		encoded , encode_err := store.encode( item )
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
			if decode_err := store.decode( value , item ); decode_err != nil {
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

// Update exposes a raw read/write transaction for the rare operation that has
// to be atomic across several keys -- redeeming a login token is the one the
// template ships with. Prefer Put/Get/Delete for everything else.
func ( store *Store ) Update( fn func( tx *bolt.Tx ) error ) ( err error ) {
	err = store.bolt.Update( fn )
	return
}

func ( store *Store ) EncodeValue( value any ) ( result []byte , err error ) {
	result , err = store.encode( value )
	return
}

func ( store *Store ) DecodeValue( raw []byte , out any ) ( err error ) {
	err = store.decode( raw , out )
	return
}
