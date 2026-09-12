// Downloading your own words as a spreadsheet.
//
// The three lists the app is built around -- not known, known, skipped --
// come out as three sheets of one .xlsx file. It is a plain authenticated GET
// so the browser can fetch it as a normal download rather than through
// JavaScript: a link with Content-Disposition works on every phone, and a
// blob assembled in the page does not.
//
// Every heading here comes from language.yaml like the rest of the UI, and
// the same rule applies: a key that resolves to "" removes what it names. An
// empty column heading drops that column from the file; an empty sheet name
// drops the whole sheet. That is how you cut the export down to a word list
// and nothing else without touching this file.
package routes

import (
	bytes "bytes"
	fmt "fmt"
	sort "sort"
	strconv "strconv"
	strings "strings"
	time "time"

	fiber "github.com/gofiber/fiber/v3"

	corpus "vocabtrainer/server/corpus"
	models "vocabtrainer/server/models"
	security "vocabtrainer/server/security"
	xlsx "vocabtrainer/server/xlsx"
)

// The MIME type Excel, Numbers, LibreOffice and Google Sheets all recognise.
// Anything vaguer -- application/octet-stream -- makes a phone offer to save
// a file nothing will open.
const spreadsheetMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// exportRow is one word on its way into a cell: the corpus entry for the
// text, the card for the history, and the level's display name resolved once
// so the column function does not need the language file.
type exportRow struct {
	word      *corpus.Word
	card      *models.Card
	levelName string
}

func ( row exportRow ) sense() ( sense corpus.Sense ) {
	if len( row.word.Senses ) > 0 { sense = row.word.Senses[ 0 ] }
	return
}

// exportColumn describes one column of every sheet. Keeping them in a table
// rather than spelled out per sheet is what makes "drop the column whose
// heading is empty" a single filter instead of a rule repeated three times.
type exportColumn struct {
	key   string
	width float64
	wrap  bool
	cell  func( row exportRow ) xlsx.Cell
}

// The columns, in the order they appear in the file. Only the primary sense
// is exported: a word with nine senses would otherwise turn one row into a
// paragraph, and the primary sense is what the app itself shows in a list.
var exportColumns = []exportColumn{
	{ key: "export.column_word" , width: 22 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Text( row.word.Word )
	} },
	// The number and the name are separate columns because they answer
	// different questions: the number sorts, and "Grade 10" next to "Grade 2"
	// does not. Delete either one in language.yaml if you only want the other.
	{ key: "export.column_level" , width: 8 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Number( float64( row.word.Tier ) )
	} },
	{ key: "export.column_level_name" , width: 16 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Text( row.levelName )
	} },
	{ key: "export.column_pos" , width: 14 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Text( row.sense().Pos )
	} },
	{ key: "export.column_definition" , width: 58 , wrap: true , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Text( row.sense().Definition )
	} },
	{ key: "export.column_example" , width: 44 , wrap: true , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Text( row.sense().Example )
	} },
	{ key: "export.column_lapses" , width: 13 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Number( float64( row.card.Lapses ) )
	} },
	{ key: "export.column_reviews" , width: 10 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Number( float64( row.card.Reps ) )
	} },
	{ key: "export.column_first_seen" , width: 12 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Date( row.card.FirstSeenAt )
	} },
	{ key: "export.column_last_seen" , width: 12 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Date( row.card.LastSeenAt )
	} },
	{ key: "export.column_due" , width: 12 , cell: func( row exportRow ) xlsx.Cell {
		return xlsx.Date( row.card.DueAt )
	} },
}

// The sheets, in the order the app lists them. Not known comes first because
// it is the list people act on.
var exportSheets = []struct {
	key    string
	status string
}{
	{ key: "export.sheet_unknown" , status: models.StatusUnknown },
	{ key: "export.sheet_known" , status: models.StatusKnown },
	{ key: "export.sheet_skipped" , status: models.StatusSkipped },
}

