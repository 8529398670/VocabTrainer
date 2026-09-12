package xlsx

import (
	zip "archive/zip"
	bytes "bytes"
	io "io"
	strings "strings"
	testing "testing"
	time "time"
	xml "encoding/xml"
)

// partsOf unzips a workbook into a map so a test can look at one part
// without caring about the order they were written in.
func partsOf( t *testing.T , body []byte ) ( parts map[string]string ) {
	t.Helper()
	reader , err := zip.NewReader( bytes.NewReader( body ) , int64( len( body ) ) )
	if err != nil { t.Fatalf( "the workbook is not a readable zip: %v" , err ) }

	parts = map[string]string{}
	for _ , file := range reader.File {
		handle , open_err := file.Open()
		if open_err != nil { t.Fatalf( "opening %s: %v" , file.Name , open_err ) }
		contents , read_err := io.ReadAll( handle )
		handle.Close()
		if read_err != nil { t.Fatalf( "reading %s: %v" , file.Name , read_err ) }
		parts[ file.Name ] = string( contents )
	}
	return
}

func sample() ( sheets []Sheet ) {
	sheets = []Sheet{
		{
			Name: "Not known",
			Columns: []Column{
				{ Header: "Word" , Width: 20 },
				{ Header: "Level" , Width: 8 },
				{ Header: "Definition" , Width: 50 , Wrap: true },
				{ Header: "Last seen" , Width: 12 },
			},
			Rows: [][]Cell{
				{ Text( "obdurate" ) , Number( 13 ) , Text( "stubbornly refusing to change" ) , Date( time.Date( 2026 , 9 , 12 , 8 , 30 , 0 , 0 , time.UTC ) ) },
				{ Text( "cat" ) , Number( 1 ) , Text( "" ) , Date( time.Time{} ) },
			},
		},
		{ Name: "Known" , Columns: []Column{ { Header: "Word" } } },
	}
	return
}

// Every part has to be well-formed XML on its own. A single stray character
// makes a spreadsheet unopenable rather than one cell wrong, so this is the
// check that matters most.
func TestEveryPartIsWellFormedXML( t *testing.T ) {
	buffer := &bytes.Buffer{}
	if err := Write( buffer , sample() ); err != nil { t.Fatalf( "writing: %v" , err ) }

	parts := partsOf( t , buffer.Bytes() )
	if len( parts ) == 0 { t.Fatal( "the workbook is empty" ) }

	for name , body := range parts {
		decoder := xml.NewDecoder( strings.NewReader( body ) )
		for {
			_ , err := decoder.Token()
			if err == io.EOF { break }
			if err != nil { t.Errorf( "%s is not well-formed XML: %v" , name , err ) ; break }
		}
	}
}

// The parts a reader looks for by name, and the one relationship that tells
// it where to start.
func TestTheRequiredPartsArePresent( t *testing.T ) {
	buffer := &bytes.Buffer{}
	if err := Write( buffer , sample() ); err != nil { t.Fatalf( "writing: %v" , err ) }
	parts := partsOf( t , buffer.Bytes() )

	wanted := []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"xl/workbook.xml",
		"xl/_rels/workbook.xml.rels",
		"xl/styles.xml",
		"xl/worksheets/sheet1.xml",
		"xl/worksheets/sheet2.xml",
	}
	for _ , name := range wanted {
		if _ , found := parts[ name ]; found == false { t.Errorf( "%s is missing" , name ) }
	}

	// One <Override> per sheet, or a reader is told a part it can see is not
	// a worksheet.
	if strings.Count( parts[ "[Content_Types].xml" ] , "/xl/worksheets/" ) != 2 {
		t.Errorf( "content types does not describe both sheets:\n%s" , parts[ "[Content_Types].xml" ] )
	}
}

func TestCellsCarryTheirType( t *testing.T ) {
	buffer := &bytes.Buffer{}
	if err := Write( buffer , sample() ); err != nil { t.Fatalf( "writing: %v" , err ) }
	sheet := partsOf( t , buffer.Bytes() )[ "xl/worksheets/sheet1.xml" ]

	// Text is inline, so there is no shared string table to keep in step.
	if strings.Contains( sheet , `<is><t>obdurate</t></is>` ) == false {
		t.Errorf( "the word is not written as an inline string:\n%s" , sheet )
	}
	// A number is a number, not the characters "13" -- otherwise the column
	// sorts alphabetically and 13 lands between 1 and 2.
	if strings.Contains( sheet , `t="inlineStr"><is><t>13</t>` ) {
		t.Errorf( "the level was written as text:\n%s" , sheet )
	}
	// 2026-09-12 08:30 UTC as an Excel serial: whole days since 1899-12-30,
	// plus the half-ish day.
	if strings.Contains( sheet , `<v>46277.354166666664</v>` ) == false {
		t.Errorf( "the date is not an Excel serial:\n%s" , sheet )
	}
	// A zero time is "never", which is a blank cell rather than a date in
	// 1899 or 1970.
	if strings.Contains( sheet , "1899" ) || strings.Contains( sheet , "<v>0</v>" ) {
		t.Errorf( "a zero time was written as a date:\n%s" , sheet )
	}
}

