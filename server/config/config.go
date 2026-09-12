// Package config is the single source of truth for everything this server can
// be told to do differently, and for where it keeps its state on disk.
//
// Settings resolve in one order, everywhere: environment variable, then
// config.yaml, then the built-in default. Environment wins because that is
// what a container or a systemd unit sets; the file wins over defaults
// because that is what a person edits. No other package reads os.Getenv, so
// "what is configurable" is answerable by reading this one file.
package config

import (
	fmt "fmt"
	os "os"
	filepath "path/filepath"
	strconv "strconv"
	strings "strings"
	time "time"

	yaml "gopkg.in/yaml.v3"

	encryption "vocabtrainer/server/encryption"
)

// AppSlug names the application on disk. It is what turns into
// ~/.config/<slug>/, so it is deliberately the lowercase, filesystem-safe
// form of the project name rather than the display name.
const AppSlug = "vocab-trainer"

// Filenames inside the app directory. Everything this app persists lives
// under one root -- see the package docs on AppDir below for why.
const (
	ConfigFileName    = "config.yaml"
	SecretKeyFileName = "secret.key"
	DatabaseFileName  = "app.db"
	ControlSocketName = "control.sock"
	StorageDirName    = "storage"
	LanguageFileName  = "language.yaml"
)

type Config struct {
	ProjectName string

	// AppDir is the one directory this application owns. The database, the
	// secret key, any uploaded or generated files, and an optional
	// config.yaml all live inside it.
	//
	// Defaulting to ~/.config/<slug> rather than ./data matters for the
	// single-file build: a binary run from a Downloads folder, a USB stick,
	// or a different working directory each time still finds the same state.
	// Writing state next to the executable would scatter half-populated
	// databases wherever it happened to be launched from.
	AppDir string

	ConfigFile    string
	SecretKeyFile string
	DatabasePath  string

	// ControlSocketPath is the unix socket the running server listens on so
	// that other processes can act on the database without opening it.
	//
	// bolt allows exactly one process to hold the database file, so a second
	// process cannot simply open it while the server is up. Rather than
	// fighting that, the server owns the database and everything else --
	// the manage CLI, a cron job, a sidecar -- asks the server over this
	// socket. One process holds the secret key, and there is no second copy
	// of the auth checks to keep in step.
	//
	// Set CONTROL_SOCKET=false to switch it off. Then manage only works
	// while the server is stopped, which is the pre-socket behaviour.
	ControlSocketPath    string
	ControlSocketEnabled bool

	// StorageDir is for application files -- uploads, exports, generated
	// artifacts. Nothing in the base template writes here; it exists so that
	// features added later have an obvious, backed-up place to put files
	// instead of inventing one.
	StorageDir string

	// StaticDir and LanguageFile are checked on disk first; if either is
	// missing, the copy embedded in the binary is used instead (see
	// server/assets). That is what lets one build serve a live directory
	// during development and run as a single self-contained file in
	// production, with no flag to remember.
	StaticDir    string
	LanguageFile string

	Host string
	Port int

	// SecureCookies must stay true anywhere a real browser will talk to this
	// over the internet. It is only false for plain-http://localhost work,
	// because a Secure cookie is silently dropped over http and the login
	// flow would appear to succeed and then do nothing.
	SecureCookies bool
	CookieName    string

	SessionTTL    time.Duration
	LoginTokenTTL time.Duration

	// SecretKey encrypts session cookie payloads and, when EncryptAtRest is
	// on, every value written to bolt. Losing it means losing the database
	// contents. See resolveSecretKey for where it comes from.
	SecretKey     string
	EncryptAtRest bool

	RateLimitMax    int
	RateLimitWindow time.Duration
	BodyLimit       int

	// TrustProxy tells fiber to believe X-Forwarded-For. Only turn this on
	// when something you control (nginx, Caddy) is actually in front, or
	// clients can forge their own IP and walk straight past the rate limiter.
	TrustProxy  bool
	ProxyHeader string

	// SecretKeySource describes how the key was obtained, for the startup log
	// and `manage paths`. SecretKeyOnDisk says whether it lives in
	// SecretKeyFile -- which decides what has to be backed up, so it is a
	// flag rather than something inferred by matching on the source string.
	SecretKeySource string
	SecretKeyOnDisk bool
}

// ---------------------------------------------------------------------------
// where the app directory lives
// ---------------------------------------------------------------------------