// GetWordsExport builds the workbook and sends it as a download.
func ( handlers *Handlers ) GetWordsExport( c fiber.Ctx ) ( err error ) {
	user := security.UserFrom( c )
	now := time.Now().UTC()

	cards , err := models.ListCards( handlers.Store , user.ID )
	if err != nil { return serverError( c ) }

	// One pass, three buckets. ListCardsByStatus would re-scan the user's
	// whole range for each list, and the whole point of this endpoint is that
	// it touches every card exactly once.
	byStatus := map[string][]*models.Card{}
	for _ , card := range cards {
		byStatus[ card.Status ] = append( byStatus[ card.Status ] , card )
	}

	columns := []exportColumn{}
	headers := []xlsx.Column{}
	for _ , column := range exportColumns {
		heading := handlers.Language.Get( column.key )
		if heading == "" { continue }
		columns = append( columns , column )
		headers = append( headers , xlsx.Column{ Header: heading , Width: column.width , Wrap: column.wrap } )
	}

	sheets := []xlsx.Sheet{}
	for _ , definition := range exportSheets {
		name := handlers.Language.Get( definition.key )
		if name == "" { continue }
		sheets = append( sheets , xlsx.Sheet{
			Name:    name,
			Columns: headers,
			Rows:    handlers.exportRows( byStatus[ definition.status ] , columns ),
		} )
	}

	// Everything can be switched off from language.yaml, and this is what
	// that looks like from here. Saying so is better than shipping an empty
	// file that no reader will open.
	if len( sheets ) == 0 || len( columns ) == 0 {
		return notFound( c , "the export has no sheets or no columns configured" )
	}

	buffer := &bytes.Buffer{}
	if err = xlsx.Write( buffer , sheets ); err != nil { return serverError( c ) }

	c.Set( fiber.HeaderContentType , spreadsheetMIME )
	c.Set( fiber.HeaderCacheControl , "no-store" )
	c.Set( fiber.HeaderContentDisposition ,
		`attachment; filename="`+handlers.exportFilename( localDate( c.Query( "date" ) , now ) )+`"` )
	err = c.Send( buffer.Bytes() )
	return
}

// exportRows turns one list of cards into spreadsheet rows.
//
// Alphabetical, unlike the list screens, which show the most recent first.
// A file someone opens once and reads down is a different object from a
// screen they act on, and the sheet carries a filter row for anyone who wants
// it back in date order.
func ( handlers *Handlers ) exportRows( cards []*models.Card , columns []exportColumn ) ( rows [][]xlsx.Cell ) {
	rows = [][]xlsx.Cell{}

	ordered := make( []*models.Card , len( cards ) )
	copy( ordered , cards )
	sort.Slice( ordered , func( a int , b int ) bool { return ordered[ a ].Word < ordered[ b ].Word } )

	for _ , card := range ordered {
		word , found := handlers.Corpus.Lookup( card.Word )
		// The word left the corpus in a rebuild, exactly as in GetCards: the
		// record survives, but there is nothing to write a definition from.
		if found == false { continue }

		row := exportRow{
			word:      word,
			card:      card,
			levelName: handlers.Language.Get( "levels.tier_" + strconv.Itoa( word.Tier ) ),
		}
		cells := make( []xlsx.Cell , 0 , len( columns ) )
		for _ , column := range columns {
			cells = append( cells , column.cell( row ) )
		}
		rows = append( rows , cells )
	}
	return
}

// filenameSafe keeps the download name to characters every filesystem and
// every browser agrees on. A Content-Disposition name is quoted, not escaped,
// so a quote or a semicolon in it is not a cosmetic problem -- it ends the
// header early.
func filenameSafe( value string ) ( result string ) {
	result = strings.Map( func( r rune ) ( kept rune ) {
		switch {
		case r >= 'a' && r <= 'z' , r >= 'A' && r <= 'Z' , r >= '0' && r <= '9':
			kept = r
		case r == '-' || r == '_' || r == '.':
			kept = r
		default:
			kept = '-'
		}
		return
	} , value )
	result = strings.Trim( result , "-." )
	return
}

func ( handlers *Handlers ) exportFilename( date string ) ( result string ) {
	base := filenameSafe( handlers.Language.Get( "export.filename" ) )
	if base == "" { base = "words" }
	result = fmt.Sprintf( "%s-%s.xlsx" , base , filenameSafe( date ) )
	return
}
