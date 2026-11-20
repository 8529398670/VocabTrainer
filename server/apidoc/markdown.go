package apidoc

import (
	fmt "fmt"
	math "math"
	sort "sort"
	strconv "strconv"
	strings "strings"
)

// Markdown is config/api.md: the same reference as api.json, as tables a
// person reads. spec and manifest are in Plain form; manifest is nil when the
// app has no MCP. A route's MCP tool links into config/mcp.md (MCPMarkdown). The Python template renders the same bytes from the same
// documents, so keep the two in step.
func Markdown( spec map[string]any , manifest map[string]any ) ( text []byte ) {
	out := &strings.Builder{}
	info := object( spec[ "info" ] )
	components := object( spec[ "components" ] )
	paths := object( spec[ "paths" ] )
	tools := map[string][]string{}
	if manifest != nil {
		for _ , raw := range list( manifest[ "tools" ] ) {
			tool := object( raw )
			route := str( tool[ "route" ] )
			tools[ route ] = append( tools[ route ] , str( tool[ "name" ] ) )
		}
	}
	// Every operation, by tag, in path then method order.
	type operation struct {
		method string
		path   string
		op     map[string]any
	}
	by_tag := map[string][]operation{}
	path_names := keys( paths )
	for _ , path := range path_names {
		item := object( paths[ path ] )
		for _ , method := range []string{ "get" , "post" , "put" , "patch" , "delete" } {
			if raw , found := item[ method ]; found {
				op := object( raw )
				tag := str( list( op[ "tags" ] )[ 0 ] )
				by_tag[ tag ] = append( by_tag[ tag ] , operation{ strings.ToUpper( method ) , path , op } )
			}
		}
	}
	tag_names := []string{}
	for _ , raw := range list( spec[ "tags" ] ) { tag_names = append( tag_names , str( object( raw )[ "name" ] ) ) }

	fmt.Fprintf( out , "# %s\n\n" , str( info[ "title" ] ) )
	fmt.Fprintf( out , "%s\n\n" , str( info[ "description" ] ) )
	out.WriteString( "This file is generated: do not edit it. Change the docs next to the handlers and run `scripts/api_docs.sh`.\n\n" )
	out.WriteString( "## Contents\n\n" )
	out.WriteString( "- [Calling the API](#calling-the-api)\n- [Errors](#errors)\n" )
	for _ , tag := range tag_names { fmt.Fprintf( out , "- [%s](#%s)\n" , tag , slug( tag ) ) }
	out.WriteString( "- [Schemas](#schemas)\n" )
	out.WriteString( "\n## Calling the API\n\n" )
	schemes := object( components[ "securitySchemes" ] )
	table( out , []string{ "Way in" , "How" } , [][]string{
		{ "Browser session" , str( object( schemes[ "sessionCookie" ] )[ "description" ] ) },
		{ "API key" , str( object( schemes[ "apiKey" ] )[ "description" ] ) },
	} )
	out.WriteString( "## Errors\n\nAn error reply is a JSON object whose `error` says what went wrong.\n\n" )
	responses := object( components[ "responses" ] )
	error_rows := [][]string{}
	for _ , name := range keys( responses ) {
		response := object( responses[ name ] )
		schema := object( object( object( response[ "content" ] )[ "application/json" ] )[ "schema" ] )
		kind , _ , _ := describe( schema )
		error_rows = append( error_rows , []string{ strings.TrimPrefix( name , "E" ) , str( response[ "description" ] ) , kind } )
	}
	table( out , []string{ "Status" , "Meaning" , "Body" } , error_rows )

	for _ , tag := range tag_names {
		fmt.Fprintf( out , "## %s\n\n" , tag )
		rows := [][]string{}
		for _ , entry := range by_tag[ tag ] {
			heading := entry.method + " " + entry.path
			rows = append( rows , []string{ entry.method , "[`" + entry.path + "`](#" + slug( heading ) + ")" , str( entry.op[ "summary" ] ) , who( entry.op ) } )
		}
		table( out , []string{ "Method" , "Path" , "Summary" , "Who may call" } , rows )
		for _ , entry := range by_tag[ tag ] {
			op := entry.op
			fmt.Fprintf( out , "### %s %s\n\n" , entry.method , entry.path )
			fmt.Fprintf( out , "%s\n\n" , str( op[ "summary" ] ) )
			if description := str( op[ "description" ] ); description != "" { fmt.Fprintf( out , "%s\n\n" , description ) }
			facts := [][]string{ { "Who may call" , who( op ) } }
			if manifest != nil {
				if names , found := tools[ entry.method + " " + entry.path ]; found {
					cells := []string{}
					for _ , name := range names { cells = append( cells , "[`" + name + "`](mcp.md#" + slug( name ) + ")" ) }
					facts = append( facts , []string{ "MCP tool" , strings.Join( cells , ", " ) } )
				} else if reason := str( op[ "x-mcp-http" ] ); reason != "" {
					facts = append( facts , []string{ "MCP tool" , "None, HTTP only: " + reason } )
				}
			}
			errors := []string{}
			success := ""
			for _ , status := range keys( object( op[ "responses" ] ) ) {
				if _ , is_ref := object( object( op[ "responses" ] )[ status ] )[ "$ref" ]; is_ref {
					errors = append( errors , status )
				} else {
					success = status
				}
			}
			facts = append( facts , []string{ "Errors" , strings.Join( errors , ", " ) } )
			table( out , []string{ "" , "" } , facts )
			if parameters := list( op[ "parameters" ] ); len( parameters ) > 0 {
				out.WriteString( "**Parameters**\n\n" )
				rows := [][]string{}
				for _ , raw := range parameters {
					parameter := object( raw )
					schema := object( parameter[ "schema" ] )
					kind , _ , _ := describe( schema )
					rows = append( rows , []string{ "`" + str( parameter[ "name" ] ) + "`" , str( parameter[ "in" ] ) , kind , yes( parameter[ "required" ] == true ) , limits( schema ) , str( parameter[ "description" ] ) } )
				}
				table( out , []string{ "Name" , "In" , "Type" , "Required" , "Limits" , "Description" } , rows )
			}
			if body := object( op[ "requestBody" ] ); body != nil {
				out.WriteString( "**Request body**\n\n" )
				content( out , object( body[ "content" ] ) )
			}
			reply := object( object( op[ "responses" ] )[ success ] )
			fmt.Fprintf( out , "**Reply %s**: %s\n\n" , success , str( reply[ "description" ] ) )
			content( out , object( reply[ "content" ] ) )
		}
	}

	out.WriteString( "## Schemas\n\nThe shapes the routes above refer to by name.\n\n" )
	schemas := object( components[ "schemas" ] )
	for _ , name := range keys( schemas ) {
		fmt.Fprintf( out , "### %s\n\n" , name )
		block( out , object( schemas[ name ] ) )
	}

	text = []byte( strings.TrimRight( out.String() , "\n" ) + "\n" )
	return
}

