// Package control is the one door into the database for every process that is
// not the web server.
//
// bolt takes an exclusive lock on its file, so exactly one process can have
// the database open at a time. Rather than working around that, this package
// leans on it: the server process owns the database, and anything else that
// needs to act on it -- the manage CLI, a cron job, a sidecar -- asks the
// server to do so over a unix socket.
//
// That is the cheapest answer available in security terms. There is no second
// process holding the secret key, no second copy of the model layer's rules
// to keep in step, and no new network surface: the socket is a file, and it
// is reachable only by something that can already read the key.
//
// # Why there is no token on this socket
//
// There is deliberately no password, token, or handshake here. The socket
// lives in AppDir, which is mode 0700, next to app.db, and only the user that
// owns config.yaml (mode 0600, holding the key) can reach it. Any process
// that can open the socket can already read the key and the database
// directly, so a credential would guard nothing while adding one more secret
// to store and rotate. The filesystem permission is the authentication.
//
// This is also why the socket must never be moved somewhere world-readable,
// and why the server refuses to bind it over a TCP address: the whole
// argument depends on the socket being as hard to reach as the database file.
//
// # In the container
//
// The container's data directory is usually shared from the host, and such a
// mount may be unable to hold a socket at all (colima's refuses the bind). So
// the image pins the socket to /tmp/control on the container's tmpfs instead
// (server/config/layout_container.go). The argument above still holds, for
// these reasons:
//
// /tmp itself is open to every user, so the socket never sits there directly.
// Listen makes /tmp/control mode 0700 and refuses to use it unless it is a
// real directory that this user owns and nobody else can enter. Anything
// another user planted first stops the server rather than being trusted.
//
// That leaves the container's own user and root, who can both read the key
// and the database already. Nothing outside the container can reach the
// socket short of `docker exec`, because a tmpfs is private to its container:
// that is tighter than a socket in a folder the host shares.
package control

import (
	context "context"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	fs "io/fs"
	log "log"
	net "net"
	http "net/http"
	os "os"
	filepath "path/filepath"
	runtime "runtime"
	strconv "strconv"
	time "time"

	backup "vocabtrainer/server/backup"
	config "vocabtrainer/server/config"
	db "vocabtrainer/server/db"
	models "vocabtrainer/server/models"
)

// maxRequestBytes caps a control request body. Nothing this API accepts is
// large, and a local caller is trusted, but a bounded reader costs nothing and
// keeps a malformed client from being able to exhaust memory.
const maxRequestBytes = 64 * 1024

// maxSocketPathLength is the kernel's limit on a unix socket path: the
// sun_path field is 104 bytes on macOS and the BSDs, 108 on Linux, including
// the terminator. 103 is the safe number everywhere.
//
// This is worth checking by hand because the failure is otherwise a bare
// "bind: invalid argument" from the kernel, which names neither the limit nor
// the path that broke it. A deep server.app_dir is all it takes.
const maxSocketPathLength = 103

// Server is the control listener. It is created after the database is open,
// so its existence is itself the signal that a live server holds the lock.
type Server struct {
	store    *db.Store
	settings *config.Holder
	http     *http.Server
	listener net.Listener
	path     string
}

