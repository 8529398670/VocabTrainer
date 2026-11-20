package routes

import (
	reflect "reflect"
	sort "sort"
	strings "strings"
	sync "sync"
	time "time"
)

// Every list the API hands out is paged the same way: ?limit= and ?offset= in,
// and { "<rows>": [...], "next": <offset of the next page, or null>, "total":
// <rows in all> } out.
//
// An offset rather than a cursor, on purpose. Most of these lists sort on a
// field that is encrypted at rest, so every page has to read the whole list
// anyway and a cursor would save nothing; and the natural cursor for a key or
// a session is its record id, which has no business in a URL. The cost is that
// a row deleted between two pages shifts the next one by a row. The screens
// reload from the first page after any change, so they never see that.
//
// Paging here bounds the response, not the read. See Known limits in
// references/architecture.md.
//
// A list a screen can sort also takes ?sort=<field>&dir=asc|desc, and one it can
// search takes ?q=, both on the whole list before it is cut, since the browser
// only ever holds a page of it. See sortedPage and matchRows.

const (
	pageDefault       = 200
	pageMax           = 1000
	pageLimitMessage  = "limit must be 1-1000"
	pageOffsetMessage = "offset must be 0 or more"
)

// Page is the query every paged list takes, and an MCP list tool's
// arguments.
type Page struct {
	Limit  int `json:"limit,omitempty" query:"limit" jsonschema:"minimum=1,maximum=1000,default=200" jsonschema_description:"Rows to return. Default 200." error:"limit must be 1-1000"`
	Offset int `json:"offset,omitempty" query:"offset" jsonschema:"minimum=0" jsonschema_description:"Rows to skip: the next value of the previous page." error:"offset must be 0 or more"`
}

// Direction is the ?dir= of a list a screen can sort.
type Direction struct {
	Dir string `json:"dir,omitempty" query:"dir" jsonschema:"enum=asc,enum=desc" jsonschema_description:"Default asc." error:"dir must be asc or desc"`
}

// Paged is what every paged reply carries beside its rows.
type Paged struct {
	Next  *int `json:"next" jsonschema_description:"Offset of the next page, or null on the last one."`
	Total int  `json:"total"`
}

// cutPage is one page of rows, which must already be in their final order.
func cutPage[ T any ]( rows []T , page Page ) ( cut []T , paged Paged ) {
	limit := page.Limit
	if limit == 0 { limit = pageDefault }
	total := len( rows )
	start := min( page.Offset , total )
	end := min( start + limit , total )
	cut = rows[ start:end ]
	if cut == nil { cut = []T{} }
	paged.Total = total
	if end < total {
		next := end
		paged.Next = &next
	}
	return
}

// sortedPage is cutPage for a list a screen can sort: field is a row field
// by its JSON name, already checked against the route's sort enum.
func sortedPage[ T any ]( rows []T , field string , direction Direction , page Page ) ( cut []T , paged Paged ) {
	if field != "" {
		rows = sortRows( rows , field , direction.Dir == "desc" )
	}
	cut , paged = cutPage( rows , page )
	return
}

// rowField is a typed row's field by its JSON name, for sorting and
// searching. Rows are structs (or pointers to them).
func rowField( row any , name string ) ( value any ) {
	found := reflect.ValueOf( row )
	for found.Kind() == reflect.Pointer {
		if found.IsNil() { return }
		found = found.Elem()
	}
	if found.Kind() == reflect.Map {
		if item := found.MapIndex( reflect.ValueOf( name ) ); item.IsValid() { value = item.Interface() }
		return
	}
	if found.Kind() != reflect.Struct { return }
	index , known := jsonFieldIndex( found.Type() )[ name ]
	if known == false { return }
	field , err := found.FieldByIndexErr( index )
	if err != nil { return }
	value = field.Interface()
	return
}

var fieldIndexes sync.Map

func jsonFieldIndex( kind reflect.Type ) ( index map[string][]int ) {
	if cached , found := fieldIndexes.Load( kind ); found {
		index = cached.( map[string][]int )
		return
	}
	index = map[string][]int{}
	for _ , field := range reflect.VisibleFields( kind ) {
		if field.IsExported() == false || field.Anonymous { continue }
		name := strings.Split( field.Tag.Get( "json" ) , "," )[ 0 ]
		if name == "-" { continue }
		if name == "" { name = field.Name }
		if _ , taken := index[ name ]; taken == false { index[ name ] = field.Index }
	}
	fieldIndexes.Store( kind , index )
	return
}

// matchRows keeps the rows with query in one of fields, in any case. An
// empty query keeps them all. Here rather than in the browser, like sorting.
func matchRows[ T any ]( rows []T , query string , fields ...string ) ( matched []T ) {
	wanted := strings.ToLower( strings.TrimSpace( query ) )
	if wanted == "" {
		matched = rows
		return
	}
	matched = []T{}
	for _ , row := range rows {
		for _ , field := range fields {
			text , is_text := rowField( row , field ).( string )
			if is_text && strings.Contains( strings.ToLower( text ) , wanted ) {
				matched = append( matched , row )
				break
			}
		}
	}
	return
}

// sortRows orders rows by one field. Rows without it go last whichever way the
// list runs, because "never used" is not the oldest date. Rows that tie stay in
// the list's own order, so a page holds still.
func sortRows[ T any ]( rows []T , field string , descending bool ) ( sorted []T ) {
	type keyed struct {
		key sortKey
		row T
	}
	present := []keyed{}
	absent := []T{}
	for _ , row := range rows {
		key := sortKeyOf( rowField( row , field ) )
		if key.missing {
			absent = append( absent , row )
		} else {
			present = append( present , keyed{ key , row } )
		}
	}
	before := func( a sortKey , b sortKey ) ( result bool ) {
		result = a.text < b.text || ( a.text == b.text && a.number < b.number )
		return
	}
	sort.SliceStable( present , func( i int , j int ) bool {
		if descending { return before( present[ j ].key , present[ i ].key ) }
		return before( present[ i ].key , present[ j ].key )
	} )
	sorted = make( []T , 0 , len( rows ) )
	for _ , item := range present {
		sorted = append( sorted , item.row )
	}
	sorted = append( sorted , absent... )
	return
}

// sortKey is one row's value, comparable whatever its type: text in any case,
// dates, numbers and switches. missing is a value the row does not have.
type sortKey struct {
	text    string
	number  float64
	missing bool
}

func sortKeyOf( value any ) ( key sortKey ) {
	switch typed := value.( type ) {
	case string:
		key = sortKey{ text: strings.ToLower( typed ) , missing: typed == "" }
	case time.Time:
		// A typed row leaves out a date it does not have (omitzero).
		if typed.IsZero() {
			key.missing = true
			return
		}
		key.number = float64( typed.UnixMicro() )
	case *time.Time:
		if typed == nil {
			key.missing = true
			return
		}
		key.number = float64( typed.UnixMicro() )
	case bool:
		if typed { key.number = 1 }
	case int:
		key.number = float64( typed )
	case int64:
		key.number = float64( typed )
	case uint64:
		key.number = float64( typed )
	default:
		key.missing = true
	}
	return
}

