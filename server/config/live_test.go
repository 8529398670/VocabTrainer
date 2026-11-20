package config

import (
	errors "errors"
	fmt "fmt"
	json "encoding/json"
	os "os"
	filepath "path/filepath"
	strings "strings"
	testing "testing"
)

// newTestHolder is a Holder over a config.yaml (the defaults) and a
// secret.key in a temporary folder.
func newTestHolder( t *testing.T ) ( holder *Holder , path string ) {
	t.Helper()
	useTestDefaults( t )
	dir := t.TempDir()
	path = filepath.Join( dir , "config.yaml" )
	if _ , err := WriteNew( path ); err != nil { t.Fatal( err ) }
	file , err := ReadFile( path )
	if err != nil { t.Fatal( err ) }
	key , err := ReadSecret( SecretPathFor( path ) )
	if err != nil { t.Fatal( err ) }
	cfg := build( file , path , ResolvePaths( file , path ) , key )
	cfg.raw , _ = os.ReadFile( path )
	holder = NewHolder( cfg )
	return
}

func currentVersion( t *testing.T , path string ) ( version string ) {
	raw , err := os.ReadFile( path )
	if err != nil { t.Fatal( err ) }
	version = Version( raw )
	return
}

func TestSaveAppliesLiveKeys( t *testing.T ) {
	holder , path := newTestHolder( t )
	result , err := holder.Save( currentVersion( t , path ) , map[string]any{
		"server.rate_limit_max": json.Number( "7" ),
		"server.port":           json.Number( "9999" ),
		"server.control_socket": true,
	} )
	if err != nil { t.Fatal( err ) }
	if holder.Get().RateLimitMax != 7 {
		t.Fatalf( "rate_limit_max not applied: %d" , holder.Get().RateLimitMax )
	}
	if holder.Get().Port == 9999 {
		t.Fatal( "server.port applied without a restart" )
	}
	if strings.Join( result.Live , "," ) != "server.rate_limit_max" || strings.Join( result.Restart , "," ) != "server.port" || strings.Join( result.Unchanged , "," ) != "server.control_socket" {
		t.Fatalf( "unexpected result %+v" , result )
	}
	raw , _ := os.ReadFile( path )
	if strings.Contains( string( raw ) , "rate_limit_max: 7 " ) == false || strings.Contains( string( raw ) , "# [live]" ) == false {
		t.Fatalf( "file not edited in place:\n%s" , raw )
	}
	snapshot , err := holder.Snapshot()
	if err != nil { t.Fatal( err ) }
	for _ , section := range snapshot.Sections {
		for _ , key := range section.Keys {
			if key.Key == "server.port" && ( key.PendingRestart == false || key.Running != holder.Boot().Port ) {
				t.Fatalf( "server.port should be pending a restart: %+v" , key )
			}
		}
	}
}

func TestSaveRefusesAStaleVersion( t *testing.T ) {
	holder , path := newTestHolder( t )
	before , _ := os.ReadFile( path )
	_ , err := holder.Save( "sha256:old" , map[string]any{ "server.rate_limit_max": json.Number( "7" ) } )
	var conflict *ConflictError
	if errors.As( err , &conflict ) == false || conflict.Version != Version( before ) {
		t.Fatalf( "expected a conflict, got %v" , err )
	}
	after , _ := os.ReadFile( path )
	if string( before ) != string( after ) {
		t.Fatal( "file changed" )
	}
}

func TestSaveRefusesInvalidValues( t *testing.T ) {
	holder , path := newTestHolder( t )
	for _ , changes := range []map[string]any{
		{ "server.port": json.Number( "0" ) },
		{ "server.port": "eighty" },
		{ "server.trust_proxy": true , "server.trusted_proxies": []any{} },
	} {
		_ , err := holder.Save( currentVersion( t , path ) , changes )
		var validation *ValidationError
		if errors.As( err , &validation ) == false {
			t.Fatalf( "%v: expected a validation error, got %v" , changes , err )
		}
	}
	if holder.Get().Port != holder.Boot().Port {
		t.Fatal( "config changed" )
	}
	for _ , key := range []string{ "server.secret_key" , "server.nope" } {
		_ , err := holder.Save( currentVersion( t , path ) , map[string]any{ key: "x" } )
		var unknown *UnknownSettingError
		if errors.As( err , &unknown ) == false {
			t.Fatalf( "%s: expected unknown setting, got %v" , key , err )
		}
	}
}

