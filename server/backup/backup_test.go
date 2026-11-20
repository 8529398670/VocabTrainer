package backup

import (
	tar "archive/tar"
	gzip "compress/gzip"
	json "encoding/json"
	errors "errors"
	os "os"
	filepath "path/filepath"
	strings "strings"
	testing "testing"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	encryption "vocabtrainer/server/encryption"
	models "vocabtrainer/server/models"
)

// What these pin down: a backup comes back whole, leaves the key out unless
// asked, is refused rather than half-restored when the key or the file is
// wrong, and a restore keeps what it replaced.

func testConfig( t *testing.T ) ( cfg *config.Config ) {
	t.Helper()
	directory := t.TempDir()
	cfg = &config.Config{
		ProjectName:   "Test",
		ConfigFile:    filepath.Join( directory , "config.yaml" ),
		SecretFile:    filepath.Join( directory , "secret.key" ),
		AppDir:        directory,
		DatabasePath:  filepath.Join( directory , "app.db" ),
		StorageDir:    filepath.Join( directory , "storage" ),
		SecretKey:     encryption.GenerateKeyHex(),
		EncryptAtRest: true,
		Backups:       config.BackupsConfig{ Dir: filepath.Join( directory , "backups" ) },
	}
	if err := os.WriteFile( cfg.ConfigFile , []byte( "app:\n  name: Test\n" ) , 0o600 ); err != nil { t.Fatal( err ) }
	if err := os.MkdirAll( filepath.Join( cfg.StorageDir , "photos" ) , 0o700 ); err != nil { t.Fatal( err ) }
	if err := os.WriteFile( filepath.Join( cfg.StorageDir , "photos" , "a.txt" ) , []byte( "original" ) , 0o600 ); err != nil { t.Fatal( err ) }
	return
}

func openStore( t *testing.T , cfg *config.Config ) ( store *db.Store ) {
	t.Helper()
	store , err := db.Open( cfg )
	if err != nil { t.Fatal( err ) }
	return
}

func userNames( t *testing.T , cfg *config.Config ) ( names []string ) {
	t.Helper()
	store := openStore( t , cfg )
	defer store.Close()
	users , err := models.ListUsers( store )
	if err != nil { t.Fatal( err ) }
	for _ , user := range users {
		names = append( names , user.DisplayName )
	}
	return
}

func members( t *testing.T , source string ) ( names map[string]bool ) {
	t.Helper()
	handle , err := os.Open( source )
	if err != nil { t.Fatal( err ) }
	defer handle.Close()
	zipped , err := gzip.NewReader( handle )
	if err != nil { t.Fatal( err ) }
	archive := tar.NewReader( zipped )
	names = map[string]bool{}
	for {
		header , next_err := archive.Next()
		if next_err != nil { return }
		names[ header.Name ] = true
	}
}

// writeTar makes an archive by hand, for the ones Create would never make.
func writeTar( t *testing.T , destination string , entries []tar.Header ) {
	t.Helper()
	output , err := os.Create( destination )
	if err != nil { t.Fatal( err ) }
	defer output.Close()
	zipped := gzip.NewWriter( output )
	archive := tar.NewWriter( zipped )
	for index := range entries {
		if err := archive.WriteHeader( &entries[ index ] ); err != nil { t.Fatal( err ) }
		if entries[ index ].Size > 0 {
			if _ , err := archive.Write( make( []byte , entries[ index ].Size ) ); err != nil { t.Fatal( err ) }
		}
	}
	archive.Close()
	zipped.Close()
}

func TestBackupLeavesTheKeyOutUnlessAsked( t *testing.T ) {
	cfg := testConfig( t )
	store := openStore( t , cfg )
	defer store.Close()

	plain , err := Create( store , cfg , Options{} )
	if err != nil { t.Fatal( err ) }
	names := members( t , plain.Path )
	for _ , wanted := range []string{ "manifest.json" , "app.db" , "config.yaml" , "storage/photos/a.txt" } {
		if names[ wanted ] == false { t.Errorf( "%s is missing from the backup: %v" , wanted , names ) }
	}
	if names[ "secret.key" ] || plain.Manifest.ContainsKey {
		t.Fatal( "a plain backup holds the key" )
	}
	if plain.Manifest.KeyFingerprint != encryption.KeyFingerprint( cfg.SecretKey ) || strings.Contains( cfg.SecretKey , plain.Manifest.KeyFingerprint ) {
		t.Fatalf( "fingerprint %q" , plain.Manifest.KeyFingerprint )
	}
	info , err := os.Stat( plain.Path )
	if err != nil { t.Fatal( err ) }
	if info.Mode().Perm() != 0o600 { t.Fatalf( "backup is mode %o" , info.Mode().Perm() ) }

	keyed , err := Create( store , cfg , Options{ Out: filepath.Join( t.TempDir() , "with-key.tar.gz" ) , IncludeKey: true } )
	if err != nil { t.Fatal( err ) }
	if members( t , keyed.Path )[ "secret.key" ] == false || keyed.Manifest.ContainsKey == false {
		t.Fatal( "-include-key did not include the key" )
	}
}