// DefaultAppDir resolves the state directory without touching the disk, so
// callers (including build.sh's help text and the manage CLI) can report it.
//
// XDG_CONFIG_HOME is honoured because respecting it costs nothing and is what
// anyone who has set it expects. Everything lands in one directory rather
// than being split across XDG's config/data/state roots: this app's state is
// small, and one directory to back up is worth more here than strict
// adherence to a layout designed for larger, more separable applications.
func DefaultAppDir() ( dir string ) {
	if explicit := strings.TrimSpace( os.Getenv( "APP_DIR" ) ); explicit != "" {
		dir = explicit
		return
	}
	if xdg := strings.TrimSpace( os.Getenv( "XDG_CONFIG_HOME" ) ); xdg != "" {
		dir = filepath.Join( xdg , AppSlug )
		return
	}
	if home , err := os.UserHomeDir(); err == nil && home != "" {
		dir = filepath.Join( home , ".config" , AppSlug )
		return
	}
	// No home directory at all (some minimal containers). Fall back to the
	// working directory rather than failing to start.
	dir = filepath.Join( "." , AppSlug + "-data" )
	return
}

// ---------------------------------------------------------------------------
// config.yaml
// ---------------------------------------------------------------------------

type fileConfig struct {
	values map[string]any
}

func loadFileConfig( path string ) ( file *fileConfig , err error ) {
	file = &fileConfig{ values: map[string]any{} }
	raw , read_err := os.ReadFile( path )
	if read_err != nil {
		// Absent is the normal case, not an error -- env vars and defaults
		// are a complete configuration on their own.
		return
	}
	parsed := map[string]any{}
	if err = yaml.Unmarshal( raw , &parsed ); err != nil {
		err = fmt.Errorf( "could not parse %s: %w" , path , err )
		return
	}
	file.values = parsed
	return
}

func ( file *fileConfig ) lookup( key string ) ( value any , ok bool ) {
	value , ok = file.values[ key ]
	return
}

// ---------------------------------------------------------------------------
// resolution: env > config.yaml > default
// ---------------------------------------------------------------------------

type resolver struct {
	file *fileConfig
}

func ( r *resolver ) str( env_name string , file_key string , fallback string ) ( result string ) {
	if value := strings.TrimSpace( os.Getenv( env_name ) ); value != "" {
		result = value
		return
	}
	if value , ok := r.file.lookup( file_key ); ok {
		if text , is_string := value.( string ); is_string && strings.TrimSpace( text ) != "" {
			result = text
			return
		}
	}
	result = fallback
	return
}

func ( r *resolver ) integer( env_name string , file_key string , fallback int ) ( result int ) {
	if value := strings.TrimSpace( os.Getenv( env_name ) ); value != "" {
		if parsed , err := strconv.Atoi( value ); err == nil {
			result = parsed
			return
		}
	}
	if value , ok := r.file.lookup( file_key ); ok {
		switch typed := value.( type ) {
		case int:
			result = typed
			return
		case float64:
			result = int( typed )
			return
		}
	}
	result = fallback
	return
}

func ( r *resolver ) boolean( env_name string , file_key string , fallback bool ) ( result bool ) {
	value := strings.ToLower( strings.TrimSpace( os.Getenv( env_name ) ) )
	switch value {
	case "1" , "true" , "yes" , "on":
		result = true
		return
	case "0" , "false" , "no" , "off":
		result = false
		return
	}
	if raw , ok := r.file.lookup( file_key ); ok {
		if typed , is_bool := raw.( bool ); is_bool {
			result = typed
			return
		}
	}
	result = fallback
	return
}

func ( r *resolver ) seconds( env_name string , file_key string , fallback time.Duration ) ( result time.Duration ) {
	if seconds := r.integer( env_name , file_key , 0 ); seconds > 0 {
		result = time.Duration( seconds ) * time.Second
		return
	}
	result = fallback
	return
}

// ---------------------------------------------------------------------------
// secret key
// ---------------------------------------------------------------------------

// resolveSecretKey finds the key or creates one.
//
// The precedence exists to serve two very different deployments from one code
// path. A container gets SECRET_KEY from the environment and nothing is ever
// written to disk, which keeps the key out of the mounted data volume. A
// portable binary has nobody to set an environment variable, so it persists a
// generated key inside the app directory and simply works on the next run.
//
// That second case is a deliberate trade, and worth being clear about: with
// the key stored beside the database, at-rest encryption protects a database
// file that leaks on its own -- a stray copy, a backup of just app.db -- but
// not someone who takes the whole directory. Pass SECRET_KEY through the
// environment when that distinction matters; nothing is written then.
func resolveSecretKey( r *resolver , path string ) ( key string , source string , on_disk bool , err error ) {
	if value := strings.TrimSpace( os.Getenv( "SECRET_KEY" ) ); value != "" {
		key , source = value , "environment (SECRET_KEY)"
		return
	}
	if value , ok := r.file.lookup( "secret_key" ); ok {
		if text , is_string := value.( string ); is_string && strings.TrimSpace( text ) != "" {
			key , source = strings.TrimSpace( text ) , ConfigFileName
			return
		}
	}

	if raw , read_err := os.ReadFile( path ); read_err == nil {
		key , source , on_disk = strings.TrimSpace( string( raw ) ) , path , true
		return
	}

	key = encryption.GenerateKeyHex()
	// 0600: this single file is what stands between a stolen backup and every
	// session cookie plus every encrypted record.
	if write_err := os.WriteFile( path , []byte( key + "\n" ) , 0o600 ); write_err != nil {
		err = fmt.Errorf( "could not write %s: %w" , path , write_err )
		return
	}
	source , on_disk = path + " (generated)" , true
	return
}