// MCPMarkdown is config/mcp.md: mcp.json as tables a person reads, beside
// api.md, which each tool's route links into. spec and manifest are in Plain
// form. The Python template renders the same bytes, so keep the two in step.
func MCPMarkdown( spec map[string]any , manifest map[string]any ) ( text []byte ) {
	out := &strings.Builder{}
	title := strings.TrimSuffix( str( object( spec[ "info" ] )[ "title" ] ) , " API" )
	fmt.Fprintf( out , "# %s MCP tools\n\n" , title )
	out.WriteString( "This file is generated: do not edit it. Change the tools next to their handlers and run `scripts/api_docs.sh`. Every route is in [api.md](api.md).\n\n" )
	fmt.Fprintf( out , "%s\n\n" , str( manifest[ "description" ] ) )
	table( out , []string{ "" , "" } , [][]string{
		{ "Endpoint" , "`POST " + str( manifest[ "endpoint" ] ) + "`" },
		{ "Transport" , str( manifest[ "transport" ] ) },
		{ "Auth" , str( manifest[ "auth" ] ) },
	} )
	rows := [][]string{}
	for _ , raw := range list( manifest[ "tools" ] ) {
		tool := object( raw )
		admin := "any key"
		if tool[ "adminOnly" ] == true { admin = "admin keys" }
		rows = append( rows , []string{ "[`" + str( tool[ "name" ] ) + "`](#" + slug( str( tool[ "name" ] ) ) + ")" , routeLink( tool[ "route" ] ) , admin , str( tool[ "title" ] ) } )
	}
	table( out , []string{ "Tool" , "Route" , "Who" , "What" } , rows )
	for _ , raw := range list( manifest[ "tools" ] ) {
		tool := object( raw )
		fmt.Fprintf( out , "## %s\n\n" , str( tool[ "name" ] ) )
		fmt.Fprintf( out , "%s\n\n" , str( tool[ "description" ] ) )
		hints := object( tool[ "annotations" ] )
		table( out , []string{ "" , "" } , [][]string{
			{ "Route" , routeLink( tool[ "route" ] ) },
			{ "Read only" , yes( hints[ "readOnlyHint" ] == true ) },
			{ "Destructive" , yes( hints[ "destructiveHint" ] == true ) },
		} )
		out.WriteString( "**Arguments**\n\n" )
		block( out , object( tool[ "inputSchema" ] ) )
	}
	if http_only := list( manifest[ "http_only" ] ); len( http_only ) > 0 {
		out.WriteString( "## HTTP only\n\nRoutes an API key may call that have no tool, and why.\n\n" )
		rows := [][]string{}
		for _ , raw := range http_only {
			entry := object( raw )
			rows = append( rows , []string{ routeLink( entry[ "route" ] ) , str( entry[ "reason" ] ) } )
		}
		table( out , []string{ "Route" , "Why" } , rows )
	}
	text = []byte( strings.TrimRight( out.String() , "\n" ) + "\n" )
	return
}

