package apidoc

import (
	bytes "bytes"
	json "encoding/json"
	reflect "reflect"
	sort "sort"
	strconv "strconv"

	fiber "github.com/gofiber/fiber/v3"
)

// Bulk routes. bulk=True in a route's @route(...) gives it a second route,
// POST /api/bulk/... (the path less its {params}), that runs the route's own
// handler once per item, so one request changes many things:
//
//	{ "items": [ { "user_id": 3 } , { "user_id": 7 , "disabled": false } ] ,
//	  "shared": { "disabled": true } }
//
// An item is the route's request as an MCP tool takes it, URL fields and
// all. shared holds body fields every item gets unless it sets its own. Each
// item is checked and run as the route would check and run it, in order, one
// at a time, so the rules, the audit log and the webhooks are the route's,
// item by item. One failing does not stop the rest, and nothing is undone:
// the reply (BulkReply) says how each went. The MCP tool <tool>_bulk is the
// same thing through /mcp.

// BulkMaxItems is how many items one bulk request may hold.
const BulkMaxItems = 100

// LogFailure logs an item that failed on our side and answers the reference
// that finds it in the log, as the ErrorHandler does for a whole request.
// The routes package sets it.
var LogFailure func( c fiber.Ctx , cause error ) ( request_id string )

// BulkReply is what a bulk route answers once its request is sound.
type BulkReply struct {
	Done    int          `json:"done" jsonschema_description:"How many items worked."`
	Failed  int          `json:"failed" jsonschema_description:"How many did not."`
	Results []BulkResult `json:"results" jsonschema_description:"One per item, in the order they were sent."`
}

func ( BulkReply ) APIName() ( name string ) { name = "BulkReply"; return }

// BulkResult is how one item went.
type BulkResult struct {
	Index     int            `json:"index" jsonschema_description:"The item's place in items, from 0."`
	Ok        bool           `json:"ok"`
	Status    int            `json:"status,omitempty" jsonschema_description:"When it failed: the status the route would have answered."`
	Error     string         `json:"error,omitempty" jsonschema_description:"When it failed: the route's words for why."`
	RequestID string         `json:"request_id,omitempty" jsonschema_description:"On a 5xx: the reference that finds this failure in the log."`
	Reply     map[string]any `json:"reply,omitempty" jsonschema_description:"When it worked: the route's own reply, if it has one."`
}

func ( BulkResult ) APIName() ( name string ) { name = "BulkResult"; return }

// The words the bulk shapes are documented with, the same in both templates.
const (
	bulkItemsDoc  = "One entry per thing to change: its id, and any field that differs from shared."
	bulkSharedDoc = "Fields every item gets, unless the item sets its own."
)

// bulkEndpoint is a mounted bulk route.
type bulkEndpoint[ In any , Out any ] struct {
	op    Op
	serve func( c fiber.Ctx , in *In ) ( *Out , error )
}

// MountBulk puts the bulk version of the route at single on the app, at
// path, after the guards given. The route table calls it for every route
// whose @route(...) has bulk=True.
func MountBulk[ In any , Out any ]( app fiber.Router , path string , single string , op Op , serve func( c fiber.Ctx , in *In ) ( *Out , error ) , guards ...any ) {
	key := RouteKey( fiber.MethodPost , path )
	found := declareBulk[ In ]( key , single , &op )
	declaredMutex.Lock()
	found.method , found.path , found.mounted = fiber.MethodPost , SpecPath( path ) , true
	declaredMutex.Unlock()
	route := &bulkEndpoint[ In , Out ]{ op: op , serve: serve }
	handler := func( c fiber.Ctx ) ( err error ) {
		err = route.handle( c , found )
		return
	}
	handlers := append( append( []any{} , guards... ) , handler )
	app.Add( []string{ fiber.MethodPost } , path , handlers[ 0 ] , handlers[ 1 : ]... ).Name( routeName( key ) )
}

// DeclareBulk records a bulk route's request type, for its MCP tool, which
// may be registered before the route is mounted.
func DeclareBulk[ In any ]( key string , single string ) {
	declareBulk[ In ]( key , single , nil )
}