// Listen binds the control socket and starts serving in the background.
//
// A socket file left behind by a crashed process is removed first. That is
// safe precisely because bolt already granted us the database lock: if this
// process holds the lock, no live server is using that stale socket.
// The socket itself is set up from the configuration the server started
// with; what a request may do (API keys on or off, link lifetimes) is read
// from the running one, so it follows edits to config.yaml.
func Listen( store *db.Store , settings *config.Holder ) ( server *Server , err error ) {
	cfg := settings.Boot()
	if cfg.ControlSocketEnabled == false {
		return
	}

	if len( cfg.ControlSocketPath ) > maxSocketPathLength {
		err = fmt.Errorf(
			"control socket path is %d characters, past the %d the kernel allows: %s\n"+
				"Set server.app_dir in config.yaml to a shorter path, or server.control_socket: false to run without the socket -- manage will then need the server stopped." ,
			len( cfg.ControlSocketPath ) , maxSocketPathLength , cfg.ControlSocketPath )
		return
	}

	// The app directory has its own permissions, set when it was made. Any
	// other home for the socket has to earn them here.
	if socket_dir := filepath.Dir( cfg.ControlSocketPath ); socket_dir != cfg.AppDir {
		if err = privateDir( socket_dir ); err != nil {
			return
		}
	}

	if _ , stat_err := os.Stat( cfg.ControlSocketPath ); stat_err == nil {
		if remove_err := os.Remove( cfg.ControlSocketPath ); remove_err != nil {
			err = fmt.Errorf( "could not clear stale control socket %s: %w" , cfg.ControlSocketPath , remove_err )
			return
		}
	}

	listener , err := net.Listen( "unix" , cfg.ControlSocketPath )
	if err != nil {
		err = fmt.Errorf( "could not bind control socket %s: %w" , cfg.ControlSocketPath , err )
		return
	}

	// Belt and braces on top of the 0700 directory around it. On Windows this is a
	// no-op and the directory ACL is what protects the socket -- see the
	// package docs; the security argument is the same either way, but it is
	// worth knowing which mechanism is doing the work.
	if runtime.GOOS != "windows" {
		if chmod_err := os.Chmod( cfg.ControlSocketPath , 0o600 ); chmod_err != nil {
			listener.Close()
			os.Remove( cfg.ControlSocketPath )
			err = fmt.Errorf( "could not restrict control socket permissions: %w" , chmod_err )
			return
		}
	}

	server = &Server{
		store:    store,
		settings: settings,
		listener: listener,
		path:     cfg.ControlSocketPath,
	}
	server.http = &http.Server{
		Handler: server.routes(),
		// A local peer should never be slow, so a short header timeout costs
		// nothing here and keeps a wedged client from holding a connection.
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		serve_err := server.http.Serve( listener )
		if serve_err != nil && errors.Is( serve_err , http.ErrServerClosed ) == false {
			log.Printf( "[control] listener stopped: %v" , serve_err )
		}
	}()
	return
}

// privateDir makes dir, or accepts it if it is already what making it would
// have produced: a real directory, owned by this user, that nobody else can
// enter. It is called for a directory inside /tmp, where anyone can get there
// first, so the check is the point and an existing directory earns no trust.
// Lstat rather than Stat, or a symlink would be judged by what it points at.
func privateDir( dir string ) ( err error ) {
	if mkdir_err := os.Mkdir( dir , 0o700 ); mkdir_err != nil && errors.Is( mkdir_err , fs.ErrExist ) == false {
		err = fmt.Errorf(
			"could not create control socket directory %s: %w\n"+
				"docker/run.sh mounts a tmpfs at /tmp for this. Without one, set server.control_socket: false -- manage will then need the server stopped." ,
			dir , mkdir_err )
		return
	}

	info , err := os.Lstat( dir )
	if err != nil {
		err = fmt.Errorf( "could not check control socket directory %s: %w" , dir , err )
		return
	}
	if info.IsDir() == false || config.OwnedByProcess( info ) == false || info.Mode().Perm()&0o077 != 0 {
		err = fmt.Errorf(
			"control socket directory %s is not private to this user (%s), so anyone who can reach it could run admin commands. Remove it and start again." ,
			dir , info.Mode() )
	}
	return
}

// Close stops serving and unlinks the socket, so a later start does not have
// to reason about whether a leftover file means a live server.
func ( server *Server ) Close() ( err error ) {
	if server == nil {
		return
	}
	ctx , cancel := context.WithTimeout( context.Background() , 5*time.Second )
	defer cancel()
	server.http.Shutdown( ctx )
	os.Remove( server.path )
	return
}

// Path reports where the socket was bound, for the startup log.
func ( server *Server ) Path() ( result string ) {
	if server == nil {
		result = "disabled"
		return
	}
	result = server.path
	return
}

