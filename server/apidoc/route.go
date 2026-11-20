package apidoc

import (
	bytes "bytes"
	json "encoding/json"
	fmt "fmt"
	math "math"
	reflect "reflect"
	regexp "regexp"
	sort "sort"
	strconv "strconv"
	strings "strings"
	sync "sync"

	fiber "github.com/gofiber/fiber/v3"
)

// A route is declared once, as a method on *Handlers whose doc comment ends
// with its // @route(...) (see server/apidoc/routegen), and the route table
// that apidoc/gen writes mounts it with Mount or MountStream:
//
//	// @route("POST /api/admin/users", summary="Create an account", tag="Admin", role=ADMIN, status=201)
//	func ( handlers *Handlers ) createUser( c fiber.Ctx , in *createUserRequest ) ( reply *createUserReply , err error ) { ... }
//
// The request type is the whole of what the route takes. A field tagged
// path:"name" or query:"name" comes from the URL, every other field from the
// JSON body, and its json tag names it the same way in both and to MCP. The
// reply type is what it answers. From the two come the route's entry in
// config/api.json, the input schema of the MCP tool that stands for it,
// and the checks every request goes through before the handler sees it:
//
//   - The body must be a JSON object ("malformed request body" otherwise),
//     and on a signed-in route a browser session's POST must carry its
//     csrf_token (403 "invalid csrf token"). An API key needs none.
//   - A field nobody declared is refused: 400 "unknown field: x".
//   - A body value of the wrong JSON type is "malformed request body".
//   - A field that is missing, or breaks a jsonschema limit, answers its own
//     error tag (or "x is required" / "x is invalid" without one). A path or
//     query value that is not a number where one is wanted counts as that.
//
// The handler gets the request already checked, and returns its reply, or an
// error: a refusal (anything with a Send method) is sent as it is, anything
// else is a failure on our side.

// None is the request of a route that takes nothing, and the reply of one
// that answers with nothing but its status.
type None struct{}

// Sender is an error that knows how to answer: the routes package's refusal.
// Refused is its status and message, for an MCP tool to say the same no.
type Sender interface {
	Send( c fiber.Ctx ) error
	Refused() ( status int , message string )
}

// CheckCSRF is security.Guard.CheckCSRF, set when the routes are registered.
var CheckCSRF func( c fiber.Ctx , token string ) bool

// endpoint is a declared route: its docs and its handler.
type endpoint[ In any , Out any ] struct {
	op     Op
	serve  func( c fiber.Ctx , in *In ) ( *Out , error )
	stream func( c fiber.Ctx , in *In ) error
}

// Mount puts a declared route on the app at method and path (as the spec
// writes it: /users/{user_id}), after the guards given. The route table that
// apidoc/gen writes from the @route(...) declarations calls it for every route;
// nothing else needs to.
func Mount[ In any , Out any ]( app fiber.Router , method string , path string , op Op , serve func( c fiber.Ctx , in *In ) ( *Out , error ) , guards ...any ) {
	mountRoute( app , method , path , &endpoint[ In , Out ]{ op: op , serve: serve } , guards )
}

// MountStream is Mount for a route that writes its own reply: bytes, a
// download, an event stream. op.ReplyType says what it sends; without one,
// the route answers with its status alone.
func MountStream[ In any ]( app fiber.Router , method string , path string , op Op , serve func( c fiber.Ctx , in *In ) error , guards ...any ) {
	mountRoute( app , method , path , &endpoint[ In , None ]{ op: op , stream: serve } , guards )
}

// RouteKey is how a declared route is known: "POST /api/admin/users".
func RouteKey( method string , path string ) ( key string ) {
	key = method + " " + SpecPath( path )
	return
}

func mountRoute[ In any , Out any ]( app fiber.Router , method string , path string , route *endpoint[ In , Out ] , guards []any ) {
	key := RouteKey( method , path )
	found := declare( key , route )
	declaredMutex.Lock()
	found.method , found.path , found.mounted = method , SpecPath( path ) , true
	declaredMutex.Unlock()
	handler := func( c fiber.Ctx ) ( err error ) {
		err = route.handle( c , found )
		return
	}
	handlers := append( append( []any{} , guards... ) , handler )
	fiber_path := specParameter.ReplaceAllString( path , ":$1" )
	app.Add( []string{ method } , fiber_path , handlers[ 0 ] , handlers[ 1 : ]... ).Name( routeName( key ) )
}

