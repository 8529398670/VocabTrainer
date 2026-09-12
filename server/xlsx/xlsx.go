// Package xlsx writes the smallest spreadsheet Excel will open, using
// nothing but the standard library.
//
// An .xlsx file is a zip of XML parts, and the subset needed to hand someone
// a few sheets of words is small enough to write out by hand. Doing that
// rather than taking a dependency is a deliberate trade: the alternative
// libraries are large, and this project builds to a single binary with the
// whole frontend inside it -- see build.sh -- so every megabyte of dependency
// is a megabyte in everyone's download for one button on the settings screen.
//
// What is deliberately not here: formulas, multiple fonts, images, charts,
// and a shared string table. Strings are written inline, which costs a few
// bytes per repeated word and saves a whole part and an index.
//
// The layout below is the minimum a strict reader accepts:
//
//	[Content_Types].xml       what every part in the zip is
//	_rels/.rels               where the workbook is
//	xl/workbook.xml           the list of sheets
//	xl/_rels/workbook.xml.rels  where each sheet and the styles are
//	xl/styles.xml             the four cell formats used below
//	xl/worksheets/sheetN.xml  one per sheet, in order
package xlsx

import (
	zip "archive/zip"
	bytes "bytes"
	xml "encoding/xml"
	fmt "fmt"
	io "io"
	math "math"
	strconv "strconv"
	strings "strings"
	time "time"
	utf8 "unicode/utf8"
)

// The four cell formats defined in styles.xml, in the order they appear in
// cellXfs. A cell names one by index, which is what the s="..." attribute is.
const (
	styleGeneral = 0
	styleHeader  = 1
	styleDate    = 2
	styleWrapped = 3
)

// What a cell holds. A spreadsheet distinguishes these three at the file
// level -- a number written as text does not sort or sum -- so the caller
// says which it means rather than the writer guessing from the characters.
type kind int

const (
	kindEmpty kind = iota
	kindText
	kindNumber
	kindDate
)

type Cell struct {
	kind   kind
	text   string
	number float64
	moment time.Time
}

func Text( value string ) ( cell Cell ) {
	value = strings.TrimSpace( value )
	if value == "" { return }
	cell = Cell{ kind: kindText , text: value }
	return
}

// Number writes a numeric cell. A value with no decimal representation --
// NaN, or an infinity -- would be written as the letters "NaN", which is not
// a number to any reader and makes the sheet unopenable in some. An empty
// cell is the honest answer and the only one that keeps the file valid.
func Number( value float64 ) ( cell Cell ) {
	if math.IsNaN( value ) || math.IsInf( value , 0 ) { return }
	cell = Cell{ kind: kindNumber , number: value }
	return
}

// Date writes a real spreadsheet date rather than a string that looks like
// one, so a column of them sorts and filters as dates. A zero time is an
// empty cell: "never seen" is not a date in 1970.
func Date( value time.Time ) ( cell Cell ) {
	if value.IsZero() { return }
	cell = Cell{ kind: kindDate , moment: value }
	return
}

type Column struct {
	Header string

	// Width is in Excel's character units, roughly the number of digits that
	// fit. Zero leaves the sheet's default.
	Width float64

	// Wrap marks a column of prose -- a definition, an example -- so it wraps
	// inside its cell instead of running out across the ones beside it.
	Wrap bool
}

type Sheet struct {
	Name    string
	Columns []Column
	Rows    [][]Cell
}

// Excel counts days from 1899-12-30 rather than 1900-01-01, because it
// carries forward a 1900-that-was-not-a-leap-year from Lotus 1-2-3. Using the
// earlier epoch is how every writer reproduces that bug without special-casing
// the first two months of 1900.
var excelEpoch = time.Date( 1899 , 12 , 30 , 0 , 0 , 0 , 0 , time.UTC )

func serialOf( moment time.Time ) ( serial float64 ) {
	serial = moment.UTC().Sub( excelEpoch ).Hours() / 24
	return
}

