package apidoc

import (
	json "encoding/json"
	reflect "reflect"
	sort "sort"
	strings "strings"

	jsonschema "github.com/invopop/jsonschema"
)

// How a Go type becomes JSON Schema. github.com/invopop/jsonschema does the
// reflecting; this file only puts its answer in the shape the docs use, the
// same shape python-webserver gets from msgspec, so both templates write the
// same bytes.
//
// A struct field's json tag names it, and it is required unless the tag says
// omitempty (or omitzero). A pointer may also be null. The jsonschema tag
// carries the rest, the way invopop reads it:
//
//	jsonschema:"minLength=1,maxLength=80"   string limits
//	jsonschema:"minimum=0,maximum=1000"     number limits
//	jsonschema:"enum=admin,enum=user"       the values it may take
//	jsonschema:"pattern=^[a-z]+$"           a regular expression it matches
//	jsonschema:"format=date-time"           a string format
//	jsonschema:"default=200,example=7"      a value, as the field's type
//	jsonschema:"nullable"                   may be null (a pointer already may)
//	jsonschema_description:"what it is"     its description (commas are fine)
//
// error:"display name must be 1-80 characters" is not schema: it is the 400 a
// request field answers when it is missing or breaks a limit (see route.go).
//
// A type with an APIName method is a named schema in components, referred to
// by $ref; every other struct is written out where it is used. A type with an
// APIOneOf method is one of the types it lists, and one with an APIDoc method
// has that description. An embedded struct's fields are the outer struct's
// own, as encoding/json sends them.

// Named types become components/schemas entries.
type Named interface{ APIName() string }

// Union types are one of several shapes.
type Union interface{ APIOneOf() []any }

// Described types say what they are.
type Described interface{ APIDoc() string }

var namedType = reflect.TypeOf( ( *Named )( nil ) ).Elem()
var unionType = reflect.TypeOf( ( *Union )( nil ) ).Elem()
var describedType = reflect.TypeOf( ( *Described )( nil ) ).Elem()

// declares is whether kind has the method of iface as its own, rather than
// promoted from a field it embeds.
func declares( kind reflect.Type , iface reflect.Type ) ( own bool ) {
	if kind.Implements( iface ) == false { return }
	own = true
	if kind.Kind() != reflect.Struct { return }
	method := iface.Method( 0 ).Name
	mine := reflect.Zero( kind ).MethodByName( method ).Call( nil )[ 0 ].Interface()
	for index := 0 ; index < kind.NumField() ; index++ {
		field := kind.Field( index )
		if field.Anonymous == false || field.Type.Implements( iface ) == false { continue }
		theirs := reflect.Zero( field.Type ).MethodByName( method ).Call( nil )[ 0 ].Interface()
		if reflect.DeepEqual( mine , theirs ) { own = false }
	}
	return
}

// schemas collects the named schemas a document refers to, as it is built.
type schemas struct {
	components map[string]any
	building   map[string]reflect.Type
}

func newSchemas() ( builder *schemas ) {
	builder = &schemas{ components: map[string]any{} , building: map[string]reflect.Type{} }
	return
}

// unionMarker stands in for a union type in invopop's answer; normalize
// swaps it for the oneOf.
const unionMarker = "x-apidoc-union"

// typed is the schema for a type: a $ref for a named one.
func ( builder *schemas ) typed( kind reflect.Type ) ( schema map[string]any ) {
	for kind.Kind() == reflect.Pointer { kind = kind.Elem() }
	if declares( kind , namedType ) {
		schema = builder.named( kind )
		return
	}
	schema = builder.reflected( kind )
	return
}

func ( builder *schemas ) named( kind reflect.Type ) ( schema map[string]any ) {
	name := reflect.Zero( kind ).Interface().( Named ).APIName()
	schema = ref( name )
	if _ , started := builder.building[ name ]; started { return }
	builder.building[ name ] = kind
	builder.components[ name ] = builder.reflected( kind )
	return
}