// DeclareRoute records the request type of the route at key, for an MCP
// tool registered before the route is mounted.
func DeclareRoute[ In any , Out any ]( key string ) {
	declare( key , &endpoint[ In , Out ]{} )
}

// declared is a mounted route as the docs and MCP see it.
type declared struct {
	name     string
	op       Op
	method   string
	path     string
	input    *inputType
	reply    reflect.Type
	mounted  bool
	// bulkOf is the route a bulk route runs once per item (see bulk.go).
	bulkOf   string
}

var declaredMutex sync.Mutex
var declaredRoutes = map[string]*declared{}

// routeName marks a route in the app's route table as declared here, so
// Build finds its docs; the path comes from the route table.
func routeName( name string ) ( result string ) {
	result = "apidoc:" + name
	return
}

// declare records a route's types under its key, once.
func declare[ In any , Out any ]( name string , route *endpoint[ In , Out ] ) ( found *declared ) {
	declaredMutex.Lock()
	defer declaredMutex.Unlock()
	found = declaredRoutes[ name ]
	if found != nil {
		if route.serve != nil || route.stream != nil {
			found.op = route.op
			found.reply = nil
			if route.stream == nil { found.reply = reflect.TypeOf( ( *Out )( nil ) ).Elem() }
		}
		return
	}
	found = &declared{
		name:  name,
		op:    route.op,
		input: inputOf( reflect.TypeOf( ( *In )( nil ) ).Elem() ),
	}
	if route.stream == nil { found.reply = reflect.TypeOf( ( *Out )( nil ) ).Elem() }
	declaredRoutes[ name ] = found
	return
}

func ( route *endpoint[ In , Out ] ) handle( c fiber.Ctx , found *declared ) ( err error ) {
	values := map[string]any{}
	// HEAD is GET without the body, which fiber answers on every GET route.
	reads := c.Method() == fiber.MethodGet || c.Method() == fiber.MethodHead
	raw_body := route.op.BodyType == "" && route.op.RawBody == nil && reads == false
	token := ""
	if raw_body {
		body := bytes.TrimSpace( c.Body() )
		if len( body ) > 0 {
			decoder := json.NewDecoder( bytes.NewReader( body ) )
			decoder.UseNumber()
			if decoder.Decode( &values ) != nil || values == nil {
				err = answer( c , fiber.StatusBadRequest , "malformed request body" )
				return
			}
		}
		token , _ = values[ "csrf_token" ].( string )
		delete( values , "csrf_token" )
	}
	if route.op.Access != Public && reads == false && route.op.NoCSRF == false && route.op.RawBody == nil && CheckCSRF != nil {
		if route.op.BodyType != "" { token = c.Get( "X-CSRF-Token" ) }
		if CheckCSRF( c , token ) == false {
			err = answer( c , fiber.StatusForbidden , "invalid csrf token" )
			return
		}
	}
	for _ , field := range found.input.fields {
		var text string
		var present bool
		switch field.place {
		case "path":
			text = c.Params( field.wire )
			present = true
		case "query":
			raw := c.Request().URI().QueryArgs().Peek( field.wire )
			text , present = string( raw ) , raw != nil
		default:
			continue
		}
		if present == false { continue }
		value , ok := field.fromText( text )
		if ok == false {
			err = answer( c , fiber.StatusBadRequest , field.message( "invalid" ) )
			return
		}
		values[ field.name ] = value
	}
	in := new( In )
	if message , _ := found.input.decode( values , in , false ); message != "" {
		err = answer( c , fiber.StatusBadRequest , message )
		return
	}
	if route.stream != nil {
		err = route.stream( c , in )
		if sender , ok := err.( Sender ); ok { err = sender.Send( c ) }
		return
	}
	reply , serve_err := route.serve( c , in )
	if serve_err != nil {
		if sender , ok := serve_err.( Sender ); ok {
			err = sender.Send( c )
			return
		}
		err = serve_err
		return
	}
	status := route.op.Status
	if status == 0 { status = fiber.StatusOK }
	if reply == nil {
		err = c.SendStatus( status )
		return
	}
	err = c.Status( status ).JSON( reply )
	return
}

