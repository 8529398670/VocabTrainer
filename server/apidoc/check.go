package apidoc

import (
	json "encoding/json"
	math "math"
	sort "sort"
	strconv "strconv"
	strings "strings"
)

// Check is what is wrong with a JSON reply, held against the docs: a
// required field missing, a field the docs do not list, or a value of the
// wrong type. spec is in Plain form; path is the spec's path. The tests run
// every success reply through it, so docs that drift from what a handler
// really sends fail there.
func Check( spec map[string]any , method string , path string , status int , body []byte ) ( problems []string ) {
	operation := object( object( object( spec[ "paths" ] )[ path ] )[ strings.ToLower( method ) ] )
	response := object( object( operation[ "responses" ] )[ strconv.Itoa( status ) ] )
	if response == nil || response[ "$ref" ] != nil { return }
	schema := object( object( object( response[ "content" ] )[ "application/json" ] )[ "schema" ] )
	if schema == nil { return }
	var value any
	if err := json.Unmarshal( body , &value ); err != nil {
		problems = append( problems , "the reply is not JSON" )
		return
	}
	problems = validate( spec , schema , value , "reply" )
	return
}

func resolve( spec map[string]any , schema map[string]any ) ( result map[string]any ) {
	result = schema
	for {
		target := str( result[ "$ref" ] )
		if target == "" { return }
		name := target[ strings.LastIndex( target , "/" ) + 1 : ]
		result = object( object( object( spec[ "components" ] )[ "schemas" ] )[ name ] )
	}
}

func validate( spec map[string]any , schema map[string]any , value any , at string ) ( problems []string ) {
	schema = resolve( spec , schema )
	if variants := list( schema[ "oneOf" ] ); len( variants ) > 0 {
		for _ , variant := range variants {
			if len( validate( spec , object( variant ) , value , at ) ) == 0 { return }
		}
		problems = append( problems , at + ": matches none of its documented shapes" )
		return
	}
	properties := map[string]any{}
	required := []any{}
	has_properties := false
	parts := []map[string]any{ schema }
	if all := list( schema[ "allOf" ] ); len( all ) > 0 {
		parts = nil
		for _ , part := range all { parts = append( parts , resolve( spec , object( part ) ) ) }
	}
	for _ , part := range parts {
		if own , found := part[ "properties" ]; found {
			has_properties = true
			for name , property := range object( own ) { properties[ name ] = property }
		}
		required = append( required , list( part[ "required" ] )... )
	}
	if typed := types( schema ); len( typed ) > 0 && typed[ kindOf( value ) ] == false {
		if kindOf( value ) != "integer" || typed[ "number" ] == false {
			problems = append( problems , at + ": is " + kindOf( value ) + ", documented as " + strings.Join( keysOf( typed ) , " or " ) )
			return
		}
	}
	switch typed := value.( type ) {
	case map[string]any:
		if has_properties {
			for _ , name := range required {
				if _ , found := typed[ str( name ) ]; found == false {
					problems = append( problems , at + "." + str( name ) + ": documented as always there, but missing" )
				}
			}
			for _ , name := range keysOf( typed ) {
				property , documented := properties[ name ]
				if documented == false {
					problems = append( problems , at + "." + name + ": not in the docs" )
					continue
				}
				problems = append( problems , validate( spec , object( property ) , typed[ name ] , at + "." + name )... )
			}
		} else if extra := object( schema[ "additionalProperties" ] ); extra != nil {
			for _ , name := range keysOf( typed ) {
				problems = append( problems , validate( spec , extra , typed[ name ] , at + "." + name )... )
			}
		}
	case []any:
		if items := object( schema[ "items" ] ); items != nil {
			for index , item := range typed {
				problems = append( problems , validate( spec , items , item , at + "[" + strconv.Itoa( index ) + "]" )... )
			}
		}
	}
	return
}

func types( schema map[string]any ) ( result map[string]bool ) {
	result = map[string]bool{}
	switch typed := schema[ "type" ].( type ) {
	case string:
		result[ typed ] = true
	case []any:
		for _ , name := range typed { result[ str( name ) ] = true }
	}
	return
}

func kindOf( value any ) ( kind string ) {
	switch typed := value.( type ) {
	case nil:
		kind = "null"
	case bool:
		kind = "boolean"
	case string:
		kind = "string"
	case float64:
		kind = "number"
		if typed == math.Trunc( typed ) { kind = "integer" }
	case []any:
		kind = "array"
	case map[string]any:
		kind = "object"
	}
	return
}

func keysOf[ V any ]( document map[string]V ) ( result []string ) {
	for key := range document { result = append( result , key ) }
	sort.Strings( result )
	return
}