// reflected is a type's own schema, never a $ref to itself.
func ( builder *schemas ) reflected( kind reflect.Type ) ( schema map[string]any ) {
	seen := map[string]reflect.Type{}
	if declares( kind , unionType ) {
		// Its own fields are only how it is sent; its schema is the
		// variants it lists.
		schema = ( &normalizer{ builder: builder , seen: seen } ).union( kind )
		return
	}
	reflector := &jsonschema.Reflector{
		ExpandedStruct:            true,
		AllowAdditionalProperties: true,
		Namer: func( found reflect.Type ) ( name string ) {
			// An unnamed type (a slice, a map) is written out where it is
			// used, so the field's own tags (minItems) stay on it.
			if found.Name() == "" { return }
			name = "~" + found.String()
			seen[ name ] = found
			return
		},
		Mapper: func( found reflect.Type ) ( result *jsonschema.Schema ) {
			if found != kind && declares( found , namedType ) == false && declares( found , unionType ) {
				result = &jsonschema.Schema{ Extras: map[string]any{ unionMarker: found.String() } }
				seen[ "union:" + found.String() ] = found
			}
			return
		},
	}
	var raw map[string]any
	var document *jsonschema.Schema
	if kind.Kind() == reflect.Struct {
		document = reflector.ReflectFromType( kind )
	} else {
		// invopop expands only a struct at the top; anything else is
		// reflected as a field of a throwaway struct.
		holder := reflect.StructOf( []reflect.StructField{ { Name: "V" , Type: kind , Tag: `json:"v"` } } )
		document = reflector.ReflectFromType( holder )
		defer func() {
			properties , _ := schema[ "properties" ].( map[string]any )
			schema , _ = properties[ "v" ].( map[string]any )
		}()
	}
	encoded , _ := json.Marshal( document )
	_ = json.Unmarshal( encoded , &raw )
	defs , _ := raw[ "$defs" ].( map[string]any )
	normalizer := &normalizer{ builder: builder , defs: defs , seen: seen }
	schema = normalizer.node( raw , kind )
	if declares( kind , describedType ) {
		schema[ "description" ] = reflect.Zero( kind ).Interface().( Described ).APIDoc()
	}
	return
}

type normalizer struct {
	builder *schemas
	defs    map[string]any
	seen    map[string]reflect.Type
}

// node is one schema of invopop's answer, for a value of type kind (nil when
// not known), in the docs' shape.
func ( normalizer *normalizer ) node( raw any , kind reflect.Type ) ( schema map[string]any ) {
	for kind != nil && kind.Kind() == reflect.Pointer { kind = kind.Elem() }
	source , _ := raw.( map[string]any )
	if source == nil {
		schema = map[string]any{} // true: any value
		return
	}
	if target , _ := source[ "$ref" ].( string ); target != "" {
		name := target[ strings.LastIndex( target , "/" ) + 1 : ]
		found := normalizer.seen[ name ]
		if found == nil { found = kind }
		switch {
		case found != nil && declares( found , namedType ):
			schema = normalizer.builder.named( found )
		case found != nil && found.Kind() == reflect.Struct && found != kind:
			schema = normalizer.builder.reflected( found )
		default:
			schema = normalizer.node( normalizer.defs[ name ] , found )
			if found != nil && declares( found , describedType ) {
				schema[ "description" ] = reflect.Zero( found ).Interface().( Described ).APIDoc()
			}
		}
		// A field's own tags (its description, an array's limits) sit beside
		// invopop's $ref.
		copied := map[string]any{}
		for key , value := range schema { copied[ key ] = value }
		for key , value := range source {
			if key == "$ref" { continue }
			if key == "examples" {
				if list , _ := value.( []any ); len( list ) > 0 { copied[ "example" ] = list[ 0 ] }
				continue
			}
			copied[ key ] = value
		}
		schema = copied
		return
	}
	if marked , _ := source[ unionMarker ].( string ); marked != "" {
		schema = normalizer.union( normalizer.seen[ "union:" + marked ] )
		return
	}
	if variants , _ := source[ "oneOf" ].( []any ); len( variants ) == 2 && isNull( variants[ 1 ] ) {
		// invopop's answer for jsonschema:"nullable".
		inner := normalizer.node( variants[ 0 ] , kind )
		schema = nullable( inner )
		return
	}
	schema = map[string]any{}
	for key , value := range source {
		switch key {
		case "$schema" , "$id" , "$defs" , "additionalProperties" , "properties" , "required" , "items":
		case "examples":
			if list , _ := value.( []any ); len( list ) > 0 { schema[ "example" ] = list[ 0 ] }
		default:
			schema[ key ] = value
		}
	}
	if items , found := source[ "items" ]; found {
		var element reflect.Type
		if kind != nil && ( kind.Kind() == reflect.Slice || kind.Kind() == reflect.Array ) { element = kind.Elem() }
		schema[ "items" ] = normalizer.node( items , element )
	}
	if extra , found := source[ "additionalProperties" ]; found {
		if allowed , is_bool := extra.( bool ); is_bool == false || allowed == false {
			var element reflect.Type
			if kind != nil && kind.Kind() == reflect.Map { element = kind.Elem() }
			if allowed , is_bool := extra.( bool ); is_bool && allowed == false {
				schema[ "additionalProperties" ] = false
			} else {
				schema[ "additionalProperties" ] = normalizer.node( extra , element )
			}
		}
	}
	if properties , found := source[ "properties" ].( map[string]any ); found {
		fields := map[string]reflect.StructField{}
		if kind != nil && kind.Kind() == reflect.Struct { fields = jsonFields( kind ) }
		own := map[string]any{}
		for name , property := range properties {
			field , known := fields[ name ]
			var field_type reflect.Type
			if known { field_type = field.Type }
			value := normalizer.node( property , field_type )
			if known && field.Type.Kind() == reflect.Pointer { value = nullable( value ) }
			own[ name ] = value
		}
		if len( own ) > 0 { schema[ "properties" ] = own }
	}
	if required , found := source[ "required" ].( []any ); found && len( required ) > 0 {
		sorted := append( []any{} , required... )
		sort.Slice( sorted , func( a , b int ) bool { return sorted[ a ].( string ) < sorted[ b ].( string ) } )
		schema[ "required" ] = sorted
	}
	return
}