func TestRestoreBringsTheDataBackAndKeepsWhatItReplaced( t *testing.T ) {
	cfg := testConfig( t )
	store := openStore( t , cfg )
	if _ , err := models.CreateUser( store , "Before" , models.RoleUser ); err != nil { t.Fatal( err ) }
	made , err := Create( store , cfg , Options{} )
	if err != nil { t.Fatal( err ) }
	if _ , err := models.CreateUser( store , "After" , models.RoleUser ); err != nil { t.Fatal( err ) }

	// A server holding the database stops a restore cold.
	if _ , err := Restore( cfg , RestoreOptions{ From: made.Path } ); errors.Is( err , ErrServerRunning ) == false {
		t.Fatalf( "restore under a live store: %v" , err )
	}
	store.Close()

	os.WriteFile( filepath.Join( cfg.StorageDir , "photos" , "a.txt" ) , []byte( "changed" ) , 0o600 )
	result , err := Restore( cfg , RestoreOptions{ From: made.Path } )
	if err != nil { t.Fatal( err ) }
	if result.Rekeyed { t.Fatal( "same key, but it was rekeyed" ) }

	names := userNames( t , cfg )
	if len( names ) != 1 || names[ 0 ] != "Before" { t.Fatalf( "restored users: %v" , names ) }
	body , err := os.ReadFile( filepath.Join( cfg.StorageDir , "photos" , "a.txt" ) )
	if err != nil || string( body ) != "original" { t.Fatalf( "restored storage: %q %v" , body , err ) }

	saved , err := os.ReadFile( filepath.Join( result.SavedStorage , "photos" , "a.txt" ) )
	if err != nil || string( saved ) != "changed" { t.Fatalf( "kept storage: %q %v" , saved , err ) }
	if _ , err := os.Stat( result.SavedDatabase ); err != nil { t.Fatal( err ) }
	if result.SavedConfig == "" { t.Fatal( "the backup's config.yaml was not left to compare" ) }
}

func TestRestoreRefusesTheWrongKeyBeforeTouchingAnything( t *testing.T ) {
	cfg := testConfig( t )
	store := openStore( t , cfg )
	if _ , err := models.CreateUser( store , "Kept" , models.RoleUser ); err != nil { t.Fatal( err ) }
	made , err := Create( store , cfg , Options{} )
	if err != nil { t.Fatal( err ) }
	store.Close()

	original := cfg.SecretKey
	cfg.SecretKey = encryption.GenerateKeyHex()
	if _ , err := Restore( cfg , RestoreOptions{ From: made.Path } ); err == nil || strings.Contains( err.Error() , "different key" ) == false {
		t.Fatalf( "no key given: %v" , err )
	}
	wrong := filepath.Join( t.TempDir() , "wrong.key" )
	os.WriteFile( wrong , []byte( encryption.GenerateKeyHex() + "\n" ) , 0o600 )
	if _ , err := Restore( cfg , RestoreOptions{ From: made.Path , KeyFile: wrong } ); err == nil || strings.Contains( err.Error() , "not the key" ) == false {
		t.Fatalf( "wrong key given: %v" , err )
	}

	// Nothing moved: the same database still opens under its own key.
	cfg.SecretKey = original
	names := userNames( t , cfg )
	if len( names ) != 1 || names[ 0 ] != "Kept" { t.Fatalf( "users after refusals: %v" , names ) }
	entries , _ := os.ReadDir( cfg.AppDir )
	for _ , entry := range entries {
		if strings.Contains( entry.Name() , "before-restore" ) || strings.HasPrefix( entry.Name() , ".restore-" ) {
			t.Fatalf( "a refused restore left %s behind" , entry.Name() )
		}
	}
}

