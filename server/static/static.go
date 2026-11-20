// Package static implements this project's one opinionated serving rule:
// markup and code are never stale, images are cached hard.
//
// Text assets (.html/.css/.js/...) are read on every request and sent with
// Cache-Control: private, no-cache and an ETag. The browser keeps its copy but asks
// again on every load; an unchanged file is answered 304 with no body, a
// changed one with the new bytes, gzipped. A deploy is therefore live on the
// next load, with no content hashing, no ?v=123 query strings, and no
// cache-busting build step to keep in sync -- and a repeat load downloads
// only what changed.
//
// Images and other binaries get the opposite treatment. Gzip does nothing for
// already-compressed formats, and nobody wants a photo re-downloaded on every
// page view, so those get a long max-age.
//
// The source is an fs.FS rather than a directory path, which is what lets the
// same code serve a live ./static during development and the copy embedded in
// a single-file binary in production. See server/assets.
package static

import (
	bytes "bytes"
	gzip "compress/gzip"
	hex "encoding/hex"
	fnv "hash/fnv"
	fs "io/fs"
	mime "mime"
	path "path"
	regexp "regexp"
	strings "strings"

	fiber "github.com/gofiber/fiber/v3"
)

// Extensions that are text, change often, and must never go stale.
var alwaysFreshExtensions = map[string]bool{
	".html": true, ".htm": true, ".css": true, ".js": true, ".mjs": true,
	".json": true, ".svg": true, ".txt": true, ".map": true,
	".xml": true, ".webmanifest": true, ".yaml": true, ".yml": true,
}

const binaryCacheSeconds = 60 * 60 * 24 * 7 // 7 days

// The paths of the app's screens and cards, as index.html declares them
// (<section class="view" data-view="users" data-path="/admin/users">).
var screenPath = regexp.MustCompile( `data-path="/([a-z0-9-]+(?:/[a-z0-9-]+)*)"` )

// The signed-out page says why someone is there by its path: a login link that
// did not work, a sign-in that took too long, a session that ended.
var signedOutPaths = map[string]bool{
	"signed-out/invalid": true, "signed-out/expired": true, "signed-out/ended": true,
}

// Go's built-in MIME table has no web fonts, and the OS one it consults (if
// any) varies by machine. The vendored icon font needs these.
func init() {
	mime.AddExtensionType( ".woff2" , "font/woff2" )
	mime.AddExtensionType( ".woff" , "font/woff" )
	// The home-screen manifest (site.webmanifest), likewise missing.
	mime.AddExtensionType( ".webmanifest" , "application/manifest+json" )
}

type Server struct {
	// NotFound answers a path with nothing behind it. main.go wraps it so a
	// browser gets the error page; a stylesheet or script tag keeps this plain
	// text, which is what makes a missing asset easy to read in the console.
	NotFound fiber.Handler

	fsys      fs.FS
	indexFile string
}

func New( fsys fs.FS ) ( server *Server , err error ) {
	server = &Server{ NotFound: plainNotFound , fsys: fsys , indexFile: "index.html" }
	return
}

func plainNotFound( c fiber.Ctx ) ( err error ) {
	err = c.Status( fiber.StatusNotFound ).SendString( "Not found" )
	return
}

// hasDotDot spots a parent-directory segment in the raw request.
//
// fs.FS already refuses these -- fs.ValidPath rejects any path containing a
// ".." element. That stops a request *naming* a path outside the root; what
// stops a symlink inside the root from *pointing* outside it is the os.Root
// that server/assets opens for the disk case. This check is here so the intent
// is stated rather than left as a property of the standard library.
func hasDotDot( request_path string ) ( result bool ) {
	normalised := strings.ReplaceAll( request_path , "\\" , "/" )
	for _ , segment := range strings.Split( normalised , "/" ) {
		if segment == ".." {
			result = true
			return
		}
	}
	return
}

