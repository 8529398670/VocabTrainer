package db

import (
	json "encoding/json"
	os "os"
	filepath "path/filepath"
	testing "testing"

	bolt "go.etcd.io/bbolt"

	config "vocabtrainer/server/config"
	encryption "vocabtrainer/server/encryption"
)

// Kept apart from migrate_test.go: an app scaffolded without most tests keeps
// this file and not that one.
type sealedName struct {
	Name string `json:"name"`
}

func sealingConfig( t *testing.T ) ( cfg *config.Config ) {
	t.Helper()
	cfg = &config.Config{
		DatabasePath:  filepath.Join( t.TempDir() , "app.db" ),
		SecretKey:     encryption.GenerateKeyHex(),
		EncryptAtRest: true,
	}
	return
}

// S12: every value is sealed to its bucket and key. Someone who can write
// app.db but has no secret.key copies the owner's users record over another
// account's: that account must not read back as the owner.
func TestARecordMovedToAnotherPlaceDoesNotOpen( t *testing.T ) {
	store , err := Open( sealingConfig( t ) )
	if err != nil { t.Fatal( err ) }
	defer store.Close()
	owner , other := encryption.Uint64ToBytes( 1 ) , encryption.Uint64ToBytes( 2 )
	if err = store.Put( BucketUsers , owner , sealedName{ Name: "owner" } ) ; err != nil { t.Fatal( err ) }
	if err = store.Put( BucketUsers , other , sealedName{ Name: "other" } ) ; err != nil { t.Fatal( err ) }

	err = store.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		sealed := append( []byte( nil ) , tx.Bucket( []byte( BucketUsers ) ).Get( owner )... )
		if tx_err = tx.Bucket( []byte( BucketUsers ) ).Put( other , sealed ); tx_err != nil { return }
		tx_err = tx.Bucket( []byte( BucketSessions ) ).Put( owner , sealed )
		return
	} )
	if err != nil { t.Fatal( err ) }
	read := sealedName{}
	if err = store.Get( BucketUsers , other , &read ); err == nil { t.Fatalf( "a copied record opened as %q" , read.Name ) }
	if err = store.Get( BucketSessions , owner , &read ); err == nil { t.Fatalf( "a record moved to another bucket opened as %q" , read.Name ) }
	if err = store.Get( BucketUsers , owner , &read ); err != nil || read.Name != "owner" { t.Fatalf( "the record in its place: %v %q" , err , read.Name ) }
}

// S12: a database sealed the old way (secret.key itself, bound to nothing) is
// brought up to date the first time it is opened, once, and the file before
// is kept beside it.
func TestAnOldDatabaseIsResealedOnce( t *testing.T ) {
	cfg := sealingConfig( t )
	handle , err := bolt.Open( cfg.DatabasePath , 0o600 , nil )
	if err != nil { t.Fatal( err ) }
	err = handle.Update( func( tx *bolt.Tx ) ( tx_err error ) {
		for _ , name := range bucketNames {
			if _ , tx_err = tx.CreateBucketIfNotExists( []byte( name ) ); tx_err != nil { return }
		}
		raw , _ := json.Marshal( sealedName{ Name: "owner" } )
		sealed , tx_err := encryption.ChaChaEncryptBytes( cfg.SecretKey , raw )
		if tx_err != nil { return }
		tx_err = tx.Bucket( []byte( BucketUsers ) ).Put( encryption.Uint64ToBytes( 1 ) , sealed )
		return
	} )
	handle.Close()
	if err != nil { t.Fatal( err ) }
	if SealedTheOldWay( cfg.DatabasePath ) == false { t.Fatal( "the old file does not read as old" ) }
	if CheckFile( cfg.DatabasePath , cfg.SecretKey , true ) == nil { t.Fatal( "CheckFile took a file sealed the old way" ) }

	for round := 0; round < 2; round++ {
		store , err := Open( cfg )
		if err != nil { t.Fatal( err ) }
		read := sealedName{}
		if err = store.Get( BucketUsers , encryption.Uint64ToBytes( 1 ) , &read ); err != nil || read.Name != "owner" { t.Fatalf( "round %d: %v %q" , round , err , read.Name ) }
		store.Close()
	}
	kept , _ := filepath.Glob( cfg.DatabasePath + ".before-reseal-*" )
	if len( kept ) != 1 { t.Fatalf( "%d copies kept, want 1" , len( kept ) ) }
	if _ , err = os.Stat( kept[ 0 ] ); err != nil { t.Fatal( err ) }
	if SealedTheOldWay( cfg.DatabasePath ) { t.Fatal( "still old after opening" ) }
	if err = CheckFile( cfg.DatabasePath , cfg.SecretKey , true ); err != nil { t.Fatal( err ) }
}
