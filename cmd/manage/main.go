// Command manage is the standalone account-administration tool shipped in the
// container image.
//
// It is deliberately a thin wrapper: every command lives in server/manage, so
// this binary and the "manage" subcommand of the main binary cannot drift
// apart. If you are using a portable binary built by build.sh, you do not
// need this at all -- run `./vocab-trainer manage ...` instead.
package main

import (
	fmt "fmt"
	os "os"

	manage "vocabtrainer/server/manage"
)

func main() {
	if err := manage.Run( os.Args[ 1: ] ); err != nil {
		fmt.Fprintf( os.Stderr , "error: %v\n" , err )
		os.Exit( 1 )
	}
}
