// Package static implements this project's one opinionated serving rule:
// markup and code are never cached, images are cached hard.
//
// Text assets (.html/.css/.js/...) are read and gzipped on every request with
// Cache-Control: no-store. A deploy is therefore live on the next refresh,
// with no content hashing, no ?v=123 query strings, and no cache-busting
// build step to keep in sync. Gzip is what makes that affordable -- the bytes
// on the wire are a fraction of the file.
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
	fs "io/fs"
	mime "mime"
	path "path"
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

type Server struct {
	fsys      fs.FS
	indexFile string
}

func New( fsys fs.FS ) ( server *Server , err error ) {
	server = &Server{ fsys: fsys , indexFile: "index.html" }
	return
}

// hasDotDot spots a parent-directory segment in the raw request.
//
// fs.FS already refuses these -- fs.ValidPath rejects any path containing a
// ".." element, so nothing can escape the root even without this check. It is
// here so such a request answers 404 directly instead of falling through to
// the index.html fallback, which is clearer to whoever reads the access log,
// and so the intent is stated rather than left as a property of the standard
// library.
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

// resolve turns a request path into an fs.FS name: forward slashes, no
// leading slash, no "." or ".." elements.
func resolve( request_path string ) ( result string , ok bool ) {
	if hasDotDot( request_path ) { return }
	cleaned := path.Clean( "/" + strings.ReplaceAll( request_path , "\\" , "/" ) )
	cleaned = strings.TrimPrefix( cleaned , "/" )
	if cleaned == "" || fs.ValidPath( cleaned ) == false { return }
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
	if request_path == "" || strings.HasSuffix( request_path , "/" ) {
		request_path = path.Join( request_path , server.indexFile )
	}

	name , ok := resolve( request_path )
	if ok == false {
		err = c.Status( fiber.StatusNotFound ).SendString( "Not found" )
		return
	}

	if server.isFile( name ) == false {
		// A path with no extension is a client-side route ("/settings"), so
		// hand back index.html and let the browser router take over. A path
		// that names a file and is missing is a genuine 404 -- answering that
		// with HTML would leave the browser parsing a page as a stylesheet
		// and reporting a confusing MIME error instead of the real problem.
		if path.Ext( name ) != "" {
			err = c.Status( fiber.StatusNotFound ).SendString( "Not found" )
			return
		}
		name = server.indexFile
		if server.isFile( name ) == false {
			err = c.Status( fiber.StatusNotFound ).SendString( "Not found" )
			return
		}
	}

	if alwaysFreshExtensions[ strings.ToLower( path.Ext( name ) ) ] == false {
		c.Set( fiber.HeaderVary , "Accept-Encoding" )
		err = c.SendFile( name , fiber.SendFile{
			FS:        server.fsys,
			MaxAge:    binaryCacheSeconds,
			ByteRange: true,
		} )
		return
	}

	body , read_err := fs.ReadFile( server.fsys , name )
	if read_err != nil {
		err = c.Status( fiber.StatusNotFound ).SendString( "Not found" )
		return
	}

	c.Set( fiber.HeaderContentType , contentType( name ) )
	c.Set( fiber.HeaderCacheControl , "no-store, no-cache, must-revalidate" )
	c.Set( fiber.HeaderVary , "Accept-Encoding" )

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