// columnName turns a zero-based index into A, B, ... Z, AA, AB. The -1 is
// what makes it bijective base-26: there is no "zero digit", so Z is followed
// by AA rather than by BA.
func columnName( index int ) ( name string ) {
	for index >= 0 {
		name = string( rune( 'A'+index%26 ) ) + name
		index = index/26 - 1
	}
	return
}

// stripInvalid drops the characters XML 1.0 cannot represent at all.
//
// This is not paranoia about our own data: a single stray control character
// makes the whole file unopenable rather than one cell wrong, and the input
// here is a word list generated from third-party corpora. Tab, newline and
// carriage return are the three below 0x20 that are legal; xml.EscapeText
// turns those into character references itself.
//
// It is separate from escape() because writeCell has to know what the text
// looks like *after* this pass -- removing a control character can leave a
// space at either end that was not exposed before.
func stripInvalid( value string ) ( result string ) {
	result = strings.Map( func( r rune ) ( kept rune ) {
		kept = r
		if r == '\t' || r == '\n' || r == '\r' { return }
		if r < 0x20 || r == 0x7f { kept = -1 }
		if r == utf8.RuneError { kept = -1 }
		return
	} , value )
	return
}

// escape makes a run of text safe to drop between XML tags.
func escape( value string ) ( result string ) {
	buffer := &bytes.Buffer{}
	if err := xml.EscapeText( buffer , []byte( stripInvalid( value ) ) ); err != nil { return }
	result = buffer.String()
	return
}

// Excel rejects these in a sheet name, and silently mangles a name longer
// than 31 characters. Both are worth handling here rather than asking every
// caller to remember, since the names come from language.yaml and can be
// edited by anyone running the app.
var sheetNameBanned = strings.NewReplacer(
	"\\" , " " , "/" , " " , "?" , " " , "*" , " " , "[" , " " , "]" , " " , ":" , " " ,
)

func sheetName( name string , index int ) ( result string ) {
	result = strings.TrimSpace( sheetNameBanned.Replace( name ) )
	result = strings.Trim( result , "'" )
	if runes := []rune( result ); len( runes ) > 31 {
		result = strings.TrimSpace( string( runes[ :31 ] ) )
	}
	if result == "" { result = "Sheet" + strconv.Itoa( index+1 ) }
	return
}

// uniqueNames resolves the collision two sheets would cause if they were
// named the same thing -- which a language.yaml edit can easily do, and which
// Excel reports as a corrupt file rather than as the naming clash it is.
func uniqueNames( sheets []Sheet ) ( names []string ) {
	taken := map[string]bool{}
	names = make( []string , len( sheets ) )
	for index , sheet := range sheets {
		name := sheetName( sheet.Name , index )
		candidate := name
		for suffix := 2; taken[ strings.ToLower( candidate ) ]; suffix += 1 {
			candidate = fmt.Sprintf( "%s %d" , name , suffix )
		}
		taken[ strings.ToLower( candidate ) ] = true
		names[ index ] = candidate
	}
	return
}

