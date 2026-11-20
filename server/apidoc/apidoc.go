// Package apidoc writes the API reference -- config/api.json and
// config/api.md -- from the code, so nobody edits either by hand.
//
// Every route is declared once, next to its handler, in a // @route(...)
// comment (see route.go and routegen): a summary, who may call it, and the Go types of
// what it takes and gives back. The struct tags on those types carry the rest
// (see reflect.go). Build walks the routes the app really registers, each
// mounted by the generated route table with Mount, so the paths and methods
// come from the route table and a route mounted any other way is reported.
//
// Nothing here runs while the server is up. scripts/api_docs.sh runs
// TestAPIDocsAreCurrent with -update, which writes the files; the same test
// fails while a committed file is out of date.
package apidoc

import (
	fmt "fmt"
	reflect "reflect"
	regexp "regexp"
	sort "sort"
	strconv "strconv"
	strings "strings"
)

// Title heads both files.
const Title = "VocabTrainer API"

// Version is info.version in api.json.
const Version = "1.0.0"

// About is the opening paragraph of both files.
const About = "Every route this server answers, apart from the pages at /login/*, /join/* and /docs/*. Generated from the code by scripts/api_docs.sh: an app's own routes get their docs the same way, and a test fails until they do."

// Access says who may call a route. The zero value is anyone signed in, by a
// browser session or an API key.
type Access int

const (
	Signed Access = iota
	// Public needs no sign-in at all.
	Public
	// SessionOnly needs a browser session: an API key is turned away.
	SessionOnly
	// KeyOnly needs an API key.
	KeyOnly
)

// Roles for Op.Role. Empty is any signed-in user.
const (
	Admin = "admin"
	Owner = "owner"
)

// Op is one route's docs: what its types do not say.
type Op struct {
	Summary     string
	Description string
	Tag         string
	Access      Access
	Role        string
	// Feature is the optional part of the template the route belongs to; it
	// becomes x-feature on the path, which scaffold.py's prune step reads.
	Feature string
	// MCPHTTP is why a route an API key may call has no MCP tool.
	MCPHTTP string
	// BulkOf, on a bulk route, is the route it runs once per item:
	// x-bulk-of in api.json (see bulk.go).
	BulkOf string
	// NoCSRF lets a browser session's POST through without its csrf_token.
	// Only for a route the page calls before it has one.
	NoCSRF bool
	// RawBody is the type of a JSON body the handler reads itself (a
	// JSON-RPC message, say): documented, but neither parsed nor checked,
	// and no csrf_token is looked for in it.
	RawBody any
	// BodyType is the body's content type when it is not JSON: raw bytes
	// the handler reads itself. A browser session then sends its csrf token
	// in the X-CSRF-Token header.
	BodyType string
	// Status is the success status, 200 when zero. ReplyType is the content
	// type of what a Stream route writes: text, bytes for
	// application/octet-stream, or application/json for a JSON object the
	// docs do not describe. A Stream route without one answers with its
	// status alone.
	Status           int
	ReplyType        string
	ReplyDescription string
	// Errors are the error statuses the route can answer with, besides 429
	// and 503, which every route can.
	Errors []int
}

// Route is one entry of the app's route table.
type Route struct {
	Method string
	Path   string
	// Name is the route table's name for the route: Mount and MountStream
	// mark the routes they mount with it.
	Name string
}

var routeParameter = regexp.MustCompile( `:(\w+)` )
var specParameter = regexp.MustCompile( `\{(\w+)\}` )
var notWord = regexp.MustCompile( `[^a-z0-9]+` )

// SpecPath is a route's path the way the spec writes it: /users/{user_id}.
func SpecPath( path string ) ( result string ) {
	result = routeParameter.ReplaceAllString( path , "{$1}" )
	return
}