func ( server *Server ) routes() ( handler http.Handler ) {
	mux := http.NewServeMux()
	mux.HandleFunc( "GET /v1/health" , server.handleHealth )
	mux.HandleFunc( "GET /v1/users" , server.handleListUsers )
	mux.HandleFunc( "POST /v1/users" , server.handleCreateUser )
	mux.HandleFunc( "POST /v1/users/{id}/login-token" , server.handleReissueLogin )
	mux.HandleFunc( "POST /v1/users/{id}/disabled" , server.handleSetDisabled )
	mux.HandleFunc( "GET /v1/invites" , server.handleListInvites )
	mux.HandleFunc( "POST /v1/invites" , server.handleCreateInvite )
	mux.HandleFunc( "POST /v1/invites/{id}/revoke" , server.handleRevokeInvite )
	mux.HandleFunc( "POST /v1/users/{id}/revoke-access" , server.handleRevokeAccess )
	mux.HandleFunc( "POST /v1/users/{id}/delete" , server.handleDeleteUser )
	mux.HandleFunc( "POST /v1/owner" , server.handleTransferOwner )
	mux.HandleFunc( "POST /v1/backup" , server.handleBackup )
	handler = mux
	return
}

// writeJSON is the single place a control response is produced, so every
// handler answers in the same shape.
func writeJSON( w http.ResponseWriter , status int , payload any ) {
	w.Header().Set( "Content-Type" , "application/json" )
	w.WriteHeader( status )
	json.NewEncoder( w ).Encode( payload )
}

func writeError( w http.ResponseWriter , status int , message string ) {
	writeJSON( w , status , map[ string ]string{ "error": message } )
}

func decodeBody( w http.ResponseWriter , r *http.Request , out any ) ( ok bool ) {
	decoder := json.NewDecoder( http.MaxBytesReader( w , r.Body , maxRequestBytes ) )
	decoder.DisallowUnknownFields()
	if err := decoder.Decode( out ); err != nil {
		writeError( w , http.StatusBadRequest , fmt.Sprintf( "malformed request body: %v" , err ) )
		return
	}
	ok = true
	return
}

func pathUserID( w http.ResponseWriter , r *http.Request ) ( user_id uint64 , ok bool ) {
	user_id , err := strconv.ParseUint( r.PathValue( "id" ) , 10 , 64 )
	if err != nil {
		writeError( w , http.StatusBadRequest , "user id must be a positive integer" )
		return
	}
	ok = true
	return
}

// BackupRequest is the body of POST /v1/backup.
type BackupRequest struct {
	Out        string `json:"out"`
	IncludeKey bool   `json:"include_key"`
}

// handleBackup is why a backup can run against a live server: bolt lets only
// this process read the file, so the copy has to be made from in here.
func ( server *Server ) handleBackup( w http.ResponseWriter , r *http.Request ) {
	request := BackupRequest{}
	if decodeBody( w , r , &request ) == false { return }
	result , err := backup.Create( server.store , server.settings.Get() , backup.Options{ Out: request.Out , IncludeKey: request.IncludeKey } )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	writeJSON( w , http.StatusOK , result )
}

// HealthResponse tells a client it is talking to a live server, and which one.
type HealthResponse struct {
	ProjectName string `json:"project_name"`
	AppDir      string `json:"app_dir"`
}

