// Command Vocab Trainer is the web server entry point.
//
// This file only wires things together -- load config, open the database,
// build the app, register routes, start and stop cleanly. Every decision of
// substance lives in a package under server/. If you find yourself adding
// business logic here, it belongs in server/routes/ or a new package instead.
//
// The same binary is also the admin CLI:
//
//	vocab-trainer                     run the server
//	vocab-trainer manage list-users   account administration
//	vocab-trainer version             build information
package main

import (
	fmt "fmt"
	log "log"
	os "os"
	signal "os/signal"
	runtime "runtime"
	syscall "syscall"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	assets "vocabtrainer/server/assets"
	bootstrap "vocabtrainer/server/bootstrap"
	config "vocabtrainer/server/config"
	corpus "vocabtrainer/server/corpus"
	control "vocabtrainer/server/control"
	db "vocabtrainer/server/db"
	language "vocabtrainer/server/language"
	manage "vocabtrainer/server/manage"
	middleware "vocabtrainer/server/middleware"
	models "vocabtrainer/server/models"
	routes "vocabtrainer/server/routes"
	security "vocabtrainer/server/security"
	static "vocabtrainer/server/static"
)

// Stamped by build.sh via -ldflags. The defaults are what you get from a
// plain `go build`, which is a useful signal in itself.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func main() {
	// Subcommands before anything else: `manage` must work on a database the
	// server cannot currently open, which is exactly when you need it.
	if len( os.Args ) > 1 {
		switch os.Args[ 1 ] {
		case "manage":
			if err := manage.Run( os.Args[ 2: ] ); err != nil {
				fmt.Fprintf( os.Stderr , "error: %v\n" , err )
				os.Exit( 1 )
			}
			return
		case "version" , "-version" , "--version":
			fmt.Printf( "Vocab Trainer %s (commit %s, built %s, %s/%s, %s)\n" ,
				version , commit , buildDate , runtime.GOOS , runtime.GOARCH , runtime.Version() )
			return
		case "-h" , "--help" , "help":
			fmt.Printf( "Vocab Trainer %s\n\nUsage:\n  vocab-trainer                 run the web server\n  vocab-trainer manage <cmd>    account administration\n  vocab-trainer version         build information\n\n" , version )
			manage.Usage()
			return
		}
	}

	if err := run(); err != nil {
		log.Fatalf( "[fatal] %v" , err )
	}
}