// Build is api.json for these routes, and what is wrong with the docs: a
// route that was not mounted with Mount, so has no declaration.
func Build( routes []Route ) ( spec map[string]any , problems []string ) {
	builder := newSchemas()
	paths := map[string]any{}
	tags := map[string]bool{}
	for _ , route := range routes {
		path := SpecPath( route.Path )
		label := route.Method + " " + path
		if strings.HasPrefix( route.Name , "apidoc:" ) == false {
			problems = append( problems , label + ": not declared: give its handler a // @route(...) and run scripts/api_docs.sh" )
			continue
		}
		found := lookup( strings.TrimPrefix( route.Name , "apidoc:" ) )
		if found == nil {
			problems = append( problems , label + ": its declaration is missing" )
			continue
		}
		item , _ := paths[ path ].( map[string]any )
		if item == nil {
			item = map[string]any{}
			paths[ path ] = item
		}
		if found.op.Feature != "" { item[ "x-feature" ] = found.op.Feature }
		operation , operation_problems := builder.operation( route.Method , path , found )
		for _ , problem := range operation_problems { problems = append( problems , label + ": " + problem ) }
		item[ strings.ToLower( route.Method ) ] = operation
		tags[ found.op.Tag ] = true
	}
	sort.Strings( problems )
	builder.named( reflect.TypeOf( ErrorReply{} ) )
	builder.named( reflect.TypeOf( MaintenanceReply{} ) )
	responses := map[string]any{}
	for _ , status := range errorStatuses {
		target := "Error"
		if status == 503 { target = "Maintenance" }
		responses[ "E" + strconv.Itoa( status ) ] = map[string]any{
			"description": errorMeanings[ status ],
			"content":     jsonContent( ref( target ) ),
		}
	}
	tag_list := []any{}
	for _ , name := range tagOrder( tags ) { tag_list = append( tag_list , map[string]any{ "name": name } ) }
	spec = map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       Title,
			"version":     Version,
			"description": About,
		},
		"tags":  tag_list,
		"paths": paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"sessionCookie": map[string]any{ "type": "apiKey" , "in": "cookie" , "name": "session" , "description": "A browser session. Its POSTs carry csrf_token in the body. The cookie's name is server.cookie_name, with the __Host- prefix while server.secure_cookies is on." },
				"apiKey":        map[string]any{ "type": "http" , "scheme": "bearer" , "description": "An API key, as Authorization: Bearer <id>.<secret>." },
			},
			"responses": responses,
			"schemas":   builder.components,
		},
	}
	return
}

// TagOrder is the order tags appear in, in both files. A tag not listed here
// comes after these, alphabetically.
var TagOrder = []string{ "Account" , "Sign-in" , "API keys" , "Files" , "Admin" , "Invite links" , "Owner" , "Webhooks" , "Monitoring" }

func tagOrder( used map[string]bool ) ( result []string ) {
	known := map[string]bool{}
	for _ , name := range TagOrder {
		known[ name ] = true
		if used[ name ] { result = append( result , name ) }
	}
	rest := []string{}
	for name := range used {
		if known[ name ] == false { rest = append( rest , name ) }
	}
	sort.Strings( rest )
	result = append( result , rest... )
	return
}

var errorStatuses = []int{ 400 , 401 , 403 , 404 , 409 , 429 , 503 }

var errorMeanings = map[int]string{
	400: "The request was refused as it stands.",
	401: "Not signed in.",
	403: "Not allowed, or a bad csrf_token.",
	404: "No such thing, or this feature is switched off.",
	409: "The record has moved on: an undo too late, or a stale edit.",
	429: "Too many requests.",
	503: "In maintenance mode. Retry-After says when to try again.",
}

// ErrorReply is the body of every error.
type ErrorReply struct {
	Error     string `json:"error"`
	RequestID string `json:"request_id,omitempty" jsonschema_description:"On a 5xx: the reference that finds this failure in the log."`
}

func ( ErrorReply ) APIName() ( name string ) { name = "Error"; return }

// MaintenanceReply is the body of a 503 in maintenance mode.
type MaintenanceReply struct {
	Error       string `json:"error"`
	Maintenance string `json:"maintenance" jsonschema:"enum=read_only,enum=full"`
}

func ( MaintenanceReply ) APIName() ( name string ) { name = "Maintenance"; return }