func ( normalizer *normalizer ) union( kind reflect.Type ) ( schema map[string]any ) {
	variants := []any{}
	for _ , variant := range reflect.Zero( kind ).Interface().( Union ).APIOneOf() {
		variants = append( variants , normalizer.builder.typed( reflect.TypeOf( variant ) ) )
	}
	schema = map[string]any{ "oneOf": variants }
	if declares( kind , describedType ) {
		schema[ "description" ] = reflect.Zero( kind ).Interface().( Described ).APIDoc()
	}
	return
}

func isNull( raw any ) ( result bool ) {
	source , _ := raw.( map[string]any )
	result = len( source ) == 1 && source[ "type" ] == "null"
	return
}

// nullable lets a schema be null too: a scalar's type becomes [type, "null"],
// anything else is oneOf it or null. A description and an x-feature stay outside.
func nullable( schema map[string]any ) ( result map[string]any ) {
	if typed , ok := schema[ "type" ].( string ); ok && scalarTypes[ typed ] {
		result = schema
		result[ "type" ] = []any{ typed , "null" }
		return
	}
	if list , ok := schema[ "type" ].( []any ); ok && len( list ) == 2 && list[ 1 ] == "null" {
		result = schema
		return
	}
	if variants , ok := schema[ "oneOf" ].( []any ); ok && len( variants ) == 2 && isNull( variants[ 1 ] ) {
		result = schema
		return
	}
	inner := map[string]any{}
	result = map[string]any{}
	for key , value := range schema {
		if key == "description" || key == "x-feature" {
			result[ key ] = value
		} else {
			inner[ key ] = value
		}
	}
	result[ "oneOf" ] = []any{ inner , map[string]any{ "type": "null" } }
	return
}

// jsonName is a field's JSON name, "" for one encoding/json leaves out, and
// whether it is left out when empty.
func jsonName( field reflect.StructField ) ( name string , omit bool ) {
	if field.IsExported() == false { return }
	tag := field.Tag.Get( "json" )
	if tag == "-" { return }
	pieces := strings.Split( tag , "," )
	name = pieces[ 0 ]
	if name == "" { name = field.Name }
	for _ , option := range pieces[ 1 : ] {
		if option == "omitempty" || option == "omitzero" { omit = true }
	}
	return
}

var scalarTypes = map[string]bool{ "string": true , "integer": true , "number": true , "boolean": true }

// jsonFields are a struct's fields by their JSON names, an embedded struct's
// included, the way encoding/json sees them.
func jsonFields( kind reflect.Type ) ( fields map[string]reflect.StructField ) {
	fields = map[string]reflect.StructField{}
	for index := 0 ; index < kind.NumField() ; index++ {
		field := kind.Field( index )
		if field.Anonymous && field.Tag.Get( "json" ) == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer { embedded = embedded.Elem() }
			if embedded.Kind() == reflect.Struct {
				for name , inner := range jsonFields( embedded ) {
					if _ , taken := fields[ name ]; taken == false { fields[ name ] = inner }
				}
				continue
			}
		}
		name , _ := jsonName( field )
		if name != "" { fields[ name ] = field }
	}
	return
}