// A backup from another key comes back on the current one, two-factor
// secret included, so secret.key itself never has to change.
func TestRestoreMovesAnOldBackupOntoTheCurrentKey( t *testing.T ) {
	cfg := testConfig( t )
	store := openStore( t , cfg )
	user , err := models.CreateUser( store , "Second factor" , models.RoleUser )
	if err != nil { t.Fatal( err ) }
	made , err := Create( store , cfg , Options{} )
	if err != nil { t.Fatal( err ) }
	store.Close()

	old_key := filepath.Join( t.TempDir() , "secret.key.retired" )
	os.WriteFile( old_key , []byte( cfg.SecretKey + "\n" ) , 0o600 )
	cfg.SecretKey = encryption.GenerateKeyHex()

	result , err := Restore( cfg , RestoreOptions{ From: made.Path , KeyFile: old_key } )
	if err != nil { t.Fatal( err ) }
	if result.Rekeyed == false { t.Fatal( "not moved onto the current key" ) }

	reopened := openStore( t , cfg )
	defer reopened.Close()
	if _ , err = models.GetUser( reopened , user.ID ); err != nil { t.Fatal( err ) }
}

func TestRestoreUsesAKeyPackedInTheBackup( t *testing.T ) {
	cfg := testConfig( t )
	store := openStore( t , cfg )
	if _ , err := models.CreateUser( store , "Packed" , models.RoleUser ); err != nil { t.Fatal( err ) }
	made , err := Create( store , cfg , Options{ IncludeKey: true } )
	if err != nil { t.Fatal( err ) }
	store.Close()

	cfg.SecretKey = encryption.GenerateKeyHex()
	if _ , err := Restore( cfg , RestoreOptions{ From: made.Path } ); err != nil { t.Fatal( err ) }
	if names := userNames( t , cfg ); len( names ) != 1 || names[ 0 ] != "Packed" { t.Fatalf( "users: %v" , names ) }
}

func TestRestoreRefusesEntriesABackupNeverHolds( t *testing.T ) {
	cfg := testConfig( t )
	cases := map[string]tar.Header{
		"parent":   { Name: "../evil" , Typeflag: tar.TypeReg , Mode: 0o600 , Size: 1 },
		"nested":   { Name: "storage/../../evil" , Typeflag: tar.TypeReg , Mode: 0o600 , Size: 1 },
		"absolute": { Name: "/tmp/evil" , Typeflag: tar.TypeReg , Mode: 0o600 , Size: 1 },
		"stranger": { Name: "something-else" , Typeflag: tar.TypeReg , Mode: 0o600 , Size: 1 },
		"symlink":  { Name: "storage/link" , Typeflag: tar.TypeSymlink , Linkname: "/etc/passwd" , Mode: 0o600 },
	}
	for label , header := range cases {
		source := filepath.Join( t.TempDir() , label + ".tar.gz" )
		writeTar( t , source , []tar.Header{ header } )
		if _ , err := Restore( cfg , RestoreOptions{ From: source } ); err == nil {
			t.Errorf( "%s: restored" , label )
		}
	}
	not_a_backup := filepath.Join( t.TempDir() , "plain.txt" )
	os.WriteFile( not_a_backup , []byte( "hello" ) , 0o600 )
	if _ , err := Restore( cfg , RestoreOptions{ From: not_a_backup } ); err == nil {
		t.Error( "a text file restored" )
	}
}

func TestRestoreRefusesANewerFormat( t *testing.T ) {
	staging := t.TempDir()
	encoded , _ := json.Marshal( Manifest{ Format: formatVersion + 1 } )
	os.WriteFile( filepath.Join( staging , manifestName ) , encoded , 0o600 )
	if _ , err := readManifest( staging ); err == nil || strings.Contains( err.Error() , "format" ) == false {
		t.Fatalf( "newer format: %v" , err )
	}
}

func TestRestoreRefusesANewerSchema( t *testing.T ) {
	staging := t.TempDir()
	encoded , _ := json.Marshal( Manifest{ Format: formatVersion , SchemaVersion: db.LatestSchemaVersion() + 1 } )
	os.WriteFile( filepath.Join( staging , manifestName ) , encoded , 0o600 )
	if _ , err := readManifest( staging ); err == nil || strings.Contains( err.Error() , "newer version" ) == false {
		t.Fatalf( "newer schema: %v" , err )
	}
}