// ---------------------------------------------------------------------------

// Load reads the environment and config.yaml once at startup, and makes sure
// the app directory exists.
func Load() ( cfg *Config , err error ) {
	app_dir := DefaultAppDir()
	if absolute , abs_err := filepath.Abs( app_dir ); abs_err == nil {
		app_dir = absolute
	}

	// 0700 because the secret key lives in here.
	if mkdir_err := os.MkdirAll( app_dir , 0o700 ); mkdir_err != nil {
		err = fmt.Errorf( "could not create app directory %q: %w" , app_dir , mkdir_err )
		return
	}

	config_file := filepath.Join( app_dir , ConfigFileName )
	file , err := loadFileConfig( config_file )
	if err != nil { return }
	r := &resolver{ file: file }

	storage_dir := filepath.Join( app_dir , StorageDirName )
	if mkdir_err := os.MkdirAll( storage_dir , 0o700 ); mkdir_err != nil {
		err = fmt.Errorf( "could not create storage directory %q: %w" , storage_dir , mkdir_err )
		return
	}

	// An operator can drop a language.yaml into the app directory to reword
	// the UI without rebuilding. Failing that, one in the working directory
	// (a source checkout) is used, and failing that the embedded copy.
	language_file := filepath.Join( app_dir , LanguageFileName )
	if _ , stat_err := os.Stat( language_file ); stat_err != nil {
		language_file = "./" + LanguageFileName
	}

	cfg = &Config{
		ProjectName: r.str( "PROJECT_NAME" , "project_name" , "Vocab Trainer" ),

		AppDir:        app_dir,
		ConfigFile:    config_file,
		SecretKeyFile: filepath.Join( app_dir , SecretKeyFileName ),
		DatabasePath:  filepath.Join( app_dir , DatabaseFileName ),

		ControlSocketPath:    filepath.Join( app_dir , ControlSocketName ),
		ControlSocketEnabled: r.boolean( "CONTROL_SOCKET" , "control_socket" , true ),
		StorageDir:    storage_dir,

		StaticDir:    r.str( "STATIC_DIR" , "static_dir" , "./static" ),
		LanguageFile: r.str( "LANGUAGE_FILE" , "language_file" , language_file ),

		Host: r.str( "HOST" , "host" , "0.0.0.0" ),
		Port: r.integer( "PORT" , "port" , 8080 ),

		SecureCookies: r.boolean( "SECURE_COOKIES" , "secure_cookies" , true ),
		CookieName:    r.str( "COOKIE_NAME" , "cookie_name" , "session" ),

		SessionTTL:    r.seconds( "SESSION_TTL_SECONDS" , "session_ttl_seconds" , 14*24*time.Hour ),
		LoginTokenTTL: r.seconds( "LOGIN_TOKEN_TTL_SECONDS" , "login_token_ttl_seconds" , 30*time.Minute ),

		EncryptAtRest: r.boolean( "ENCRYPT_AT_REST" , "encrypt_at_rest" , true ),

		RateLimitMax:    r.integer( "RATE_LIMIT_MAX" , "rate_limit_max" , 120 ),
		RateLimitWindow: r.seconds( "RATE_LIMIT_WINDOW_SECONDS" , "rate_limit_window_seconds" , time.Minute ),
		BodyLimit:       r.integer( "BODY_LIMIT_BYTES" , "body_limit_bytes" , 1*1024*1024 ),

		TrustProxy:  r.boolean( "TRUST_PROXY" , "trust_proxy" , false ),
		ProxyHeader: r.str( "PROXY_HEADER" , "proxy_header" , "X-Forwarded-For" ),
	}

	cfg.SecretKey , cfg.SecretKeySource , cfg.SecretKeyOnDisk , err = resolveSecretKey( r , cfg.SecretKeyFile )
	if err != nil { return }
	if len( cfg.SecretKey ) != 64 {
		err = fmt.Errorf( "SECRET_KEY must be 64 hex characters (32 bytes), got %d from %s" , len( cfg.SecretKey ) , cfg.SecretKeySource )
		cfg = nil
		return
	}
	return
}

func ( cfg *Config ) ListenAddress() ( result string ) {
	result = fmt.Sprintf( "%s:%d" , cfg.Host , cfg.Port )
	return
}