// Write produces the whole workbook. At least one sheet is required: a
// workbook with none is not a valid file, so it is refused here rather than
// written and rejected by the reader.
func Write( destination io.Writer , sheets []Sheet ) ( err error ) {
	if len( sheets ) == 0 {
		err = fmt.Errorf( "xlsx: a workbook needs at least one sheet" )
		return
	}

	names := uniqueNames( sheets )
	archive := zip.NewWriter( destination )
	stamp := time.Now()

	add := func( path string , body string ) ( write_err error ) {
		writer , write_err := archive.CreateHeader( &zip.FileHeader{
			Name:     path,
			Method:   zip.Deflate,
			Modified: stamp,
		} )
		if write_err != nil { return }
		_ , write_err = io.WriteString( writer , body )
		return
	}

	if err = add( "[Content_Types].xml" , contentTypes( len( sheets ) ) ); err != nil { return }
	if err = add( "_rels/.rels" , rootRelationships() ); err != nil { return }
	if err = add( "xl/workbook.xml" , workbook( names ) ); err != nil { return }
	if err = add( "xl/_rels/workbook.xml.rels" , workbookRelationships( len( sheets ) ) ); err != nil { return }
	if err = add( "xl/styles.xml" , styles() ); err != nil { return }
	for index , sheet := range sheets {
		path := fmt.Sprintf( "xl/worksheets/sheet%d.xml" , index+1 )
		if err = add( path , worksheet( sheet ) ); err != nil { return }
	}

	err = archive.Close()
	return
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"

const (
	namespaceMain          = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	namespaceRelationships = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	namespacePackageRels   = "http://schemas.openxmlformats.org/package/2006/relationships"
	namespaceContentTypes  = "http://schemas.openxmlformats.org/package/2006/content-types"
)

func contentTypes( sheetCount int ) ( result string ) {
	builder := &strings.Builder{}
	builder.WriteString( xmlHeader )
	fmt.Fprintf( builder , `<Types xmlns=%q>` , namespaceContentTypes )
	builder.WriteString( `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` )
	builder.WriteString( `<Default Extension="xml" ContentType="application/xml"/>` )
	builder.WriteString( `<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>` )
	builder.WriteString( `<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>` )
	for index := 1; index <= sheetCount; index += 1 {
		fmt.Fprintf( builder , `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>` , index )
	}
	builder.WriteString( `</Types>` )
	result = builder.String()
	return
}

func rootRelationships() ( result string ) {
	result = xmlHeader +
		`<Relationships xmlns="` + namespacePackageRels + `">` +
		`<Relationship Id="rId1" Type="` + namespaceRelationships + `/officeDocument" Target="xl/workbook.xml"/>` +
		`</Relationships>`
	return
}

func workbook( names []string ) ( result string ) {
	builder := &strings.Builder{}
	builder.WriteString( xmlHeader )
	fmt.Fprintf( builder , `<workbook xmlns=%q xmlns:r=%q><sheets>` , namespaceMain , namespaceRelationships )
	for index , name := range names {
		fmt.Fprintf( builder , `<sheet name="%s" sheetId="%d" r:id="rId%d"/>` , escape( name ) , index+1 , index+1 )
	}
	builder.WriteString( `</sheets></workbook>` )
	result = builder.String()
	return
}

// The sheets claim rId1..rIdN so that they line up with sheetId, which makes
// the two files readable side by side; styles takes the next number.
func workbookRelationships( sheetCount int ) ( result string ) {
	builder := &strings.Builder{}
	builder.WriteString( xmlHeader )
	fmt.Fprintf( builder , `<Relationships xmlns=%q>` , namespacePackageRels )
	for index := 1; index <= sheetCount; index += 1 {
		fmt.Fprintf( builder , `<Relationship Id="rId%d" Type="%s/worksheet" Target="worksheets/sheet%d.xml"/>` ,
			index , namespaceRelationships , index )
	}
	fmt.Fprintf( builder , `<Relationship Id="rId%d" Type="%s/styles" Target="styles.xml"/>` ,
		sheetCount+1 , namespaceRelationships )
	builder.WriteString( `</Relationships>` )
	result = builder.String()
	return
}

// styles.xml is mostly ceremony. Excel requires the first two fills to be
// "none" and "gray125" in that order whether or not anything uses them, and
// requires a border and a cellStyleXf to exist before any cellXf can refer to
// them. The four formats that actually matter are the cellXfs at the bottom,
// indexed by the style* constants at the top of this file.
func styles() ( result string ) {
	result = xmlHeader +
		`<styleSheet xmlns="` + namespaceMain + `">` +
		`<numFmts count="1"><numFmt numFmtId="164" formatCode="yyyy\-mm\-dd"/></numFmts>` +
		`<fonts count="2">` +
		`<font><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
		`<font><b/><sz val="11"/><name val="Calibri"/><family val="2"/></font>` +
		`</fonts>` +
		`<fills count="2">` +
		`<fill><patternFill patternType="none"/></fill>` +
		`<fill><patternFill patternType="gray125"/></fill>` +
		`</fills>` +
		`<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>` +
		`<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>` +
		`<cellXfs count="4">` +
		`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>` +
		`<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>` +
		`<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>` +
		`<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1">` +
		`<alignment vertical="top" wrapText="1"/></xf>` +
		`</cellXfs>` +
		`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>` +
		`</styleSheet>`
	return
}

// worksheet writes one sheet: a frozen header row, column widths, the data,
// and a filter over the lot.
//
// The order of the elements is not a matter of taste -- the schema fixes it
// (dimension, sheetViews, cols, sheetData, then autoFilter), and a reader
// that validates will refuse a file that puts them any other way.
func worksheet( sheet Sheet ) ( result string ) {
	width := len( sheet.Columns )
	lastColumn := columnName( maxInt( width-1 , 0 ) )
	lastRow := len( sheet.Rows ) + 1

	builder := &strings.Builder{}
	builder.WriteString( xmlHeader )
	fmt.Fprintf( builder , `<worksheet xmlns=%q>` , namespaceMain )
	fmt.Fprintf( builder , `<dimension ref="A1:%s%d"/>` , lastColumn , lastRow )

	// Freezing the header is the one piece of formatting worth the bytes: a
	// list of a few thousand words is unreadable once the column names have
	// scrolled away.
	builder.WriteString( `<sheetViews><sheetView workbookViewId="0">` +
		`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
		`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/>` +
		`</sheetView></sheetViews>` )

	if width > 0 {
		builder.WriteString( `<cols>` )
		for index , column := range sheet.Columns {
			size := column.Width
			if size <= 0 { size = 14 }
			fmt.Fprintf( builder , `<col min="%d" max="%d" width="%.2f" customWidth="1"/>` , index+1 , index+1 , size )
		}
		builder.WriteString( `</cols>` )
	}

	builder.WriteString( `<sheetData>` )

	builder.WriteString( `<row r="1">` )
	for index , column := range sheet.Columns {
		writeCell( builder , index , 1 , Text( column.Header ) , styleHeader )
	}
	builder.WriteString( `</row>` )

	for offset , row := range sheet.Rows {
		number := offset + 2
		fmt.Fprintf( builder , `<row r="%d">` , number )
		for index , cell := range row {
			if index >= width { break }
			writeCell( builder , index , number , cell , styleFor( cell , sheet.Columns[ index ] ) )
		}
		builder.WriteString( `</row>` )
	}

	builder.WriteString( `</sheetData>` )
	if width > 0 {
		fmt.Fprintf( builder , `<autoFilter ref="A1:%s%d"/>` , lastColumn , lastRow )
	}
	builder.WriteString( `</worksheet>` )
	result = builder.String()
	return
}

func styleFor( cell Cell , column Column ) ( style int ) {
	if cell.kind == kindDate { style = styleDate; return }
	if column.Wrap { style = styleWrapped }
	return
}

// writeCell emits one <c>. An empty cell is written with its style and no
// value rather than skipped, so a wrapped or date-formatted column keeps its
// formatting down the gaps.
func writeCell( builder *strings.Builder , column int , row int , cell Cell , style int ) {
	reference := columnName( column ) + strconv.Itoa( row )

	if cell.kind == kindEmpty {
		fmt.Fprintf( builder , `<c r="%s" s="%d"/>` , reference , style )
		return
	}

	if cell.kind == kindText {
		// xml:space="preserve" only where it is needed: without it a reader
		// is free to collapse leading and trailing whitespace, and with it on
		// every cell the file grows by a fifth for nothing. The test is made
		// against the stripped text, which is what ends up in the file --
		// Text() has already trimmed, so the only way to reach this is a
		// control character having been removed from an edge.
		text := stripInvalid( cell.text )
		space := ""
		if strings.TrimSpace( text ) != text { space = ` xml:space="preserve"` }
		fmt.Fprintf( builder , `<c r="%s" s="%d" t="inlineStr"><is><t%s>%s</t></is></c>` ,
			reference , style , space , escape( text ) )
		return
	}

	value := cell.number
	if cell.kind == kindDate { value = serialOf( cell.moment ) }
	fmt.Fprintf( builder , `<c r="%s" s="%d"><v>%s</v></c>` ,
		reference , style , strconv.FormatFloat( value , 'f' , -1 , 64 ) )
	return
}

func maxInt( a int , b int ) ( result int ) {
	result = a
	if b > a { result = b }
	return
}