// routeLink is a tool's route, linked to its section of api.md.
func routeLink( raw any ) ( text string ) {
	text = "[`" + str( raw ) + "`](api.md#" + slug( str( raw ) ) + ")"
	return
}

// content is a request or reply body.
func content( out *strings.Builder , types map[string]any ) {
	for _ , content_type := range keys( types ) {
		schema := object( object( types[ content_type ] )[ "schema" ] )
		if content_type != "application/json" {
			kind := "Text"
			if str( schema[ "format" ] ) == "binary" { kind = "Raw bytes" }
			fmt.Fprintf( out , "%s, as `%s`.\n\n" , kind , content_type )
			continue
		}
		block( out , schema )
	}
}

// block is a whole schema: a table of its fields, or what it is made of.
func block( out *strings.Builder , schema map[string]any ) {
	if _ , has := schema[ "properties" ]; has {
		rows := [][]string{}
		fields( schema , "" , &rows )
		table( out , fieldHeader , rows )
		return
	}
	variants := list( schema[ "oneOf" ] )
	if len( variants ) > 1 && nonNull( variants ) == nil {
		out.WriteString( "One of these:\n\n" )
		for index , raw := range variants {
			variant := object( raw )
			if _ , has := variant[ "properties" ]; has {
				fmt.Fprintf( out , "%d. An object:\n\n" , index + 1 )
				rows := [][]string{}
				fields( variant , "" , &rows )
				table( out , fieldHeader , rows )
				continue
			}
			kind , _ , _ := describe( variant )
			fmt.Fprintf( out , "%d. %s\n\n" , index + 1 , kind )
		}
		return
	}
	if parts := list( schema[ "allOf" ] ); len( parts ) > 0 {
		names := []string{}
		inline := []map[string]any{}
		for _ , raw := range parts {
			part := object( raw )
			if str( part[ "$ref" ] ) != "" {
				word , _ , _ := describe( part )
				names = append( names , word )
			} else {
				inline = append( inline , part )
			}
		}
		if len( inline ) == 0 {
			fmt.Fprintf( out , "Every field of %s.\n\n" , strings.Join( names , " and " ) )
			return
		}
		fmt.Fprintf( out , "Every field of %s, plus these:\n\n" , strings.Join( names , " and " ) )
		for _ , part := range inline { block( out , part ) }
		return
	}
	kind , children , array := describe( schema )
	if kind == "object" && len( children ) == 0 {
		out.WriteString( "An empty object.\n\n" )
		return
	}
	fmt.Fprintf( out , "%s.\n\n" , upper( kind ) )
	prefix := ""
	if array { prefix = "[]." }
	for _ , child := range children {
		rows := [][]string{}
		fields( child , prefix , &rows )
		table( out , fieldHeader , rows )
	}
}

