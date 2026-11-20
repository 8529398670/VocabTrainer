package assets

import (
	os "os"
	filepath "path/filepath"
	strings "strings"
	testing "testing"
)

// S2: everything in static_dir is served without signing in, so the server
// refuses to start when it would serve the config folder, the state folder,
// secret.key or the database.
func TestStaticDirMayNotServeSecrets( t *testing.T ) {
	project := t.TempDir()
	config_dir := filepath.Join( project , "config" )
	app_dir := filepath.Join( project , "state" )
	static_dir := filepath.Join( project , "static" )
	for _ , dir := range []string{ config_dir , app_dir , static_dir } {
		if err := os.MkdirAll( dir , 0o700 ); err != nil { t.Fatal( err ) }
	}
	secret := filepath.Join( config_dir , "secret.key" )
	database := filepath.Join( app_dir , "app.db" )
	os.WriteFile( secret , []byte( "key" ) , 0o600 )
	os.WriteFile( database , []byte( "db" ) , 0o600 )
	protected := []string{ config_dir , app_dir , secret , database }

	if err := CheckStaticDir( static_dir , protected... ); err != nil { t.Fatalf( "a plain static dir: %v" , err ) }
	if err := CheckStaticDir( filepath.Join( project , "missing" ) , protected... ); err != nil { t.Fatalf( "a missing one falls back to embedded: %v" , err ) }

	link := filepath.Join( project , "link" )
	if err := os.Symlink( config_dir , link ); err != nil { t.Fatal( err ) }
	for name , dir := range map[string]string{
		"the config dir":        config_dir,
		"the state dir":         app_dir,
		"inside the config dir": mkdir( t , filepath.Join( config_dir , "web" ) ),
		"above both":            project,
		"a link to config":      link,
	} {
		err := CheckStaticDir( dir , protected... )
		if err == nil || strings.Contains( err.Error() , "static_dir" ) == false {
			t.Errorf( "%s: %v" , name , err )
		}
	}
}

func mkdir( t *testing.T , dir string ) ( result string ) {
	t.Helper()
	if err := os.MkdirAll( dir , 0o700 ); err != nil { t.Fatal( err ) }
	result = dir
	return
}
