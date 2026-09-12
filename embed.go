package main

import (
	embed "embed"
	fs "io/fs"
)

// The frontend, compiled into the binary.
//
// This is what makes build.sh able to produce one file that runs anywhere:
// no ./static directory to ship alongside it, no language.yaml to forget.
// The embed directives have to live in a package at the module root, because
// go:embed cannot reference paths outside its own package directory -- which
// is the only reason this is here rather than inside server/assets.
//
// "all:" includes files whose names begin with "." or "_", which a normal
// embed pattern skips.
//
//go:embed all:static
var embeddedStaticRoot embed.FS

//go:embed language.yaml
var embeddedLanguage []byte

// embeddedStatic re-roots the embedded tree at ./static, so a request for
// "css/style.css" does not have to know it lives at "static/css/style.css"
// inside the binary. This makes it interchangeable with os.DirFS("./static").
func embeddedStatic() ( fsys fs.FS , err error ) {
	fsys , err = fs.Sub( embeddedStaticRoot , "static" )
	return
}