func answer( c fiber.Ctx , status int , message string ) ( err error ) {
	err = c.Status( status ).JSON( fiber.Map{ "error": message } )
	return
}

// inputType is what a request type declares, field by field.
type inputType struct {
	kind   reflect.Type
	fields []*inputField
	byName map[string]*inputField
}

type inputField struct {
	name     string // the JSON name, the MCP argument's name
	wire     string // the URL's name for a path or query field
	place    string // "path", "query" or "" for the body
	kind     reflect.Type
	required bool
	errorTag string
	schema   map[string]any
	fallback any // the jsonschema default, filled in when it is left out
}

var inputCache sync.Map

func inputOf( kind reflect.Type ) ( input *inputType ) {
	if cached , found := inputCache.Load( kind ); found {
		input = cached.( *inputType )
		return
	}
	input = &inputType{ kind: kind , byName: map[string]*inputField{} }
	builder := newSchemas()
	schema := builder.reflected( kind )
	properties , _ := schema[ "properties" ].( map[string]any )
	required := map[string]bool{}
	for _ , name := range list( schema[ "required" ] ) { required[ str( name ) ] = true }
	var walk func( kind reflect.Type )
	walk = func( kind reflect.Type ) {
		for index := 0 ; index < kind.NumField() ; index++ {
			field := kind.Field( index )
			if field.Anonymous && field.Tag.Get( "json" ) == "" {
				walk( field.Type )
				continue
			}
			name , _ := jsonName( field )
			if name == "" { continue }
			if _ , taken := input.byName[ name ]; taken { continue }
			entry := &inputField{ name: name , wire: name , kind: field.Type , errorTag: field.Tag.Get( "error" ) }
			for _ , place := range []string{ "path" , "query" } {
				if wire := field.Tag.Get( place ); wire != "" { entry.place , entry.wire = place , wire }
			}
			entry.schema = resolveAll( builder , object( properties[ name ] ) )
			entry.required = required[ name ] && entry.place != "query"
			if fallback , found := entry.schema[ "default" ]; found { entry.fallback = fallback }
			input.fields = append( input.fields , entry )
			input.byName[ name ] = entry
		}
	}
	if kind.Kind() == reflect.Struct { walk( kind ) }
	inputCache.Store( kind , input )
	return
}

// resolveAll is a schema with every $ref into the builder's components
// written out, however deep (a list of named types included), so a field can
// be checked on its own and an MCP tool's input schema stands alone. A key
// beside a $ref, such as the field's own description, wins over the named
// schema's. A type that holds itself stays a $ref past its first level.
func resolveAll( builder *schemas , schema map[string]any ) ( result map[string]any ) {
	spec := map[string]any{ "components": map[string]any{ "schemas": builder.components } }
	result = resolveDeep( spec , schema , map[string]bool{} )
	return
}

func resolveDeep( spec map[string]any , schema map[string]any , open map[string]bool ) ( result map[string]any ) {
	if target := str( schema[ "$ref" ] ); target != "" {
		name := target[ strings.LastIndex( target , "/" ) + 1 : ]
		if open[ name ] {
			result = schema
			return
		}
		merged := map[string]any{}
		for key , value := range resolve( spec , map[string]any{ "$ref": target } ) { merged[ key ] = value }
		for key , value := range schema {
			if key != "$ref" { merged[ key ] = value }
		}
		open[ name ] = true
		result = resolveDeep( spec , merged , open )
		delete( open , name )
		return
	}
	result = map[string]any{}
	for key , value := range schema {
		switch typed := value.( type ) {
		case map[string]any:
			result[ key ] = resolveDeep( spec , typed , open )
		case []any:
			items := make( []any , len( typed ) )
			for index , item := range typed {
				if inner , is_map := item.( map[string]any ); is_map {
					items[ index ] = resolveDeep( spec , inner , open )
				} else {
					items[ index ] = item
				}
			}
			result[ key ] = items
		default:
			result[ key ] = value
		}
	}
	return
}

