package control

import (
	bytes "bytes"
	context "context"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	io "io"
	net "net"
	http "net/http"
	time "time"

	config "vocabtrainer/server/config"
)

// ErrNoServer means nothing is listening on the control socket, which is the
// normal state on a fresh install or with the server stopped. Callers treat
// it as "fall back to opening the database directly" rather than as failure.
var ErrNoServer = errors.New( "control: no server is listening" )

// Client talks to a running server over the control socket. The host in the
// URL is a placeholder -- the transport ignores it and dials the socket path
// instead -- but net/http still requires one to be present.
type Client struct {
	http *http.Client
	path string
}

// Dial connects to the control socket, returning ErrNoServer if no server
// holds it. It performs a real request rather than only opening the socket,
// so a socket file left behind by a killed process is reported as absent
// instead of appearing live and then hanging on first use.
func Dial( cfg *config.Config ) ( client *Client , err error ) {
	if cfg.ControlSocketEnabled == false {
		err = ErrNoServer
		return
	}

	socket_path := cfg.ControlSocketPath
	// Same limit the listener checks. Dialing an over-long path fails with
	// the same unhelpful error, and here it simply means no server.
	if len( socket_path ) > maxSocketPathLength {
		err = ErrNoServer
		return
	}
	candidate := &Client{
		path: socket_path,
		http: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				DialContext: func( ctx context.Context , network string , address string ) ( conn net.Conn , dial_err error ) {
					dialer := net.Dialer{}
					conn , dial_err = dialer.DialContext( ctx , "unix" , socket_path )
					return
				},
			},
		},
	}

	if _ , health_err := candidate.Health(); health_err != nil {
		err = ErrNoServer
		return
	}
	client = candidate
	return
}

// Close releases the transport's idle connections. The socket itself belongs
// to the server process, so there is nothing here to unlink.
func ( client *Client ) Close() ( err error ) {
	if client == nil {
		return
	}
	client.http.CloseIdleConnections()
	return
}

// Path reports the socket this client is bound to, so a caller can say where
// it sent a command.
func ( client *Client ) Path() ( result string ) {
	result = client.path
	return
}

// errorBody is the shape every control error response uses.
type errorBody struct {
	Error string `json:"error"`
}

// do performs one request and decodes the response into out. A non-2xx status
// is turned into the server's own error message, so the CLI reports the same
// wording the server used rather than a bare status code.
func ( client *Client ) do( method string , path string , body any , out any ) ( err error ) {
	var payload io.Reader
	if body != nil {
		encoded , encode_err := json.Marshal( body )
		if encode_err != nil {
			err = encode_err
			return
		}
		payload = bytes.NewReader( encoded )
	}

	request , err := http.NewRequest( method , "http://control"+path , payload )
	if err != nil { return }
	if body != nil {
		request.Header.Set( "Content-Type" , "application/json" )
	}

	response , err := client.http.Do( request )
	if err != nil { return }
	defer response.Body.Close()

	raw , err := io.ReadAll( io.LimitReader( response.Body , maxRequestBytes ) )
	if err != nil { return }

	if response.StatusCode < 200 || response.StatusCode > 299 {
		failure := errorBody{}
		if json.Unmarshal( raw , &failure ) == nil && failure.Error != "" {
			err = errors.New( failure.Error )
			return
		}
		err = fmt.Errorf( "control server returned %s" , response.Status )
		return
	}

	if out != nil {
		err = json.Unmarshal( raw , out )
	}
	return
}

func ( client *Client ) Health() ( health HealthResponse , err error ) {
	err = client.do( http.MethodGet , "/v1/health" , nil , &health )
	return
}

func ( client *Client ) ListUsers() ( users []UserResponse , err error ) {
	users = []UserResponse{}
	err = client.do( http.MethodGet , "/v1/users" , nil , &users )
	return
}

func ( client *Client ) CreateUser( display_name string , role string ) ( user UserResponse , err error ) {
	err = client.do( http.MethodPost , "/v1/users" ,
		CreateUserRequest{ DisplayName: display_name , Role: role } , &user )
	return
}

func ( client *Client ) ReissueLogin( user_id uint64 ) ( user UserResponse , err error ) {
	err = client.do( http.MethodPost , fmt.Sprintf( "/v1/users/%d/login-token" , user_id ) , nil , &user )
	return
}

func ( client *Client ) SetDisabled( user_id uint64 , disabled bool ) ( user UserResponse , err error ) {
	err = client.do( http.MethodPost , fmt.Sprintf( "/v1/users/%d/disabled" , user_id ) ,
		SetDisabledRequest{ Disabled: disabled } , &user )
	return
}