var fieldHeader = []string{ "Field" , "Type" , "Required" , "Limits" , "Description" }

// fields flattens an object's properties into rows: a nested object's own
// fields follow it as parent.child, and an array's items' as parent[].child.
func fields( schema map[string]any , prefix string , rows *[][]string ) {
	properties := object( schema[ "properties" ] )
	required := map[string]bool{}
	for _ , name := range list( schema[ "required" ] ) { required[ str( name ) ] = true }
	for _ , name := range keys( properties ) {
		property := object( properties[ name ] )
		kind , children , array := describe( property )
		*rows = append( *rows , []string{ "`" + prefix + name + "`" , kind , yes( required[ name ] ) , limits( property ) , doc( property ) } )
		inner := prefix + name + "."
		if array { inner = prefix + name + "[]." }
		for _ , child := range children { fields( child , inner , rows ) }
	}
}

// describe is a schema's type in words, the inline objects whose fields
// belong under it, and whether those are an array's items.
func describe( schema map[string]any ) ( kind string , children []map[string]any , array bool ) {
	if target := str( schema[ "$ref" ] ); target != "" {
		name := target[ strings.LastIndex( target , "/" ) + 1 : ]
		kind = "[`" + name + "`](#" + slug( name ) + ")"
		return
	}
	if variants := list( schema[ "oneOf" ] ); len( variants ) > 0 {
		if single := nonNull( variants ); single != nil {
			kind , children , array = describe( single )
			kind += " or null"
			return
		}
		words := []string{}
		for _ , raw := range variants {
			word , more , _ := describe( object( raw ) )
			words = append( words , word )
			children = append( children , more... )
		}
		kind = strings.Join( words , " or " )
		return
	}
	if parts := list( schema[ "allOf" ] ); len( parts ) > 0 {
		words := []string{}
		for _ , raw := range parts {
			word , more , _ := describe( object( raw ) )
			words = append( words , word )
			children = append( children , more... )
		}
		kind = strings.Join( words , " + " )
		return
	}
	base , nullable := "" , false
	switch typed := schema[ "type" ].( type ) {
	case string:
		base = typed
	case []any:
		for _ , raw := range typed {
			if str( raw ) == "null" { nullable = true } else { base = str( raw ) }
		}
	}
	switch base {
	case "":
		kind = "any"
	case "array":
		inner , more , _ := describe( object( schema[ "items" ] ) )
		kind , children , array = "array of " + inner , more , true
	case "object":
		kind = "object"
		if _ , has := schema[ "properties" ]; has {
			children = []map[string]any{ schema }
		} else if extra := object( schema[ "additionalProperties" ] ); extra != nil {
			inner , _ , _ := describe( extra )
			kind = "map of " + inner
		}
	default:
		kind = base
		if format := str( schema[ "format" ] ); format != "" { kind += " (" + format + ")" }
	}
	if nullable { kind += " or null" }
	return
}

func nonNull( variants []any ) ( single map[string]any ) {
	others := []map[string]any{}
	nulls := 0
	for _ , raw := range variants {
		variant := object( raw )
		if str( variant[ "type" ] ) == "null" && len( variant ) == 1 {
			nulls++
		} else {
			others = append( others , variant )
		}
	}
	if nulls == 1 && len( others ) == 1 { single = others[ 0 ] }
	return
}

// limits are the rules a value must keep to, in words.
func limits( schema map[string]any ) ( text string ) {
	if single := nonNull( list( schema[ "oneOf" ] ) ); single != nil { schema = single }
	parts := []string{}
	minimum , has_minimum := schema[ "minimum" ]
	maximum , has_maximum := schema[ "maximum" ]
	switch {
	case has_minimum && has_maximum:
		parts = append( parts , value( minimum ) + " to " + value( maximum ) )
	case has_minimum:
		parts = append( parts , "at least " + value( minimum ) )
	case has_maximum:
		parts = append( parts , "at most " + value( maximum ) )
	}
	shortest , has_shortest := schema[ "minLength" ]
	longest , has_longest := schema[ "maxLength" ]
	switch {
	case has_shortest && has_longest:
		parts = append( parts , value( shortest ) + " to " + value( longest ) + " characters" )
	case has_shortest:
		parts = append( parts , "at least " + value( shortest ) + " characters" )
	case has_longest:
		parts = append( parts , "at most " + value( longest ) + " characters" )
	}
	if values := list( schema[ "enum" ] ); len( values ) > 0 {
		words := []string{}
		for _ , raw := range values { words = append( words , "`" + value( raw ) + "`" ) }
		parts = append( parts , "one of " + strings.Join( words , ", " ) )
	}
	if raw , found := schema[ "default" ]; found { parts = append( parts , "default `" + value( raw ) + "`" ) }
	if raw , found := schema[ "example" ]; found { parts = append( parts , "e.g. `" + value( raw ) + "`" ) }
	text = strings.Join( parts , "; " )
	return
}