// UserResponse is a stored user plus the fields a caller would otherwise have
// to recompute. Credential is set only by the two endpoints that mint a login
// link, and is the one moment it is ever visible.
type UserResponse struct {
	ID          uint64 `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
	Owner       bool   `json:"owner"`
	Credential  string `json:"credential,omitempty"`
	// LoginLink describes the link Credential opens: how often, how long.
	LoginLink *models.LoginLinkInfo `json:"login_link,omitempty"`
}

func userResponse( user *models.User , owner_id uint64 , credential string ) ( result UserResponse ) {
	result = UserResponse{
		ID:          user.ID,
		DisplayName: user.DisplayName,
		Role:        user.Role,
		Disabled:    user.Disabled(),
		Owner:       user.ID == owner_id,
		Credential:  credential,
	}
	return
}

// InviteResponse is a stored invite plus what a caller would otherwise have
// to recompute. Credential is set only by the endpoint that mints one, and is
// the one moment it is ever visible -- only its hash is stored, so there is
// no endpoint that could show it again.
type InviteResponse struct {
	ID         string    `json:"id"`
	Label      string    `json:"label,omitempty"`
	Role       string    `json:"role"`
	MaxUses    int       `json:"max_uses"`
	UsedCount  int       `json:"used_count"`
	SeatsLeft  int       `json:"seats_left"`
	Usable     bool      `json:"usable"`
	Status     string    `json:"status"`
	ExpiresAt  time.Time `json:"expires_at"`
	Credential string    `json:"credential,omitempty"`
}

// inviteStatus is the CLI-facing word for why a link will not work. It
// mirrors the reason codes the web API returns, so the two describe the same
// invite the same way.
func inviteStatus( invite *models.Invite ) ( status string ) {
	switch err := invite.Usable(); {
	case err == nil:
		status = "live"
	case errors.Is( err , models.ErrInviteRevoked ):
		status = "withdrawn"
	case errors.Is( err , models.ErrInviteExpired ):
		status = "expired"
	case errors.Is( err , models.ErrInviteFull ):
		status = "full"
	default:
		status = "unusable"
	}
	return
}

// NewInviteResponse is exported because manage's direct-database backend has
// to produce the same shape when no server is running. Without it there would
// be two conversions to keep in step, which is exactly the drift the backend
// interface in that package exists to avoid.
func NewInviteResponse( invite_id string , invite *models.Invite , credential string ) ( result InviteResponse ) {
	result = inviteResponse( invite_id , invite , credential )
	return
}

func inviteResponse( invite_id string , invite *models.Invite , credential string ) ( result InviteResponse ) {
	result = InviteResponse{
		ID:         invite_id,
		Label:      invite.Label,
		Role:       invite.Role,
		MaxUses:    invite.MaxUses,
		UsedCount:  invite.UsedCount,
		SeatsLeft:  invite.SeatsLeft(),
		Usable:     invite.Usable() == nil,
		Status:     inviteStatus( invite ),
		ExpiresAt:  invite.ExpiresAt,
		Credential: credential,
	}
	return
}

// CreateInviteRequest is the body of POST /v1/invites.
type CreateInviteRequest struct {
	Label   string `json:"label"`
	Role    string `json:"role"`
	MaxUses int    `json:"max_uses"`
}

// TransferOwnerRequest is the body of POST /v1/owner.
type TransferOwnerRequest struct {
	UserID uint64 `json:"user_id"`
}

// writeOwnedUser re-reads a user and the owner marker and answers with both,
// so every response reflects what is actually stored.
func ( server *Server ) writeOwnedUser( w http.ResponseWriter , user_id uint64 , credential string , link *models.LoginLinkInfo ) {
	user , err := models.GetUser( server.store , user_id )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	owner_id , err := models.OwnerID( server.store )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	result := userResponse( user , owner_id , credential )
	result.LoginLink = link
	writeJSON( w , http.StatusOK , result )
}

// CreateUserRequest is the body of POST /v1/users.
type CreateUserRequest struct {
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	models.LoginLinkRequest
}

// loginLink resolves a link request against the live config, as the web
// routes do.
func ( server *Server ) loginLink( w http.ResponseWriter , request models.LoginLinkRequest ) ( options models.LoginLinkOptions , ok bool ) {
	options , err := models.ResolveLoginLink( request , server.cfg().LoginTokenTTL , server.cfg().MultiUseLoginTokenTTL )
	if err != nil {
		writeError( w , http.StatusBadRequest , err.Error() )
		return
	}
	ok = true
	return
}

// SetDisabledRequest is the body of POST /v1/users/{id}/disabled.
type SetDisabledRequest struct {
	Disabled bool `json:"disabled"`
}













func ( server *Server ) handleHealth( w http.ResponseWriter , r *http.Request ) {
	writeJSON( w , http.StatusOK , HealthResponse{
		ProjectName: server.cfg().ProjectName,
		AppDir:      server.cfg().AppDir,
	} )
}

func ( server *Server ) handleListUsers( w http.ResponseWriter , r *http.Request ) {
	users , err := models.ListUsers( server.store )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	owner_id , err := models.OwnerID( server.store )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	out := []UserResponse{}
	for _ , user := range users {
		out = append( out , userResponse( user , owner_id , "" ) )
	}
	writeJSON( w , http.StatusOK , out )
}

// handleCreateUser validates through the same models helpers the web admin
// routes use, so a user created over the socket cannot be shaped differently
// from one created in the UI.
func ( server *Server ) handleCreateUser( w http.ResponseWriter , r *http.Request ) {
	body := CreateUserRequest{}
	if decodeBody( w , r , &body ) == false { return }

	if models.ValidDisplayName( body.DisplayName ) == false {
		writeError( w , http.StatusBadRequest , "display name must be 1-80 characters" )
		return
	}
	if models.ValidRole( body.Role ) == false {
		writeError( w , http.StatusBadRequest , "role must be admin or user" )
		return
	}
	options , ok := server.loginLink( w , body.LoginLinkRequest )
	if ok == false { return }

	user , err := models.CreateUser( server.store , body.DisplayName , body.Role )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	credential , info , err := models.IssueLoginLink( server.store , user.ID , options )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	server.writeOwnedUser( w , user.ID , credential , &info )
}

func ( server *Server ) handleReissueLogin( w http.ResponseWriter , r *http.Request ) {
	user_id , ok := pathUserID( w , r )
	if ok == false { return }

	// The body is optional: no body is a link that works once.
	body := models.LoginLinkRequest{}
	if r.ContentLength != 0 && decodeBody( w , r , &body ) == false { return }
	options , ok := server.loginLink( w , body )
	if ok == false { return }

	user , err := models.GetUser( server.store , user_id )
	if err != nil {
		writeError( w , http.StatusNotFound , fmt.Sprintf( "no user with id %d" , user_id ) )
		return
	}
	credential , info , err := models.IssueLoginLink( server.store , user.ID , options )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	server.writeOwnedUser( w , user.ID , credential , &info )
}

// handleSetDisabled goes through models.SetUserDisabled, which deletes the
// account's sessions and live login links in the same transaction as the
// change. Keeping that in the model rather than in each caller is what keeps
// "disabled" meaning "logged out now" no matter which door the change came
// through.
func ( server *Server ) handleSetDisabled( w http.ResponseWriter , r *http.Request ) {
	user_id , ok := pathUserID( w , r )
	if ok == false { return }

	body := SetDisabledRequest{}
	if decodeBody( w , r , &body ) == false { return }

	user , err := models.GetUser( server.store , user_id )
	if err != nil {
		writeError( w , http.StatusNotFound , fmt.Sprintf( "no user with id %d" , user_id ) )
		return
	}
	if err = models.SetUserDisabled( server.store , user.ID , body.Disabled ); err != nil {
		if errors.Is( err , models.ErrOwnerProtected ) {
			writeError( w , http.StatusForbidden , err.Error() )
			return
		}
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}

	// Re-read rather than patching the copy in hand, so the response
	// reflects what is actually stored.
	server.writeOwnedUser( w , user.ID , "" , nil )
}

// handleRevokeAccess is models.RevokeAllCredentials over the socket: every
// session, live login link, and live API key the account holds stops
// working now. The account stays enabled.
//
// Unlike the web route, this one may cut off the owner: when it is the
// owner's own credentials that leaked, the operator is who cleans up.
func ( server *Server ) handleRevokeAccess( w http.ResponseWriter , r *http.Request ) {
	user_id , ok := pathUserID( w , r )
	if ok == false { return }

	if _ , err := models.GetUser( server.store , user_id ); err != nil {
		writeError( w , http.StatusNotFound , fmt.Sprintf( "no user with id %d" , user_id ) )
		return
	}
	revoked , err := models.RevokeAllCredentials( server.store , user_id , true )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	writeJSON( w , http.StatusOK , revoked )
}


// handleDeleteUser is models.DeleteUser with the operator as the actor: any
// account but the owner can be deleted, admins included. The owner has to be
// moved first with POST /v1/owner.
func ( server *Server ) handleDeleteUser( w http.ResponseWriter , r *http.Request ) {
	user_id , ok := pathUserID( w , r )
	if ok == false { return }

	removed , err := models.DeleteUser( server.store , user_id , 0 )
	switch {
	case err == nil:
		writeJSON( w , http.StatusOK , removed )
	case errors.Is( err , db.ErrNotFound ):
		writeError( w , http.StatusNotFound , fmt.Sprintf( "no user with id %d" , user_id ) )
	case errors.Is( err , models.ErrOwnerProtected ):
		writeError( w , http.StatusForbidden , err.Error() )
	default:
		writeError( w , http.StatusInternalServerError , err.Error() )
	}
}

// handleTransferOwner moves ownership to another enabled admin. There is no
// web route for this on purpose -- see models.TransferOwner.
func ( server *Server ) handleTransferOwner( w http.ResponseWriter , r *http.Request ) {
	body := TransferOwnerRequest{}
	if decodeBody( w , r , &body ) == false { return }

	err := models.TransferOwner( server.store , body.UserID )
	switch {
	case err == nil:
		server.writeOwnedUser( w , body.UserID , "" , nil )
	case errors.Is( err , db.ErrNotFound ):
		writeError( w , http.StatusNotFound , fmt.Sprintf( "no user with id %d" , body.UserID ) )
	case errors.Is( err , models.ErrNotAdmin ):
		writeError( w , http.StatusBadRequest , err.Error() )
	default:
		writeError( w , http.StatusInternalServerError , err.Error() )
	}
}

func ( server *Server ) cfg() ( cfg *config.Config ) {
	cfg = server.settings.Get()
	return
}

// handleListInvites and the two below it validate through the same models
// helpers the web routes use, so an invite minted over the socket cannot be
// shaped differently from one minted in the UI.
func ( server *Server ) handleListInvites( w http.ResponseWriter , r *http.Request ) {
	records , err := models.ListInvites( server.store )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	out := []InviteResponse{}
	for _ , record := range records {
		out = append( out , inviteResponse( record.ID , record.Invite , "" ) )
	}
	writeJSON( w , http.StatusOK , out )
}

// handleCreateInvite attributes the invite to user 0, which is nobody: a
// command run from a shell has no signed-in admin behind it. The field is for
// an audit trail, and recording a real id that did not do it would be worse
// than recording none.
func ( server *Server ) handleCreateInvite( w http.ResponseWriter , r *http.Request ) {
	body := CreateInviteRequest{}
	if decodeBody( w , r , &body ) == false { return }

	if models.ValidRole( body.Role ) == false {
		writeError( w , http.StatusBadRequest , "role must be admin or user" )
		return
	}
	if models.ValidInviteUses( body.MaxUses ) == false {
		writeError( w , http.StatusBadRequest , "number of uses must be between 1 and 100" )
		return
	}
	if models.ValidInviteLabel( body.Label ) == false {
		writeError( w , http.StatusBadRequest , "label must be 80 characters or fewer" )
		return
	}

	invite_id , credential , err := models.IssueInvite(
		server.store , 0 , body.Role , body.Label , body.MaxUses , server.cfg().InviteTTL )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	invite , err := models.GetInvite( server.store , invite_id )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	writeJSON( w , http.StatusOK , inviteResponse( invite_id , invite , credential ) )
}

func ( server *Server ) handleRevokeInvite( w http.ResponseWriter , r *http.Request ) {
	invite_id := r.PathValue( "id" )
	if _ , err := models.GetInvite( server.store , invite_id ); err != nil {
		writeError( w , http.StatusNotFound , fmt.Sprintf( "no invite with id %s" , invite_id ) )
		return
	}
	if err := models.RevokeInvite( server.store , invite_id ); err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}

	// Re-read rather than patching the copy in hand, so the response
	// reflects what is actually stored.
	invite , err := models.GetInvite( server.store , invite_id )
	if err != nil {
		writeError( w , http.StatusInternalServerError , err.Error() )
		return
	}
	writeJSON( w , http.StatusOK , inviteResponse( invite_id , invite , "" ) )
}