// hasDotFile spots a segment that starts with a dot: .env, .git/config,
// .htpasswd, an editor's swap file. Nothing the app serves is named like
// that, and a directory that ends up served by mistake is exactly where such
// files are, so they are never served -- defense in depth behind the startup
// check (server/assets.CheckStaticDir). .well-known/ is the exception: it is
// public by definition (RFC 8615).
func hasDotFile( name string ) ( result bool ) {
	for index , segment := range strings.Split( name , "/" ) {
		if index == 0 && segment == ".well-known" { continue }
		if strings.HasPrefix( segment , "." ) {
			result = true
			return
		}
	}
	return
}

// resolve turns a request path into an fs.FS name: forward slashes, no
// leading slash, no "." or ".." elements, and no dotfiles (see hasDotFile).
func resolve( request_path string ) ( result string , ok bool ) {
	if hasDotDot( request_path ) { return }
	cleaned := path.Clean( "/" + strings.ReplaceAll( request_path , "\\" , "/" ) )
	cleaned = strings.TrimPrefix( cleaned , "/" )
	if cleaned == "" || fs.ValidPath( cleaned ) == false || hasDotFile( cleaned ) { return }
	result , ok = cleaned , true
	return
}

func ( server *Server ) isFile( name string ) ( result bool ) {
	info , err := fs.Stat( server.fsys , name )
	result = err == nil && info.Mode().IsRegular()
	return
}

func contentType( name string ) ( result string ) {
	result = mime.TypeByExtension( strings.ToLower( path.Ext( name ) ) )
	if result == "" {
		result = "application/octet-stream"
	}
	return
}

// Handler serves whatever is in the filesystem. Register it last: it matches
// everything, so any route declared after it is unreachable.
func ( server *Server ) Handler( c fiber.Ctx ) ( err error ) {
	request_path := c.Params( "*" )
	page := strings.Trim( request_path , "/" )
	if request_path == "" || strings.HasSuffix( request_path , "/" ) {
		request_path = path.Join( request_path , server.indexFile )
	}

	// No blanket fallback to index.html. Each screen is a path (/admin/users)
	// that index.html declares; those, and the signed-out reasons, get
	// index.html, and anything else is a real 404. The pages with a
	// credential in the path have their own routes (/login/*, /join/*).
	name , ok := resolve( request_path )
	if ( ok == false || server.isFile( name ) == false ) && ( signedOutPaths[ page ] || server.IsScreen( page ) ) {
		name , ok = server.indexFile , true
	}
	if ok == false || server.isFile( name ) == false {
		err = server.NotFound( c )
		return
	}

	err = server.send( c , name )
	return
}

// SendNamed serves one known file, for a route whose URL does not map onto
// the filesystem at all.
//
// /join/<credential> is the case it exists for: the credential contains a
// ".", so Handler would take it for a file extension and answer a genuine
// 404 rather than falling back to the index. The route knows which page it
// wants, so it says so.
func ( server *Server ) SendNamed( c fiber.Ctx , name string ) ( err error ) {
	resolved , ok := resolve( name )
	if ok == false || server.isFile( resolved ) == false {
		err = c.Status( fiber.StatusNotFound ).SendString( "Not found" )
		return
	}
	err = server.send( c , resolved )
	return
}

// send applies the caching rule and writes the body. Both entry points go
// through here so that "how a file is served" -- the no-store header on
// markup, the long max-age on binaries, the gzip -- has one implementation.
func ( server *Server ) send( c fiber.Ctx , name string ) ( err error ) {
	if alwaysFreshExtensions[ strings.ToLower( path.Ext( name ) ) ] == false {
		c.Set( fiber.HeaderVary , "Accept-Encoding" )
		err = c.SendFile( name , fiber.SendFile{
			FS:        server.fsys,
			MaxAge:    binaryCacheSeconds,
			ByteRange: true,
		} )
		return
	}

	err = server.sendText( c , name , true )
	return
}