func run() ( err error ) {
	cfg , err := config.Load()
	if err != nil { return }

	embedded , err := embeddedStatic()
	if err != nil { return }

	// Disk when it exists, the embedded copy otherwise. See server/assets.
	bundle , err := assets.Resolve( cfg.StaticDir , cfg.LanguageFile , embedded , embeddedLanguage )
	if err != nil { return }

	store , err := db.Open( cfg )
	if err != nil { return }
	defer store.Close()

	lang , err := language.Parse( bundle.LanguageYAML )
	if err != nil { return }

	static_server , err := static.New( bundle.Static )
	if err != nil { return }

	if err = bootstrap.EnsureFirstAdmin( store , cfg , "Admin" ); err != nil { return }

	// The control socket is what lets `manage` run against a live server.
	// bolt hands the database to exactly one process, so without it every
	// admin command would need the server stopped first. It is started after
	// the database opens, which means the socket existing is itself proof
	// that a live process holds the lock.
	control_server , err := control.Listen( store , cfg )
	if err != nil { return }
	defer control_server.Close()

	guard := security.New( store , cfg )
	// The word list is compiled into the binary and parsed once here. It is
	// read-only afterwards, so every request shares one copy with no locking.
	words , err := corpus.Load()
	if err != nil {
		log.Fatalf( "[fatal] %v" , err )
	}

	handlers := routes.New( store , cfg , guard , lang , static_server , words )

	app := fiber.New( fiber.Config{
		AppName: cfg.ProjectName,
		// An empty Server header keeps the response from advertising which
		// framework and version is running -- free reconnaissance otherwise.
		ServerHeader: "",
		BodyLimit:    cfg.BodyLimit,
		// Timeouts are what stop a handful of slow or stalled connections
		// from holding sockets open until the process runs out of them.
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  75 * time.Second,
		TrustProxy:   cfg.TrustProxy,
		ProxyHeader:  proxyHeader( cfg ),
		ErrorHandler: func( c fiber.Ctx , handler_err error ) ( response_err error ) {
			// Never hand an internal error message to a client. The detail
			// goes to the log, where the operator can see it.
			code := fiber.StatusInternalServerError
			var fiber_err *fiber.Error
			if ok := asFiberError( handler_err , &fiber_err ); ok {
				code = fiber_err.Code
			}
			if code >= 500 {
				log.Printf( "[error] %s %s: %v" , c.Method() , c.Path() , handler_err )
			}
			response_err = c.Status( code ).JSON( fiber.Map{ "error": statusText( code ) } )
			return
		},
	} )

	app.Use( middleware.Recover( cfg ) )
	app.Use( middleware.SecurityHeaders( cfg ) )
	app.Use( middleware.RateLimit( cfg ) )

	handlers.Register( app )

	stop_purging := startPurgeLoop( store )
	defer close( stop_purging )

	// Shut down on a signal rather than being killed, so in-flight requests
	// finish and bolt closes cleanly instead of leaving a locked file behind.
	shutdown := make( chan os.Signal , 1 )
	signal.Notify( shutdown , os.Interrupt , syscall.SIGTERM )
	go func() {
		<-shutdown
		log.Println( "[shutdown] draining connections..." )
		// Close the control socket first: it is the door other processes
		// come through, and letting a manage command start against a
		// database that is about to close would fail confusingly.
		control_server.Close()
		app.ShutdownWithTimeout( 10 * time.Second )
	}()

	log.Printf( "[start] %s %s listening on %s (secure cookies: %v)" , cfg.ProjectName , version , cfg.ListenAddress() , cfg.SecureCookies )
	log.Printf( "[state]  %s" , cfg.AppDir )
	log.Printf( "[secret] %s" , cfg.SecretKeySource )
	log.Printf( "[assets] %s" , bundle.Source )
	log.Printf( "[corpus] %d words across %d reading levels" , words.Size() , corpus.TierMax )
	log.Printf( "[control] %s" , control_server.Path() )
	err = app.Listen( cfg.ListenAddress() , fiber.ListenConfig{ DisableStartupMessage: true } )
	return
}

func proxyHeader( cfg *config.Config ) ( result string ) {
	if cfg.TrustProxy {
		result = cfg.ProxyHeader
	}
	return
}

func asFiberError( err error , out **fiber.Error ) ( ok bool ) {
	converted , ok := err.( *fiber.Error )
	if ok {
		*out = converted
	}
	return
}

func statusText( code int ) ( result string ) {
	switch {
	case code == fiber.StatusNotFound:
		result = "not found"
	case code == fiber.StatusUnauthorized:
		result = "not authenticated"
	case code == fiber.StatusForbidden:
		result = "forbidden"
	case code == fiber.StatusTooManyRequests:
		result = "too many requests"
	case code >= 500:
		result = "internal error"
	default:
		result = "bad request"
	}
	return
}

// startPurgeLoop drops expired sessions and login tokens on a timer. Neither
// is a correctness issue -- expired records are already refused on read -- so
// this runs quietly in the background purely to keep the bolt file from
// growing forever.
func startPurgeLoop( store *db.Store ) ( stop chan struct{} ) {
	stop = make( chan struct{} )
	go func() {
		ticker := time.NewTicker( time.Hour )
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				sessions , _ := models.PurgeExpiredSessions( store )
				tokens , _ := models.PurgeExpiredLoginTokens( store )
				if sessions > 0 || tokens > 0 {
					fmt.Printf( "[purge] removed %d expired sessions, %d expired login tokens\n" , sessions , tokens )
				}
			}
		}
	}()
	return
}