func TestPollAppliesAHandEdit( t *testing.T ) {
	holder , path := newTestHolder( t )
	notified := 0
	holder.OnChange( func( _ *Config , _ *Config ) { notified++ } )

	raw , _ := os.ReadFile( path )
	edited := strings.Replace( string( raw ) , "rate_limit_max: 120" , "rate_limit_max: 7  " , 1 )
	os.WriteFile( path , []byte( edited ) , 0o600 )
	holder.Poll()
	if holder.Get().RateLimitMax != 120 {
		t.Fatal( "applied on the first sighting -- it should wait for a second" )
	}
	holder.Poll()
	if holder.Get().RateLimitMax != 7 || notified != 1 {
		t.Fatalf( "hand edit not applied: %d, notified %d" , holder.Get().RateLimitMax , notified )
	}

	// An invalid edit keeps the last good config and says why.
	os.WriteFile( path , []byte( strings.Replace( edited , fmt.Sprintf( "port: %d" , holder.Boot().Port ) , "port: 0" , 1 ) ) , 0o600 )
	holder.Poll()
	holder.Poll()
	snapshot , _ := holder.Snapshot()
	if snapshot.Error == nil || strings.Contains( snapshot.Error.Message , "server.port" ) == false {
		t.Fatalf( "expected an error about server.port, got %+v" , snapshot.Error )
	}
	if holder.Get().RateLimitMax != 7 {
		t.Fatal( "last good config lost" )
	}

	// Fixing it clears the error.
	os.WriteFile( path , []byte( strings.Replace( edited , "rate_limit_max: 7  " , "rate_limit_max: 8  " , 1 ) ) , 0o600 )
	holder.Poll()
	holder.Poll()
	if holder.Get().RateLimitMax != 8 {
		t.Fatalf( "not recovered: %d" , holder.Get().RateLimitMax )
	}

	snapshot , _ = holder.Snapshot()
	if snapshot.Error != nil {
		t.Fatalf( "not recovered: %+v" , snapshot.Error )
	}

	// The server's own save is not news to the watcher.
	before := notified
	if _ , err := holder.Save( snapshot.Version , map[string]any{ "server.rate_limit_max": json.Number( "9" ) } ); err != nil { t.Fatal( err ) }
	holder.Poll()
	holder.Poll()
	if notified != before + 1 {
		t.Fatalf( "save then poll notified %d times" , notified - before )
	}
}

func TestSnapshotNeverHoldsTheKey( t *testing.T ) {
	holder , _ := newTestHolder( t )
	snapshot , err := holder.Snapshot()
	if err != nil { t.Fatal( err ) }
	encoded , _ := json.Marshal( snapshot )
	if strings.Contains( string( encoded ) , holder.Get().SecretKey ) || strings.Contains( string( encoded ) , "secret_key" ) {
		t.Fatal( "the snapshot leaks the secret key" )
	}
}

func TestContainerLocksScriptKeys( t *testing.T ) {
	holder , path := newTestHolder( t )
	inContainer( t , path , t.TempDir() )
	_ , err := holder.Save( currentVersion( t , path ) , map[string]any{ "docker.memory": "1g" } )
	if errors.Is( err , ErrLocked ) == false {
		t.Fatalf( "expected ErrLocked, got %v" , err )
	}
}

// S2: a path is file-only outside the container too.
func TestPathsAreFileOnlyEverywhere( t *testing.T ) {
	holder , path := newTestHolder( t )
	for _ , key := range []string{ "server.static_dir" , "server.language_file" , "server.app_dir" , "docker.data_dir" , "build.output_dir" } {
		_ , err := holder.Save( currentVersion( t , path ) , map[string]any{ key: filepath.Dir( path ) } )
		if errors.Is( err , ErrFileOnly ) == false {
			t.Fatalf( "%s: expected ErrFileOnly, got %v" , key , err )
		}
	}
}

func TestWriteInPlaceKeepsTheFileAndABackup( t *testing.T ) {
	dir := t.TempDir()
	path := filepath.Join( dir , "config.yaml" )
	os.WriteFile( path , []byte( "a long first version\n" ) , 0o640 )
	before , _ := os.Stat( path )
	backup := filepath.Join( dir , "data" , BackupFileName )
	os.MkdirAll( filepath.Dir( backup ) , 0o700 )
	if err := writeConfigFile( path , []byte( "short\n" ) , true , backup ); err != nil { t.Fatal( err ) }
	after , _ := os.Stat( path )
	if os.SameFile( before , after ) == false {
		t.Fatal( "in-place write replaced the file" )
	}
	if raw , _ := os.ReadFile( path ); string( raw ) != "short\n" {
		t.Fatalf( "got %q" , raw )
	}
	if raw , _ := os.ReadFile( backup ); string( raw ) != "a long first version\n" {
		t.Fatalf( "backup %q" , raw )
	}

	if err := writeConfigFile( path , []byte( "renamed\n" ) , false , "" ); err != nil { t.Fatal( err ) }
	renamed , _ := os.Stat( path )
	if renamed.Mode().Perm() != 0o640 {
		t.Fatalf( "mode not kept: %o" , renamed.Mode().Perm() )
	}
}

// app.repo is live, loses a trailing / or .git, and builds file links; an
// address that is not https is refused.
func TestRepoIsLiveAndCleaned( t *testing.T ) {
	holder , path := newTestHolder( t )
	// A scaffold given --repo, or a github.com module path, starts with one.
	if _ , err := holder.Save( currentVersion( t , path ) , map[string]any{ "app.repo": "" } ); err != nil {
		t.Fatal( err )
	}
	if holder.Get().RepoFile( "config/api.json" ) != "" {
		t.Fatal( "no repo should mean no link" )
	}
	if _ , err := holder.Save( currentVersion( t , path ) , map[string]any{ "app.repo": "https://github.com/you/app.git/" } ); err != nil {
		t.Fatal( err )
	}
	cfg := holder.Get()
	if cfg.Repo != "https://github.com/you/app" {
		t.Fatalf( "repo not cleaned: %q" , cfg.Repo )
	}
	if link := cfg.RepoFile( "/config/api.json" ); link != "https://github.com/you/app/blob/HEAD/config/api.json" {
		t.Fatalf( "link: %q" , link )
	}
	for _ , bad := range []string{ "http://github.com/you/app" , "github.com/you/app" , "https://github.com/you/app?x=1" , "javascript:alert(1)" } {
		_ , err := holder.Save( currentVersion( t , path ) , map[string]any{ "app.repo": bad } )
		var validation *ValidationError
		if errors.As( err , &validation ) == false {
			t.Fatalf( "%q: expected a validation error, got %v" , bad , err )
		}
	}
}
