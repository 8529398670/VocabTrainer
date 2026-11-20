package models

import (
	filepath "path/filepath"
	testing "testing"

	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
)

// testStore opens a throwaway database. The Config is built by hand rather
// than through config.Load, which would find and maybe write a real
// config.yaml.
func testStore( t *testing.T ) ( store *db.Store ) {
	t.Helper()
	cfg := &config.Config{ DatabasePath: filepath.Join( t.TempDir() , "test.db" ) }
	store , err := db.Open( cfg )
	if err != nil { t.Fatal( err ) }
	t.Cleanup( func() { store.Close() } )
	return
}