func ( field *inputField ) message( fault string ) ( text string ) {
	text = field.errorTag
	if text == "" { text = field.name + " is " + fault }
	return
}

// refusal is the 400 for a value that breaks the field's limits: its error
// tag, or for an enum without one, the values it may take.
func ( field *inputField ) refusal() ( text string ) {
	if values := list( field.schema[ "enum" ] ); field.errorTag == "" && len( values ) > 0 {
		names := []string{}
		for _ , value := range values { names = append( names , fmt.Sprint( value ) ) }
		text = field.name + " must be one of: " + strings.Join( names , ", " )
		return
	}
	text = field.message( "invalid" )
	return
}

// fromText is a path or query value as the field's type.
func ( field *inputField ) fromText( text string ) ( value any , ok bool ) {
	kind := field.kind
	for kind.Kind() == reflect.Pointer { kind = kind.Elem() }
	switch kind.Kind() {
	case reflect.Int , reflect.Int8 , reflect.Int16 , reflect.Int32 , reflect.Int64:
		number , err := strconv.ParseInt( text , 10 , 64 )
		value , ok = json.Number( strconv.FormatInt( number , 10 ) ) , err == nil
	case reflect.Uint , reflect.Uint8 , reflect.Uint16 , reflect.Uint32 , reflect.Uint64:
		number , err := strconv.ParseUint( text , 10 , 64 )
		value , ok = json.Number( strconv.FormatUint( number , 10 ) ) , err == nil
	case reflect.Bool:
		truth , err := strconv.ParseBool( text )
		value , ok = truth , err == nil
	default:
		value , ok = text , true
	}
	return
}

// decode checks values against the request type and fills in. It answers
// the 400 message, or "" when the request is fine. strict_places is MCP's
// way: every field, path and query ones included, comes from values.
func ( input *inputType ) decode( values map[string]any , out any , strict_places bool ) ( message string , malformed bool ) {
	unknown := []string{}
	for name := range values {
		if input.byName[ name ] == nil { unknown = append( unknown , name ) }
	}
	if len( unknown ) > 0 {
		sort.Strings( unknown )
		message , malformed = "unknown field: " + unknown[ 0 ] , true
		return
	}
	for _ , field := range input.fields {
		value , present := values[ field.name ]
		if present == false && field.fallback != nil {
			values[ field.name ] = field.fallback
			value , present = field.fallback , true
		}
		if present == false {
			if field.required {
				message = field.message( "required" )
				return
			}
			continue
		}
		switch inspect( field.schema , value ) {
		case wrongType:
			if field.place != "" && strict_places == false {
				message = field.message( "invalid" )
			} else {
				message , malformed = "malformed request body" , true
			}
			return
		case outOfBounds:
			message = field.refusal()
			return
		}
	}
	// A number stays a json.Number where the type takes any value, so an
	// int is still an int (a float64 would let 1.5 through as 1).
	encoded , err := json.Marshal( values )
	if err == nil {
		decoder := json.NewDecoder( bytes.NewReader( encoded ) )
		decoder.UseNumber()
		err = decoder.Decode( out )
	}
	if err != nil { message , malformed = "malformed request body" , true }
	return
}

type verdict int

const (
	fits verdict = iota
	wrongType
	outOfBounds
)

var patternCache sync.Map