// IsScreen reports whether index.html declares page (no leading slash) as a
// screen's or card's data-path. index.html is read each time, so a pruned
// feature or a new screen needs nothing registered here, and only a path with
// no file behind it ever asks.
func ( server *Server ) IsScreen( page string ) ( result bool ) {
	if page == "" { return }
	body , read_err := fs.ReadFile( server.fsys , server.indexFile )
	if read_err != nil { return }
	for _ , match := range screenPath.FindAllSubmatch( body , -1 ) {
		if string( match[ 1 ] ) == page {
			result = true
			return
		}
	}
	return
}

// ServeIndex answers with index.html whatever the path, for routes the
// browser app handles itself (the login link's confirmation page). Those
// URLs carry a credential, so the page is no-store: not even the browser
// keeps a copy filed under it.
func ( server *Server ) ServeIndex( c fiber.Ctx ) ( err error ) {
	if server.isFile( server.indexFile ) == false {
		err = server.NotFound( c )
		return
	}
	err = server.sendText( c , server.indexFile , false )
	return
}

// ServeFile answers with one named page whatever the path, for a route
// whose page reads its own URL (/docs/api, /docs/mcp). no-store, like every
// route in the table rather than the files behind the catch-all.
func ( server *Server ) ServeFile( c fiber.Ctx , name string ) ( err error ) {
	if server.isFile( name ) == false {
		err = server.NotFound( c )
		return
	}
	err = server.sendText( c , name , false )
	return
}

// sendText is the always-fresh half of the serving rule: read on every
// request, re-checked on every load, gzipped when the client accepts it.
// keep false is for the pages routes serve (one whose URL holds a credential,
// the docs page): no-store, no tag.
func ( server *Server ) sendText( c fiber.Ctx , name string , keep bool ) ( err error ) {
	body , read_err := fs.ReadFile( server.fsys , name )
	if read_err != nil {
		err = server.NotFound( c )
		return
	}

	c.Set( fiber.HeaderVary , "Accept-Encoding" )
	if keep == false {
		c.Set( fiber.HeaderCacheControl , "no-store, private" )
	} else {
		// Weak, because the gzipped and plain replies carry the same tag.
		tag := ETag( body )
		// private: the browser keeps it, a proxy or CDN never does (see
		// middleware.NoStore). no-cache: the browser asks again every load.
		c.Set( fiber.HeaderCacheControl , "private, no-cache" )
		c.Set( fiber.HeaderETag , tag )

		// Before gzipping: an unchanged file costs neither the bytes nor the CPU.
		if ETagMatches( c.Get( fiber.HeaderIfNoneMatch ) , tag ) {
			err = c.SendStatus( fiber.StatusNotModified )
			return
		}
	}

	c.Set( fiber.HeaderContentType , contentType( name ) )
	if strings.Contains( c.Get( fiber.HeaderAcceptEncoding ) , "gzip" ) == false {
		err = c.Send( body )
		return
	}

	var compressed bytes.Buffer
	writer , _ := gzip.NewWriterLevel( &compressed , gzip.BestSpeed )
	if _ , write_err := writer.Write( body ); write_err != nil {
		writer.Close()
		err = c.Send( body )
		return
	}
	if close_err := writer.Close(); close_err != nil {
		err = c.Send( body )
		return
	}

	c.Set( fiber.HeaderContentEncoding , "gzip" )
	err = c.Send( compressed.Bytes() )
	return
}

// ETag is the validator for a text asset: a hash of its bytes, so it changes
// exactly when the file does, whichever copy (disk or embedded) served it.
func ETag( body []byte ) ( result string ) {
	hasher := fnv.New64a()
	hasher.Write( body )
	result = `W/"` + hex.EncodeToString( hasher.Sum( nil ) ) + `"`
	return
}

// ETagMatches is If-None-Match's weak comparison: any listed tag equal to
// ours once the W/ prefixes are set aside, or "*".
func ETagMatches( header string , tag string ) ( result bool ) {
	want := strings.TrimPrefix( tag , "W/" )
	for _ , candidate := range strings.Split( header , "," ) {
		candidate = strings.TrimSpace( candidate )
		if candidate == "*" || strings.TrimPrefix( candidate , "W/" ) == want {
			result = true
			return
		}
	}
	return
}
