// Package assets decides where the frontend comes from: the directory on
// disk, or the copy compiled into the binary.
//
// This is what lets one codebase produce two very different things without a
// build flag or a second code path:
//
//   - Running from a source checkout, ./static and ./language.yaml are read
//     from disk. Edit a file, refresh, see the change -- the whole point of
//     the no-store serving policy.
//   - A binary built by build.sh has no source tree next to it, so it falls
//     back to the embedded copy and runs anywhere as a single file.
//
// Disk wins when it is present because that is the only case where someone
// is actively editing. A shipped binary run somewhere with no ./static
// directory silently and correctly uses what is inside it.
package assets

import (
	fmt "fmt"
	fs "io/fs"
	os "os"
	filepath "path/filepath"
)

type Bundle struct {
	// Static is the filesystem the web server serves from, rooted at what
	// would be ./static -- so "css/style.css", not "static/css/style.css".
	Static fs.FS

	// LanguageYAML is the raw language.yaml, already read.
	LanguageYAML []byte

	// Source describes where the above came from, for the startup log. Being
	// able to see "embedded" versus "disk" in the log is the fastest way to
	// explain why an edit did or did not show up.
	Source string
}

func isDirectory( path string ) ( result bool ) {
	info , err := os.Stat( path )
	result = err == nil && info.IsDir()
	return
}

func isRegularFile( path string ) ( result bool ) {
	info , err := os.Stat( path )
	result = err == nil && info.Mode().IsRegular()
	return
}

// Resolve prefers disk and falls back to embedded, independently for the
// static tree and the language file. They are separate on purpose: a
// deployment that wants the shipped UI but its own wording only has to place
// a language.yaml next to the binary.
func Resolve( static_dir string , language_file string , embedded_static fs.FS , embedded_language []byte ) ( bundle Bundle , err error ) {
	static_source := "embedded"
	bundle.Static = embedded_static
	if static_dir != "" && isDirectory( static_dir ) {
		absolute , abs_err := filepath.Abs( static_dir )
		if abs_err != nil {
			err = abs_err
			return
		}
		// os.DirFS re-reads on every call, so the always-fresh serving
		// behaviour survives -- and it refuses paths that escape the root.
		bundle.Static = os.DirFS( absolute )
		static_source = "disk " + absolute
	}

	language_source := "embedded"
	bundle.LanguageYAML = embedded_language
	if language_file != "" && isRegularFile( language_file ) {
		raw , read_err := os.ReadFile( language_file )
		if read_err != nil {
			err = fmt.Errorf( "could not read language file %q: %w" , language_file , read_err )
			return
		}
		bundle.LanguageYAML = raw
		language_source = "disk " + language_file
	}

	if len( bundle.LanguageYAML ) == 0 {
		err = fmt.Errorf( "no language.yaml found on disk (%q) and none embedded -- the UI would render with no text" , language_file )
		return
	}

	bundle.Source = fmt.Sprintf( "static=%s language=%s" , static_source , language_source )
	return
}