// inspect holds a JSON value against a schema the reflector wrote: wrong
// JSON type first, then the limits.
func inspect( schema map[string]any , value any ) ( result verdict ) {
	if variants := list( schema[ "oneOf" ] ); len( variants ) > 0 {
		// A value of a variant's type that breaks its limits is the closer
		// miss: a list of themes with one bad mode is out of bounds, not a
		// wrong type because it is not null either.
		best := wrongType
		for _ , variant := range variants {
			found := inspect( object( variant ) , value )
			if found == fits { return fits }
			if found == outOfBounds { best = outOfBounds }
		}
		result = best
		return
	}
	if typed := types( schema ); len( typed ) > 0 {
		kind := jsonKind( value )
		if typed[ kind ] == false && ( kind != "integer" || typed[ "number" ] == false ) {
			result = wrongType
			return
		}
	}
	if values := list( schema[ "enum" ] ); len( values ) > 0 {
		matched := false
		for _ , allowed := range values {
			if sameValue( allowed , value ) { matched = true }
		}
		if matched == false {
			result = outOfBounds
			return
		}
	}
	switch typed := value.( type ) {
	case string:
		length := len( []rune( typed ) )
		if limit , ok := limitOf( schema , "minLength" ); ok && float64( length ) < limit { return outOfBounds }
		if limit , ok := limitOf( schema , "maxLength" ); ok && float64( length ) > limit { return outOfBounds }
		if pattern := str( schema[ "pattern" ] ); pattern != "" {
			compiled , found := patternCache.Load( pattern )
			if found == false {
				compiled = regexp.MustCompile( pattern )
				patternCache.Store( pattern , compiled )
			}
			if compiled.( *regexp.Regexp ).MatchString( typed ) == false { return outOfBounds }
		}
	case json.Number , float64:
		number := toFloat( typed )
		if limit , ok := limitOf( schema , "minimum" ); ok && number < limit { return outOfBounds }
		if limit , ok := limitOf( schema , "maximum" ); ok && number > limit { return outOfBounds }
	case []any:
		if limit , ok := limitOf( schema , "minItems" ); ok && float64( len( typed ) ) < limit { return outOfBounds }
		if limit , ok := limitOf( schema , "maxItems" ); ok && float64( len( typed ) ) > limit { return outOfBounds }
		if items := object( schema[ "items" ] ); items != nil {
			for _ , item := range typed {
				if found := inspect( items , item ); found != fits { return found }
			}
		}
	case map[string]any:
		properties := object( schema[ "properties" ] )
		if properties != nil {
			for _ , name := range list( schema[ "required" ] ) {
				if _ , found := typed[ str( name ) ]; found == false { return outOfBounds }
			}
			for name , item := range typed {
				property , found := properties[ name ]
				if found == false {
					if schema[ "additionalProperties" ] == false { return wrongType }
					continue
				}
				if result = inspect( object( property ) , item ); result != fits { return }
			}
		} else if extra := object( schema[ "additionalProperties" ] ); extra != nil {
			for _ , item := range typed {
				if result = inspect( extra , item ); result != fits { return }
			}
		}
	}
	return
}

func jsonKind( value any ) ( kind string ) {
	switch typed := value.( type ) {
	case json.Number:
		kind = "number"
		if _ , err := typed.Int64(); err == nil { kind = "integer" }
	default:
		kind = kindOf( value )
	}
	return
}

func toFloat( value any ) ( number float64 ) {
	switch typed := value.( type ) {
	case json.Number:
		number , _ = typed.Float64()
	case float64:
		number = typed
	}
	return
}

func limitOf( schema map[string]any , key string ) ( limit float64 , ok bool ) {
	switch typed := schema[ key ].( type ) {
	case float64:
		limit , ok = typed , true
	case int:
		limit , ok = float64( typed ) , true
	case int64:
		limit , ok = float64( typed ) , true
	case json.Number:
		parsed , err := typed.Float64()
		limit , ok = parsed , err == nil
	}
	return
}

func sameValue( allowed any , value any ) ( same bool ) {
	switch typed := value.( type ) {
	case json.Number , float64:
		number := toFloat( typed )
		switch target := allowed.( type ) {
		case float64:
			same = target == number
		case int:
			same = float64( target ) == number
		case int64:
			same = float64( target ) == number
		}
		same = same && math.IsNaN( number ) == false
	default:
		same = allowed == value
	}
	return
}

// The docs and MCP side of a declared route.

// InputSchema is the JSON Schema of an MCP tool's arguments: the route's
// whole request, URL fields included, as one object. csrf_token is not part
// of it: a key needs none.
func InputSchema( name string ) ( schema map[string]any , err error ) {
	found := lookup( name )
	if found == nil {
		err = fmt.Errorf( "apidoc: %s is not a declared route" , name )
		return
	}
	if found.bulkOf != "" {
		schema = found.input.bulkArgumentsSchema()
		return
	}
	schema = found.input.argumentsSchema()
	return
}