func doc( schema map[string]any ) ( text string ) {
	text = str( schema[ "description" ] )
	if text == "" {
		if single := nonNull( list( schema[ "oneOf" ] ) ); single != nil { text = str( single[ "description" ] ) }
	}
	return
}

// who is who may call an operation, in words.
func who( op map[string]any ) ( text string ) {
	ways := map[string]bool{}
	for _ , raw := range list( op[ "security" ] ) {
		for name := range object( raw ) { ways[ name ] = true }
	}
	switch {
	case len( ways ) == 0:
		text = "anyone"
		return
	case ways[ "sessionCookie" ] && ways[ "apiKey" ]:
		text = "browser session or API key"
	case ways[ "sessionCookie" ]:
		text = "browser session only"
	default:
		text = "API key only"
	}
	switch str( op[ "x-required-role" ] ) {
	case "admin":
		text += ", admins"
	case "owner":
		text += ", the owner"
	default:
		text += ", anyone signed in"
	}
	return
}

func table( out *strings.Builder , header []string , rows [][]string ) {
	line := func( cells []string ) {
		escaped := []string{}
		for _ , cell := range cells { escaped = append( escaped , cellText( cell ) ) }
		out.WriteString( "| " + strings.Join( escaped , " | " ) + " |\n" )
	}
	line( header )
	rule := []string{}
	for range header { rule = append( rule , "---" ) }
	out.WriteString( "|" + strings.Join( rule , "|" ) + "|\n" )
	for _ , row := range rows { line( row ) }
	out.WriteString( "\n" )
}

func cellText( text string ) ( result string ) {
	result = strings.ReplaceAll( text , "|" , "\\|" )
	result = strings.ReplaceAll( result , "\r\n" , " " )
	result = strings.ReplaceAll( result , "\n" , " " )
	return
}

// slug is the anchor GitHub gives a heading.
func slug( heading string ) ( result string ) {
	builder := &strings.Builder{}
	for _ , character := range strings.ToLower( heading ) {
		switch {
		case character >= 'a' && character <= 'z' , character >= '0' && character <= '9' , character == '_' , character == '-':
			builder.WriteRune( character )
		case character == ' ':
			builder.WriteRune( '-' )
		}
	}
	result = builder.String()
	return
}

func upper( text string ) ( result string ) {
	result = text
	if text != "" && text[ 0 ] >= 'a' && text[ 0 ] <= 'z' { result = strings.ToUpper( text[ :1 ] ) + text[ 1: ] }
	return
}

func yes( flag bool ) ( text string ) {
	text = "no"
	if flag { text = "yes" }
	return
}

func value( raw any ) ( text string ) {
	switch typed := raw.( type ) {
	case nil:
		text = "null"
	case string:
		text = typed
	case bool:
		text = strconv.FormatBool( typed )
	case float64:
		if typed == math.Trunc( typed ) && math.Abs( typed ) < 1e15 {
			text = strconv.FormatInt( int64( typed ) , 10 )
		} else {
			text = strconv.FormatFloat( typed , 'f' , -1 , 64 )
		}
	default:
		text = fmt.Sprint( typed )
	}
	return
}

func object( raw any ) ( result map[string]any ) {
	result , _ = raw.( map[string]any )
	return
}

func list( raw any ) ( result []any ) {
	result , _ = raw.( []any )
	return
}

func str( raw any ) ( result string ) {
	result , _ = raw.( string )
	return
}

func keys( document map[string]any ) ( result []string ) {
	for key := range document { result = append( result , key ) }
	sort.Strings( result )
	return
}
