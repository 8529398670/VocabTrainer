// Package livereload tells an open page that the frontend it is running has
// changed, so a save in an editor (or a new build) shows up without anyone
// pressing refresh.
//
// server.live_reload decides whether main.go creates it: auto does for an
// unstamped build ("dev") serving ./static from disk, always and never decide
// it for every build. Without it there is no route, and static/js/live.js
// stops at the 404.
//
// The page asks GET /api/live?boot=..&since=.. and the request is held until
// something changes or holdFor passes (long-poll, kept under the server's
// 30-second WriteTimeout, so no streaming machinery is needed). The reply says
// what changed: a stylesheet alone is swapped in place by static/js/live.js,
// anything else needs a reload, which live.js does at once on a dev build and
// at the next screen change on a stamped one (see static/js/fresh.js). boot
// changes with every start of the process, which is how a page notices a
// restart onto new server code.
//
// Changes are found by comparing modification times and sizes every pollEvery,
// standard library only. A static tree is a few hundred files; walking it is
// cheaper than a file-watching dependency. Embedded files never change, so
// with no directory on disk only restarts are reported.
package livereload

import (
	fs "io/fs"
	filepath "path/filepath"
	strconv "strconv"
	strings "strings"
	sync "sync"
	time "time"

	fiber "github.com/gofiber/fiber/v3"
)

const (
	pollEvery = 300 * time.Millisecond
	holdFor   = 25 * time.Second
)

type fileState struct {
	modified time.Time
	size     int64
}

type Watcher struct {
	root string
	boot string

	mutex   sync.Mutex
	files   map[string]fileState
	stamp   int64
	changed []string      // what the latest stamp changed, slash-separated, relative to root
	wake    chan struct{} // closed, and replaced, on every change
	done    chan struct{}
	closed  bool
}

// Reply is what the page gets. Changed is nil when the page missed more than
// one change (or is new), which means "reload".
type Reply struct {
	Boot    string   `json:"boot"`
	Stamp   int64    `json:"stamp"`
	Changed []string `json:"changed"`
}

// New starts watching root, the static directory on disk, or nothing when
// root is "" (files embedded in the binary).
func New( root string ) ( watcher *Watcher ) {
	watcher = &Watcher{
		root: root,
		boot: strconv.FormatInt( time.Now().UnixNano() , 36 ),
		wake: make( chan struct{} ),
		done: make( chan struct{} ),
	}
	if root == "" { return }
	watcher.files = watcher.scan()
	go watcher.loop()
	return
}

// Close stops watching and answers every held request, so a shutdown does
// not wait out holdFor.
func ( watcher *Watcher ) Close() {
	watcher.mutex.Lock()
	defer watcher.mutex.Unlock()
	if watcher.closed { return }
	watcher.closed = true
	close( watcher.done )
}

func ( watcher *Watcher ) scan() ( files map[string]fileState ) {
	files = map[string]fileState{}
	filepath.WalkDir( watcher.root , func( path string , entry fs.DirEntry , err error ) error {
		if err != nil || entry.IsDir() { return nil }
		if strings.HasPrefix( entry.Name() , "." ) { return nil } // editor swap files
		info , info_err := entry.Info()
		if info_err != nil { return nil }
		relative , _ := filepath.Rel( watcher.root , path )
		files[ filepath.ToSlash( relative ) ] = fileState{ modified: info.ModTime() , size: info.Size() }
		return nil
	} )
	return
}

func ( watcher *Watcher ) loop() {
	ticker := time.NewTicker( pollEvery )
	defer ticker.Stop()
	for {
		select {
		case <-watcher.done:
			return
		case <-ticker.C:
		}
		files := watcher.scan()
		var changed []string
		for name , state := range files {
			if watcher.files[ name ] != state { changed = append( changed , name ) }
		}
		for name := range watcher.files {
			if _ , ok := files[ name ]; ok == false { changed = append( changed , name ) }
		}
		if len( changed ) == 0 { continue }
		watcher.mutex.Lock()
		watcher.files = files
		watcher.stamp++
		watcher.changed = changed
		close( watcher.wake )
		watcher.wake = make( chan struct{} )
		watcher.mutex.Unlock()
	}
}

func ( watcher *Watcher ) reply( since int64 ) ( reply Reply ) {
	reply = Reply{ Boot: watcher.boot , Stamp: watcher.stamp }
	if since >= 0 && since == watcher.stamp - 1 { reply.Changed = watcher.changed }
	return
}

// Handler is GET /api/live. A page from another process (boot differs)
// or one behind on changes is answered at once; one that is up to date waits.
func ( watcher *Watcher ) Handler( c fiber.Ctx ) ( err error ) {
	since , parse_err := strconv.ParseInt( c.Query( "since" ) , 10 , 64 )
	if parse_err != nil { since = -1 }

	watcher.mutex.Lock()
	current , wake := watcher.stamp , watcher.wake
	watcher.mutex.Unlock()

	if c.Query( "boot" ) == watcher.boot && since == current {
		timer := time.NewTimer( holdFor )
		select {
		case <-wake:
		case <-timer.C:
		case <-watcher.done:
		}
		timer.Stop()
	}

	watcher.mutex.Lock()
	reply := watcher.reply( since )
	watcher.mutex.Unlock()
	c.Set( fiber.HeaderCacheControl , "no-store" )
	err = c.JSON( reply )
	return
}