// DecodeArguments is decode for an MCP tool: the arguments object, checked
// the way the route checks a request. malformed is a fault in the arguments
// themselves (not an object, a wrong type, an unknown name) rather than a
// field's own 400.
func DecodeArguments( name string , raw []byte , out any ) ( message string , malformed bool ) {
	found := lookup( name )
	if found == nil {
		message , malformed = "no such route" , true
		return
	}
	values := map[string]any{}
	trimmed := bytes.TrimSpace( raw )
	if len( trimmed ) > 0 && bytes.Equal( trimmed , []byte( "null" ) ) == false {
		decoder := json.NewDecoder( bytes.NewReader( trimmed ) )
		decoder.UseNumber()
		if decoder.Decode( &values ) != nil || values == nil {
			message , malformed = "malformed request body" , true
			return
		}
	}
	message , malformed = found.input.decode( values , out , true )
	return
}

// RouteOf is the route a declared handler is mounted at, as the spec writes
// it: "POST /api/admin/users". Empty until it is mounted.
func RouteOf( name string ) ( route string ) {
	found := lookup( name )
	if found == nil { return }
	if found.path == "" {
		if strings.Contains( name , " " ) { route = name }
		return
	}
	route = found.method + " " + found.path
	return
}

func lookup( name string ) ( found *declared ) {
	declaredMutex.Lock()
	found = declaredRoutes[ name ]
	declaredMutex.Unlock()
	return
}

func ( input *inputType ) argumentsSchema() ( schema map[string]any ) {
	builder := newSchemas()
	reflected := builder.reflected( input.kind )
	properties := map[string]any{}
	required := []any{}
	source := object( reflected[ "properties" ] )
	for _ , field := range input.fields {
		property := resolveAll( builder , object( source[ field.name ] ) )
		if doc := field.schemaDescription(); doc != "" { property[ "description" ] = doc }
		properties[ field.name ] = property
		if field.required { required = append( required , field.name ) }
	}
	schema = map[string]any{ "type": "object" , "properties": properties , "additionalProperties": false }
	if len( required ) > 0 {
		sort.Slice( required , func( a , b int ) bool { return required[ a ].( string ) < required[ b ].( string ) } )
		schema[ "required" ] = required
	}
	return
}

func ( field *inputField ) schemaDescription() ( text string ) {
	text = str( field.schema[ "description" ] )
	return
}

// csrfProperty is the csrf_token every signed-in route's body may carry.
var csrfProperty = map[string]any{ "type": "string" , "description": "From GET /api/me. Browser sessions only." }

// bodySchema is the request body: the request type less its URL fields,
// and csrf_token on a route a browser session signs.
func ( builder *schemas ) bodySchema( input *inputType , op Op ) ( schema map[string]any , has_body bool ) {
	reflected := builder.reflected( input.kind )
	source := object( reflected[ "properties" ] )
	properties := map[string]any{}
	required := []any{}
	for _ , field := range input.fields {
		if field.place != "" { continue }
		properties[ field.name ] = source[ field.name ]
		if field.required { required = append( required , field.name ) }
	}
	if op.Access != Public && op.NoCSRF == false { properties[ "csrf_token" ] = csrfProperty }
	has_body = len( properties ) > 0
	schema = map[string]any{ "type": "object" , "properties": properties , "additionalProperties": false }
	if len( required ) > 0 {
		sort.Slice( required , func( a , b int ) bool { return required[ a ].( string ) < required[ b ].( string ) } )
		schema[ "required" ] = required
	}
	return
}

// urlParameters are the request type's path and query fields, as the spec's
// parameters.
func ( builder *schemas ) urlParameters( input *inputType ) ( declared map[string]map[string]any , order []string ) {
	declared = map[string]map[string]any{}
	reflected := builder.reflected( input.kind )
	source := object( reflected[ "properties" ] )
	for _ , field := range input.fields {
		if field.place == "" { continue }
		schema := map[string]any{}
		for key , value := range object( source[ field.name ] ) { schema[ key ] = value }
		description := str( schema[ "description" ] )
		delete( schema , "description" )
		parameter := map[string]any{ "name": field.wire , "in": field.place , "schema": schema }
		if description != "" { parameter[ "description" ] = description }
		if field.place == "path" { parameter[ "required" ] = true }
		key := field.place + " " + field.wire
		declared[ key ] = parameter
		order = append( order , key )
	}
	return
}