// A definition can contain anything WordNet does, and a sheet name can
// contain anything language.yaml does. Neither may be able to end a tag.
func TestTextIsEscaped( t *testing.T ) {
	sheets := []Sheet{ {
		Name:    `Not "known" & <fine>`,
		Columns: []Column{ { Header: "Word" } , { Header: "Definition" } },
		Rows:    [][]Cell{ { Text( `a<b & c"d` ) , Text( "keeps\x07going" ) } },
	} }

	buffer := &bytes.Buffer{}
	if err := Write( buffer , sheets ); err != nil { t.Fatalf( "writing: %v" , err ) }
	parts := partsOf( t , buffer.Bytes() )

	if strings.Contains( parts[ "xl/worksheets/sheet1.xml" ] , `a&lt;b &amp; c&#34;d` ) == false {
		t.Errorf( "the definition was not escaped:\n%s" , parts[ "xl/worksheets/sheet1.xml" ] )
	}
	// A bell character is not representable in XML 1.0 at all, so it is
	// dropped rather than written and left to break the whole file.
	if strings.Contains( parts[ "xl/worksheets/sheet1.xml" ] , "\x07" ) {
		t.Error( "a control character survived into the sheet" )
	}
	// Dropping one from an edge can expose a space that Text()'s trim did not
	// see, and a leading space a reader is free to discard is a changed cell.
	preserving := []Sheet{ {
		Name:    "Edges",
		Columns: []Column{ { Header: "Word" } },
		Rows:    [][]Cell{ { Text( " \x07 a" ) } },
	} }
	edge := &bytes.Buffer{}
	if err := Write( edge , preserving ); err != nil { t.Fatalf( "writing: %v" , err ) }
	sheet := partsOf( t , edge.Bytes() )[ "xl/worksheets/sheet1.xml" ]
	if strings.Contains( sheet , `<t xml:space="preserve"> a</t>` ) == false {
		t.Errorf( "the exposed leading space was left for a reader to collapse:\n%s" , sheet )
	}
	if strings.Contains( parts[ "xl/workbook.xml" ] , `&amp;` ) == false {
		t.Errorf( "the sheet name was not escaped:\n%s" , parts[ "xl/workbook.xml" ] )
	}
}

func TestSheetNamesAreMadeLegal( t *testing.T ) {
	long := strings.Repeat( "word " , 12 )
	sheets := []Sheet{
		{ Name: "a/b:c[d]" , Columns: []Column{ { Header: "Word" } } },
		{ Name: long , Columns: []Column{ { Header: "Word" } } },
		{ Name: "" , Columns: []Column{ { Header: "Word" } } },
		// Two sheets Excel would see as the same name is a corrupt file, not
		// a naming clash, so the second is moved out of the way.
		{ Name: "a b c d" , Columns: []Column{ { Header: "Word" } } },
	}

	buffer := &bytes.Buffer{}
	if err := Write( buffer , sheets ); err != nil { t.Fatalf( "writing: %v" , err ) }
	workbook := partsOf( t , buffer.Bytes() )[ "xl/workbook.xml" ]

	for _ , banned := range []string{ "/" , ":" , "[" , "]" } {
		if strings.Contains( workbook , `name="a`+banned ) {
			t.Errorf( "%q survived in a sheet name:\n%s" , banned , workbook )
		}
	}
	if strings.Contains( workbook , `name="Sheet3"` ) == false {
		t.Errorf( "an empty sheet name was not given a fallback:\n%s" , workbook )
	}
	if strings.Contains( workbook , `name="a b c d 2"` ) == false {
		t.Errorf( "the duplicate name was not resolved:\n%s" , workbook )
	}
	for _ , name := range namesIn( workbook ) {
		if len( []rune( name ) ) > 31 { t.Errorf( "sheet name %q is longer than Excel allows" , name ) }
	}
}

func namesIn( workbook string ) ( names []string ) {
	for _ , piece := range strings.Split( workbook , `<sheet name="` )[ 1: ] {
		names = append( names , piece[ :strings.Index( piece , `"` ) ] )
	}
	return
}

func TestAWorkbookNeedsASheet( t *testing.T ) {
	if err := Write( &bytes.Buffer{} , nil ); err == nil {
		t.Error( "a workbook with no sheets was written rather than refused" )
	}
}

func TestColumnNames( t *testing.T ) {
	cases := map[int]string{ 0: "A" , 25: "Z" , 26: "AA" , 27: "AB" , 51: "AZ" , 52: "BA" , 701: "ZZ" , 702: "AAA" }
	for index , want := range cases {
		if got := columnName( index ); got != want {
			t.Errorf( "column %d = %q, wanted %q" , index , got , want )
		}
	}
}