func ( builder *schemas ) operation( method string , path string , typed *declared ) ( operation map[string]any , problems []string ) {
	op := typed.op
	operation = map[string]any{
		"summary":     op.Summary,
		"operationId": strings.Trim( notWord.ReplaceAllString( strings.ToLower( method + " " + path ) , "_" ) , "_" ),
		"tags":        []any{ op.Tag },
	}
	if op.Summary == "" { problems = append( problems , "no Summary" ) }
	if op.Tag == "" { problems = append( problems , "no Tag" ) }
	if op.Description != "" { operation[ "description" ] = op.Description }
	switch op.Access {
	case Public:
		operation[ "security" ] = []any{}
	case SessionOnly:
		operation[ "security" ] = []any{ map[string]any{ "sessionCookie": []any{} } }
	case KeyOnly:
		operation[ "security" ] = []any{ map[string]any{ "apiKey": []any{} } }
	default:
		operation[ "security" ] = []any{ map[string]any{ "sessionCookie": []any{} } , map[string]any{ "apiKey": []any{} } }
	}
	if op.Role != "" { operation[ "x-required-role" ] = op.Role }
	if op.MCPHTTP != "" { operation[ "x-mcp-http" ] = op.MCPHTTP }
	if op.BulkOf != "" { operation[ "x-bulk-of" ] = op.BulkOf }
	if typed.bulkOf == "" {
		parameters , parameter_problems := builder.parameters( path , typed )
		problems = append( problems , parameter_problems... )
		if len( parameters ) > 0 { operation[ "parameters" ] = parameters }
	}
	switch {
	case typed.bulkOf != "":
		operation[ "requestBody" ] = map[string]any{ "required": true , "content": jsonContent( builder.bulkBodySchema( typed.input , op ) ) }
	case op.BodyType != "":
		operation[ "requestBody" ] = map[string]any{ "required": true , "content": map[string]any{ op.BodyType: map[string]any{ "schema": rawSchema( op.BodyType ) } } }
	case op.RawBody != nil:
		operation[ "requestBody" ] = map[string]any{ "required": true , "content": jsonContent( builder.typed( reflect.TypeOf( op.RawBody ) ) ) }
	case method != "GET":
		if body , has_body := builder.bodySchema( typed.input , op ); has_body {
			operation[ "requestBody" ] = map[string]any{ "required": true , "content": jsonContent( body ) }
		}
	}
	status := op.Status
	if status == 0 { status = 200 }
	description := op.ReplyDescription
	if description == "" { description = "Success" }
	reply := map[string]any{ "description": description }
	switch {
	case op.ReplyType != "":
		reply[ "content" ] = map[string]any{ op.ReplyType: map[string]any{ "schema": rawSchema( op.ReplyType ) } }
	case typed.reply != nil && typed.reply != reflect.TypeOf( None{} ):
		reply[ "content" ] = jsonContent( builder.typed( typed.reply ) )
	}
	responses := map[string]any{ strconv.Itoa( status ): reply }
	statuses := append( []int{ 429 , 503 } , op.Errors... )
	for _ , code := range statuses {
		if _ , known := errorMeanings[ code ]; known == false {
			problems = append( problems , fmt.Sprintf( "error status %d has no shared response" , code ) )
			continue
		}
		responses[ strconv.Itoa( code ) ] = ref( "E" + strconv.Itoa( code ) , "responses" )
	}
	operation[ "responses" ] = responses
	return
}

// csrfHeader is where a browser session's csrf token rides on a route whose
// body is raw bytes.
var csrfHeader = map[string]any{ "name": "X-CSRF-Token" , "in": "header" , "description": "From GET /api/me. Browser sessions only." , "schema": map[string]any{ "type": "string" } }

// parameters are the route's path parameters, in the path's order, then its
// query parameters in the request type's, then the csrf header of a raw body
// a browser session may send.
func ( builder *schemas ) parameters( path string , typed *declared ) ( result []any , problems []string ) {
	declared , order := builder.urlParameters( typed.input )
	for _ , match := range specParameter.FindAllStringSubmatch( path , -1 ) {
		key := "path " + match[ 1 ]
		if parameter , found := declared[ key ]; found {
			result = append( result , parameter )
			delete( declared , key )
			continue
		}
		result = append( result , map[string]any{ "name": match[ 1 ] , "in": "path" , "required": true , "schema": map[string]any{ "type": "string" } } )
	}
	for _ , key := range order {
		if parameter , found := declared[ key ]; found {
			if strings.HasPrefix( key , "path " ) {
				problems = append( problems , "path parameter " + key[ 5 : ] + " is not in the path" )
				continue
			}
			result = append( result , parameter )
		}
	}
	if typed.op.BodyType != "" && typed.op.Access != Public && typed.op.NoCSRF == false { result = append( result , csrfHeader ) }
	return
}

func jsonContent( schema map[string]any ) ( content map[string]any ) {
	content = map[string]any{ "application/json": map[string]any{ "schema": schema } }
	return
}

// rawSchema is what a body the handler reads or writes itself is: bytes,
// text, or a JSON object the docs leave undescribed.
func rawSchema( content_type string ) ( schema map[string]any ) {
	if content_type == "application/json" {
		schema = map[string]any{ "type": "object" }
		return
	}
	schema = map[string]any{ "type": "string" }
	if content_type == "application/octet-stream" { schema[ "format" ] = "binary" }
	return
}

func ref( name string , section ...string ) ( result map[string]any ) {
	where := "schemas"
	if len( section ) > 0 { where = section[ 0 ] }
	result = map[string]any{ "$ref": "#/components/" + where + "/" + name }
	return
}
