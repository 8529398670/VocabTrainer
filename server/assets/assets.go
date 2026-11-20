// Package assets decides where the frontend comes from: the directory on
// disk, or the copy compiled into the binary.
//
// This is what lets one codebase produce two very different things without a
// build flag or a second code path:
//
//   - Running from a source checkout, ./static and ./config/language.yaml are read
//     from disk. Edit a file, refresh, see the change -- the whole point of
//     the no-store serving policy.
//   - A binary built by scripts/build.sh has no source tree next to it, so it falls
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
	strings "strings"
)

type Bundle struct {
	// Static is the filesystem the web server serves from, rooted at what
	// would be ./static -- so "css/style.css", not "static/css/style.css".
	Static fs.FS

	// StaticDir is the absolute path served from disk, or "" for the
	// embedded copy.
	StaticDir string

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
// a language.yaml in its app directory.
func Resolve( static_dir string , language_file string , embedded_static fs.FS , embedded_language []byte ) ( bundle Bundle , err error ) {
	static_source := "embedded"
	bundle.Static = embedded_static
	if static_dir != "" && isDirectory( static_dir ) {
		absolute , abs_err := filepath.Abs( static_dir )
		if abs_err != nil {
			err = abs_err
			return
		}
		// os.Root, not os.DirFS. Both re-read on every call, so the
		// always-fresh serving behaviour survives either way -- but os.DirFS
		// follows symlinks wherever they point, so a link in static/ to
		// ../config/config.yaml would hand the secret key to any visitor. A Root
		// refuses to resolve anything, symlinks included, that lands outside
		// the directory. It holds the directory open for the life of the
		// process, so replacing static/ wholesale (rather than the files in
		// it) needs a restart to be seen.
		root , root_err := os.OpenRoot( absolute )
		if root_err != nil {
			err = fmt.Errorf( "could not open static directory %q: %w" , absolute , root_err )
			return
		}
		bundle.Static = root.FS()
		bundle.StaticDir = absolute
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

// CheckStaticDir refuses a static directory that would hand a secret to
// anyone: everything in it is served without signing in. protected is the
// config directory, the state directory, secret.key and the database. The
// static directory may not be one of them, sit inside one, or hold one.
//
// config.yaml is file-only for this key (see config.FileOnly), so only a
// person editing the file can get here -- but a typo there must stop the
// server with a clear message rather than publish the key. A static_dir that
// does not exist is fine: the embedded copy is served instead.
func CheckStaticDir( static_dir string , protected ...string ) ( err error ) {
	if static_dir == "" || isDirectory( static_dir ) == false { return }
	static_path , err := canonical( static_dir )
	if err != nil { return }
	for _ , item := range protected {
		if item == "" { continue }
		item_path , item_err := canonical( item )
		if item_err != nil { continue } // not there yet: nothing to serve
		if within( static_path , item_path ) || within( item_path , static_path ) {
			err = fmt.Errorf( "refusing to start: server.static_dir (%s) would serve %s to anyone -- point static_dir at the frontend files only, in config.yaml" , static_path , item_path )
			return
		}
	}
	return
}

// canonical is path absolute, with symlinks followed, so a link cannot dodge
// CheckStaticDir.
func canonical( path string ) ( result string , err error ) {
	result , err = filepath.Abs( path )
	if err != nil { return }
	result , err = filepath.EvalSymlinks( result )
	return
}

// within is true when inner is outer or below it.
func within( inner string , outer string ) ( result bool ) {
	rel , err := filepath.Rel( outer , inner )
	result = err == nil && rel != ".." && strings.HasPrefix( rel , ".." + string( filepath.Separator ) ) == false && filepath.IsAbs( rel ) == false
	return
}