func declareBulk[ In any ]( key string , single string , op *Op ) ( found *declared ) {
	declaredMutex.Lock()
	defer declaredMutex.Unlock()
	found = declaredRoutes[ key ]
	if found == nil {
		found = &declared{
			name:   key,
			input:  inputOf( reflect.TypeOf( ( *In )( nil ) ).Elem() ),
			reply:  reflect.TypeOf( BulkReply{} ),
			bulkOf: single,
		}
		declaredRoutes[ key ] = found
	}
	if op != nil { found.op = *op }
	return
}

func ( route *bulkEndpoint[ In , Out ] ) handle( c fiber.Ctx , found *declared ) ( err error ) {
	values := map[string]any{}
	if body := bytes.TrimSpace( c.Body() ); len( body ) > 0 {
		decoder := json.NewDecoder( bytes.NewReader( body ) )
		decoder.UseNumber()
		if decoder.Decode( &values ) != nil || values == nil {
			err = answer( c , fiber.StatusBadRequest , "malformed request body" )
			return
		}
	}
	token , _ := values[ "csrf_token" ].( string )
	delete( values , "csrf_token" )
	if route.op.Access != Public && route.op.NoCSRF == false && CheckCSRF != nil && CheckCSRF( c , token ) == false {
		err = answer( c , fiber.StatusForbidden , "invalid csrf token" )
		return
	}
	items , shared , message , _ := found.input.bulkInput( values )
	if message != "" {
		err = answer( c , fiber.StatusBadRequest , message )
		return
	}
	err = c.Status( fiber.StatusOK ).JSON( runBulk( c , found.input , items , shared , route.serve ) )
	return
}

// CallBulk is a bulk route for its MCP tool: the arguments checked as the
// route checks a body, then every item run. malformed is a fault in the
// arguments' shape rather than a refusal.
func CallBulk[ In any , Out any ]( c fiber.Ctx , key string , raw []byte , serve func( c fiber.Ctx , in *In ) ( *Out , error ) ) ( reply *BulkReply , message string , malformed bool ) {
	found := lookup( key )
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
	items , shared , message , malformed := found.input.bulkInput( values )
	if message != "" { return }
	reply = runBulk( c , found.input , items , shared , serve )
	return
}

// bulkInput checks a bulk body's own shape: items, 1 to BulkMaxItems
// objects, and shared, an object of the route's body fields.
func ( input *inputType ) bulkInput( values map[string]any ) ( items []map[string]any , shared map[string]any , message string , malformed bool ) {
	unknown := []string{}
	for name := range values {
		if name != "items" && name != "shared" { unknown = append( unknown , name ) }
	}
	if len( unknown ) > 0 {
		sort.Strings( unknown )
		message , malformed = "unknown field: " + unknown[ 0 ] , true
		return
	}
	raw_items , present := values[ "items" ]
	if present == false {
		message = "items is required"
		return
	}
	listed , is_list := raw_items.( []any )
	if is_list == false {
		message , malformed = "malformed request body" , true
		return
	}
	for _ , raw := range listed {
		item , is_object := raw.( map[string]any )
		if is_object == false || item == nil {
			message , malformed = "malformed request body" , true
			return
		}
		items = append( items , item )
	}
	if len( items ) < 1 || len( items ) > BulkMaxItems {
		message = "items must hold 1 to " + strconv.Itoa( BulkMaxItems ) + " entries"
		return
	}
	shared = map[string]any{}
	if raw_shared , present := values[ "shared" ]; present {
		object , is_object := raw_shared.( map[string]any )
		if is_object == false || object == nil {
			message , malformed = "malformed request body" , true
			return
		}
		unknown := []string{}
		for name := range object {
			if field := input.byName[ name ]; field == nil || field.place != "" { unknown = append( unknown , name ) }
		}
		if len( unknown ) > 0 {
			sort.Strings( unknown )
			message , malformed = "unknown field: shared." + unknown[ 0 ] , true
			return
		}
		shared = object
	}
	return
}

