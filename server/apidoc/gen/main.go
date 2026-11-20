// Command gen writes server/routes/routes_generated.go, the route table,
// from the // @route(...) declaration of each handler (see
// server/apidoc/routegen). go generate runs it from server/routes:
//
//	//go:generate go run ../apidoc/gen
//
// and scripts/api_docs.sh runs go generate before it writes the docs. It
// writes nothing while the file is already current, and stops with every
// mistake it finds, by file and line.
package main

import (
	bytes "bytes"
	fmt "fmt"
	os "os"
	filepath "path/filepath"

	routegen "vocabtrainer/server/apidoc/routegen"
)

func main() {
	dir := "."
	if len( os.Args ) > 1 { dir = os.Args[ 1 ] }
	source , err := routegen.Generate( dir )
	if err != nil {
		fmt.Fprintln( os.Stderr , err )
		os.Exit( 1 )
	}
	path := filepath.Join( dir , routegen.FileName )
	if current , _ := os.ReadFile( path ); bytes.Equal( current , source ) { return }
	if err = os.WriteFile( path , source , 0o644 ); err != nil {
		fmt.Fprintln( os.Stderr , err )
		os.Exit( 1 )
	}
}