// runBulk runs every item through the route's handler, in order: shared
// under the item, checked as an MCP tool's arguments are, then served.
func runBulk[ In any , Out any ]( c fiber.Ctx , input *inputType , items []map[string]any , shared map[string]any , serve func( c fiber.Ctx , in *In ) ( *Out , error ) ) ( reply *BulkReply ) {
	reply = &BulkReply{ Results: []BulkResult{} }
	_ , no_reply := any( new( Out ) ).( *None )
	for index , item := range items {
		values := map[string]any{}
		for name , value := range shared { values[ name ] = value }
		for name , value := range item { values[ name ] = value }
		result := BulkResult{ Index: index }
		in := new( In )
		if message , _ := input.decode( values , in , true ); message != "" {
			result.Status , result.Error = fiber.StatusBadRequest , message
		} else if out , err := serve( c , in ); err != nil {
			sender , is_sender := err.( Sender )
			status , message := 0 , ""
			if is_sender { status , message = sender.Refused() }
			if is_sender && status < 500 {
				result.Status , result.Error = status , message
			} else {
				result.Status , result.Error = fiber.StatusInternalServerError , "internal error"
				if LogFailure != nil { result.RequestID = LogFailure( c , err ) }
			}
		} else {
			result.Ok = true
			if out != nil && no_reply == false {
				if encoded , encode_err := json.Marshal( out ); encode_err == nil {
					json.Unmarshal( encoded , &result.Reply )
				}
			}
		}
		if result.Ok {
			reply.Done++
		} else {
			reply.Failed++
		}
		reply.Results = append( reply.Results , result )
	}
	return
}

// bulkSchema is a bulk route's body: items, each the route's arguments with
// only its path fields required, and shared, its body fields. property is a
// field's schema as the caller writes it (a $ref for the docs, written out
// for MCP).
func ( input *inputType ) bulkSchema( property func( field *inputField ) map[string]any , csrf bool ) ( schema map[string]any ) {
	item_properties := map[string]any{}
	shared_properties := map[string]any{}
	required := []any{}
	for _ , field := range input.fields {
		item_properties[ field.name ] = property( field )
		if field.place == "path" { required = append( required , field.name ) }
		if field.place == "" { shared_properties[ field.name ] = property( field ) }
	}
	item := map[string]any{ "type": "object" , "properties": item_properties , "additionalProperties": false }
	if len( required ) > 0 {
		sort.Slice( required , func( a , b int ) bool { return required[ a ].( string ) < required[ b ].( string ) } )
		item[ "required" ] = required
	}
	properties := map[string]any{
		"items":  map[string]any{ "type": "array" , "items": item , "minItems": 1 , "maxItems": BulkMaxItems , "description": bulkItemsDoc },
		"shared": map[string]any{ "type": "object" , "properties": shared_properties , "additionalProperties": false , "description": bulkSharedDoc },
	}
	if csrf { properties[ "csrf_token" ] = csrfProperty }
	schema = map[string]any{ "type": "object" , "properties": properties , "required": []any{ "items" } , "additionalProperties": false }
	return
}

// bulkBodySchema is a bulk route's body as config/api.json writes it.
func ( builder *schemas ) bulkBodySchema( input *inputType , op Op ) ( schema map[string]any ) {
	source := object( builder.reflected( input.kind )[ "properties" ] )
	schema = input.bulkSchema( func( field *inputField ) map[string]any { return object( source[ field.name ] ) } , op.Access != Public && op.NoCSRF == false )
	return
}

// bulkArgumentsSchema is a bulk tool's input schema: the body, standing alone.
func ( input *inputType ) bulkArgumentsSchema() ( schema map[string]any ) {
	builder := newSchemas()
	source := object( builder.reflected( input.kind )[ "properties" ] )
	schema = input.bulkSchema( func( field *inputField ) map[string]any {
		property := resolveAll( builder , object( source[ field.name ] ) )
		if doc := field.schemaDescription(); doc != "" { property[ "description" ] = doc }
		return property
	} , false )
	return
}
