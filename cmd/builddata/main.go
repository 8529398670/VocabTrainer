// Command builddata turns the raw linguistic sources in ./corpus-sources into
// the single compressed word list the server embeds.
//
// It is a build-time tool, not part of the server. Run it after
// scripts/fetch-corpus-sources.sh and commit the result; nobody needs the
// 40MB of raw sources to build or run the app.
//
// The interesting part is estimateTier: how three independent signals get
// turned into one reading-grade band. See the comment there before changing
// any constant -- the numbers are calibrated against each other, not
// independently meaningful.
package main

import (
	bufio "bufio"
	gzip "compress/gzip"
	json "encoding/json"
	flag "flag"
	fmt "fmt"
	log "log"
	math "math"
	os "os"
	filepath "path/filepath"
	regexp "regexp"
	sort "sort"
	strconv "strconv"
	strings "strings"
)

// ---------------------------------------------------------------------------
// the record we emit
// ---------------------------------------------------------------------------

// Sense is one dictionary meaning. Field names are one letter because this
// file is written ~60,000 times and the keys would otherwise be most of it.
type Sense struct {
	Pos        string `json:"p"`
	Definition string `json:"d"`
	Example    string `json:"x,omitempty"`

	// lexFile is WordNet's semantic category for this sense. It never
	// reaches the output file -- it exists so estimateTier can tell a rare
	// abstract word from a rare species name.
	lexFile int `json:"-"`
}

type Word struct {
	Word   string  `json:"w"`
	Tier   int     `json:"t"`
	Senses []Sense `json:"s"`
	Zipf   float64 `json:"z"`
	AoA    float64 `json:"a,omitempty"`

	// score is the continuous difficulty estimate the tier is rounded from.
	// It stays out of the output file but is needed to rank the advanced
	// band -- see assignAdvancedTiers.
	score float64 `json:"-"`
}

// advancedFloor is where the calibrated part of the scale runs out.
//
// Grades 1-12 are anchored to something real: age-of-acquisition ratings say
// when people report learning a word, and a grade is an age. Past school
// there is no equivalent anchor -- no dataset says which words are
// "doctoral" -- so claiming an absolute measure up there would be inventing
// precision we do not have.
//
// What the signals genuinely support above grade 12 is an ordering. So the
// words scoring past this floor are ranked and cut into three equal bands:
// undergraduate, graduate, doctoral. The claim is "these are the hardest
// words this corpus has, hardest third last", which is true, rather than
// "this word is doctoral", which would not be.
const advancedFloor = 12.5

func assignAdvancedTiers( words []Word ) {
	advanced := []int{}
	for index := range words {
		if words[ index ].score >= advancedFloor { advanced = append( advanced , index ) }
	}
	sort.Slice( advanced , func( a int , b int ) bool {
		return words[ advanced[ a ] ].score < words[ advanced[ b ] ].score
	} )
	if len( advanced ) == 0 { return }
	band := len( advanced ) / 3
	for rank , index := range advanced {
		switch {
		case band > 0 && rank < band:
			words[ index ].Tier = 13
		case band > 0 && rank < band*2:
			words[ index ].Tier = 14
		default:
			words[ index ].Tier = 15
		}
	}
}

// ---------------------------------------------------------------------------
// WordNet
// ---------------------------------------------------------------------------

// posNames maps WordNet's synset type codes. "s" is an adjective satellite --
// a shade of meaning hanging off a head adjective -- which is an adjective as
// far as a learner is concerned.
var posNames = map[string]string{
	"n": "noun",
	"v": "verb",
	"a": "adjective",
	"s": "adjective",
	"r": "adverb",
}

type synset struct {
	pos        string
	lexFile    int
	words      []string
	definition string
	example    string
}

var adjectiveMarker = regexp.MustCompile( `\((a|p|ip)\)$` )

// parseDataFile reads one of data.noun / data.verb / data.adj / data.adv.
//
// Line shape, space separated:
//
//	offset lex_filenum ss_type w_cnt word lex_id [word lex_id...] p_cnt [pointers...] | gloss
//
// w_cnt is two hex digits, which is the one field that will silently produce
// nonsense if read as decimal.
func parseDataFile( path string , into map[string]*synset ) ( err error ) {
	handle , err := os.Open( path )
	if err != nil { return }
	defer handle.Close()

	scanner := bufio.NewScanner( handle )
	scanner.Buffer( make( []byte , 1024*1024 ) , 1024*1024 )
	for scanner.Scan() {
		line := scanner.Text()
		// The copyright header is the only thing indented by two spaces.
		if strings.HasPrefix( line , "  " ) { continue }

		bar := strings.Index( line , " | " )
		if bar < 0 { continue }
		head := strings.Fields( line[ :bar ] )
		gloss := line[ bar+3: ]
		if len( head ) < 4 { continue }

		offset , ss_type := head[ 0 ] , head[ 2 ]
		pos , known := posNames[ ss_type ]
		if known == false { continue }

		lex_file , lex_err := strconv.Atoi( head[ 1 ] )
		if lex_err != nil { lex_file = -1 }

		word_count , parse_err := strconv.ParseInt( head[ 3 ] , 16 , 32 )
		if parse_err != nil { continue }

		words := []string{}
		for index := 0; index < int( word_count ); index += 1 {
			at := 4 + index*2
			if at >= len( head ) { break }
			words = append( words , adjectiveMarker.ReplaceAllString( head[ at ] , "" ) )
		}
		if len( words ) == 0 { continue }

		definition , example := splitGloss( gloss )
		if definition == "" { continue }

		into[ ss_type + offset ] = &synset{
			pos:        pos,
			lexFile:    lex_file,
			words:      words,
			definition: definition,
			example:    example,
		}
	}
	err = scanner.Err()
	return
}

// splitGloss separates the definition from WordNet's usage examples. The
// examples are the quoted runs at the end; everything before the first one is
// the definition (which may itself contain semicolons separating near-synonym
// readings, and those are worth keeping).
func splitGloss( gloss string ) ( definition string , example string ) {
	quote := strings.Index( gloss , "\"" )
	if quote < 0 {
		definition = strings.TrimSpace( strings.Trim( strings.TrimSpace( gloss ) , ";" ) )
		return
	}
	definition = strings.TrimSpace( strings.Trim( strings.TrimSpace( gloss[ :quote ] ) , ";" ) )
	rest := gloss[ quote: ]
	if end := strings.Index( rest[ 1: ] , "\"" ); end >= 0 {
		example = strings.TrimSpace( rest[ 1 : 1+end ] )
	}
	return
}

type indexEntry struct {
	lemma   string
	pos     string
	offsets []string
}

// parseIndexFile reads index.noun and friends. The synset offsets are listed
// in sense order, and WordNet orders senses by tagged frequency -- so the
// first offset is the meaning a learner is most likely to want. That ordering
// is the whole reason we read the index files instead of just the data files.
//
//	lemma pos synset_cnt p_cnt [ptr_symbol...] sense_cnt tagsense_cnt offset...
func parseIndexFile( path string ) ( entries []indexEntry , err error ) {
	handle , err := os.Open( path )
	if err != nil { return }
	defer handle.Close()

	scanner := bufio.NewScanner( handle )
	scanner.Buffer( make( []byte , 1024*1024 ) , 1024*1024 )
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix( line , "  " ) { continue }
		fields := strings.Fields( line )
		if len( fields ) < 7 { continue }

		lemma , pos := fields[ 0 ] , fields[ 1 ]
		synset_count , err_a := strconv.Atoi( fields[ 2 ] )
		pointer_count , err_b := strconv.Atoi( fields[ 3 ] )
		if err_a != nil || err_b != nil { continue }

		// Skip the pointer symbols, then sense_cnt and tagsense_cnt.
		at := 4 + pointer_count + 2
		if at+synset_count > len( fields ) { continue }

		entries = append( entries , indexEntry{
			lemma:   lemma,
			pos:     pos,
			offsets: fields[ at : at+synset_count ],
		} )
	}
	err = scanner.Err()
	return
}

// ---------------------------------------------------------------------------
// frequency
// ---------------------------------------------------------------------------

// readCounts handles both source formats: the subtitle list is space
// separated, the web list is tab separated.
func readCounts( path string ) ( counts map[string]float64 , total float64 , err error ) {
	counts = map[string]float64{}
	handle , err := os.Open( path )
	if err != nil { return }
	defer handle.Close()

	scanner := bufio.NewScanner( handle )
	scanner.Buffer( make( []byte , 1024*1024 ) , 1024*1024 )
	for scanner.Scan() {
		fields := strings.Fields( scanner.Text() )
		if len( fields ) != 2 { continue }
		word := strings.ToLower( fields[ 0 ] )
		count , parse_err := strconv.ParseFloat( fields[ 1 ] , 64 )
		if parse_err != nil || count <= 0 { continue }
		counts[ word ] += count
		total += count
	}
	err = scanner.Err()
	return
}

type aoaEntry struct {
	years          float64
	knownFraction  float64
}

func readAoA( path string ) ( entries map[string]aoaEntry , err error ) {
	entries = map[string]aoaEntry{}
	handle , err := os.Open( path )
	if err != nil { return }
	defer handle.Close()

	scanner := bufio.NewScanner( handle )
	first := true
	for scanner.Scan() {
		if first { first = false; continue } // header
		fields := strings.Split( scanner.Text() , "\t" )
		if len( fields ) < 3 { continue }
		years , err_a := strconv.ParseFloat( fields[ 1 ] , 64 )
		known , err_b := strconv.ParseFloat( fields[ 2 ] , 64 )
		if err_a != nil { continue }
		if err_b != nil { known = 1 }
		entries[ strings.ToLower( fields[ 0 ] ) ] = aoaEntry{ years: years , knownFraction: known }
	}
	err = scanner.Err()
	return
}

// ---------------------------------------------------------------------------
// difficulty
// ---------------------------------------------------------------------------

const (
	// TierMin and TierMax bracket the reading bands: 1..12 are US school
	// grades, 13 undergraduate, 14 graduate, 15 doctoral.
	TierMin = 1
	TierMax = 15

	// schoolStartAge is the age at which grade 1 begins, so a word acquired
	// at age 6 sits at grade 1. Kuperman's ratings are in years.
	schoolStartAge = 5.0

	// zipfAtGradeOne is the Zipf value a grade-1 word sits at, and
	// zipfPerGrade how much rarer each further grade is. Zipf is
	// log10(occurrences per billion words), so a step of one covers a
	// tenfold drop in frequency -- which is why a grade costs well under a
	// whole point.
	zipfAtGradeOne = 5.6
	zipfPerGrade   = 1.0 / 3.2
)

// academicSuffixes mark the Greek/Latin word-formation that shows up almost
// only in technical registers. This is a small nudge, not a classifier: the
// frequency data does the real work and this keeps "photosynthesis" from
// landing next to "pancake" because both are moderately rare in subtitles.
var academicSuffixes = []string{
	"ology" , "ography" , "ographic" , "escence" , "aceous" , "iform" ,
	"itude" , "ivity" , "genesis" , "morphic" , "otropic" , "emia" ,
	"itis" , "osis" , "istic" , "ational" , "ically" , "ferous" ,
}

func morphologyBonus( word string ) ( bonus float64 ) {
	for _ , suffix := range academicSuffixes {
		if strings.HasSuffix( word , suffix ) {
			bonus += 0.8
			break
		}
	}
	if len( word ) >= 13 { bonus += 0.4 }
	return
}

// WordNet groups every synset into a "lexicographer file" by broad semantic
// category. Two of those groupings matter here.
//
// taxonomicLexFiles are the concrete inventories -- animals, plants, foods,
// artifacts, substances, places. They are full of words that are extremely
// rare and yet completely undemanding: knowing "hogfish" or "overshoe" is
// trivia, not reading level. Frequency alone cannot see the difference, and
// without this correction the hardest tiers fill up with species names while
// the words that actually mark advanced reading sit several bands lower.
var taxonomicLexFiles = map[int]bool{
	5: true ,  // noun.animal
	6: true ,  // noun.artifact
	8: true ,  // noun.body
	13: true , // noun.food
	15: true , // noun.location
	17: true , // noun.object
	20: true , // noun.plant
	27: true , // noun.substance
}

// abstractLexFiles are the categories where rarity really does track
// difficulty: ideas, attributes, relations, states. Adjectives, adverbs and
// verbs behave the same way and are treated as abstract regardless of file.
var abstractLexFiles = map[int]bool{
	4: true ,  // noun.act
	7: true ,  // noun.attribute
	9: true ,  // noun.cognition
	10: true , // noun.communication
	11: true , // noun.event
	12: true , // noun.feeling
	16: true , // noun.motive
	19: true , // noun.phenomenon
	22: true , // noun.process
	24: true , // noun.relation
	26: true , // noun.state
}

// estimateTier folds four signals into one reading grade.
//
//   - Age of acquisition (Kuperman et al.) is the most direct evidence we
//     have for the school grades, because it is literally "when do people
//     report learning this word". It runs out of resolution at the top: very
//     few words are rated above 18, so it cannot separate undergraduate from
//     doctoral vocabulary on its own.
//
//   - Frequency covers the whole range and takes over where AoA stops. The
//     two corpora are averaged as counts rather than as Zipf values, because
//     the question is total exposure -- a word common in one register is one
//     a learner has plausibly met.
//
//   - Register -- how much more common a word is in written text than in
//     speech -- separates "hypothesis" from "hamburger" when both are equally
//     rare on screen. It only applies where both corpora have the word, since
//     a zero in one of them means "unknown", not "never written".
//
//   - Semantic category decides whether rarity should count at all. See
//     taxonomicLexFiles above: this is what keeps the doctoral band full of
//     words like "epistemology" instead of words like "lapwing".
//
// knownFraction corrects a survivorship bias in the AoA data: a rating is the
// average over people who knew the word, so a word only 40% of raters
// recognised has an AoA that describes those 40% and understates its
// difficulty for everyone else.
func estimateTier( word string , lexFile int , zipfEffective float64 , zipfSub float64 , zipfWeb float64 , aoa aoaEntry , hasAoA bool ) ( tier int , score float64 ) {
	gradeFromZipf := ( zipfAtGradeOne - zipfEffective ) / zipfPerGrade + 1

	if hasAoA {
		gradeFromAoA := aoa.years - schoolStartAge
		score = 0.62*gradeFromAoA + 0.38*gradeFromZipf
		score += ( 1 - aoa.knownFraction ) * 4.0
	} else {
		score = gradeFromZipf
	}

	register := 0.0
	if zipfSub > 0 && zipfWeb > 0 {
		register = zipfWeb - zipfSub
		if register > 0 {
			score += math.Min( register , 2.5 ) * 0.6
		}
	}

	score += morphologyBonus( word )

	// Rarity means different things for different kinds of word, and this is
	// where that gets applied. A rare concrete noun is capped at the end of
	// school: no amount of obscurity makes a fish species a doctoral word.
	// A rare abstract word gets the opposite treatment -- above grade 10 its
	// rarity is stretched, because that is exactly the range where an unusual
	// idea-word is what separates undergraduate from doctoral reading.
	isTaxonomic := taxonomicLexFiles[ lexFile ]
	isAbstract := abstractLexFiles[ lexFile ] || lexFile < 3 || lexFile >= 29 || lexFile == 44

	// Both adjustments compress or stretch rather than clamping. A hard cap
	// looks reasonable in isolation and then piles every capped word onto one
	// tier, which shows up in the histogram as a spike and in the app as a
	// level full of unrelated words.
	switch {
	case isTaxonomic && register < 1.0:
		if score > 9 { score = 9 + ( score-9 )*0.35 }
	case isAbstract && score > 10:
		score += ( score - 10 ) * 0.45
	}

	tier = int( math.Round( score ) )
	if tier < TierMin { tier = TierMin }
	if tier > TierMax { tier = TierMax }
	return
}

// ---------------------------------------------------------------------------
// filtering
// ---------------------------------------------------------------------------

var validLemma = regexp.MustCompile( `^[a-z]+(-[a-z]+)*$` )

// unsuitableGloss keeps slurs and sexual vocabulary out of a trainer whose
// easiest setting is aimed at six-year-olds. WordNet labels these in the
// gloss itself, which makes the filter a text match rather than a word list
// we would have to maintain.
var unsuitableGloss = []string{
	"offensive term" , "disparaging" , "derogatory" , "ethnic slur" ,
	"racial slur" , "vulgar" , "obscene" , "insulting term" ,
	"term of disparagement" , "taboo" , "offensive names" , "insulting names" ,
	"offensive name" , "term of abuse" , "slang for" ,
}

// numberGloss catches spelled-out numerals and roman numerals. WordNet has an
// entry for every one of them into the hundreds, and they are arithmetic
// rather than vocabulary -- nobody needs "forty-nine" or "lxxv" on a
// flashcard.
//
// Matching the gloss rather than the spelling is what makes this safe. The
// obvious alternative, a roman-numeral regex over the lemma, also matches
// "mix" -- which is a perfectly good word whose definition is about mixtures,
// so it survives here and the numerals do not.
var numberGloss = regexp.MustCompile(
	`^(the cardinal number|the ordinal number|a cardinal number|the number that|being \w+ more than|denoting a quantity)` )

// vulgarLemmas are the handful of words WordNet does not tag in the gloss but
// that have no place in a trainer whose first level is aimed at six-year-olds.
var vulgarLemmas = map[string]bool{
	"whoreson": true , "whore": true , "slut": true , "bastardly": true ,
	"cocksucker": true , "motherfucker": true , "fucker": true , "fuck": true ,
	"shit": true , "cunt": true , "twat": true , "wanker": true ,
}

func glossIsUnsuitable( text string ) ( result bool ) {
	lower := strings.ToLower( text )
	for _ , marker := range unsuitableGloss {
		if strings.Contains( lower , marker ) {
			result = true
			return
		}
	}
	result = numberGloss.MatchString( lower )
	return
}

// ---------------------------------------------------------------------------

func main() {
	sources := flag.String( "sources" , "./corpus-sources" , "directory holding the raw downloads" )
	output := flag.String( "out" , "./server/corpus/data/words.jsonl.gz" , "where to write the generated corpus" )
	maxSenses := flag.Int( "max-senses" , 3 , "how many WordNet senses to keep per word" )
	flag.Parse()

	log.SetFlags( 0 )

	log.Println( "reading frequency corpora" )
	subCounts , subTotal , err := readCounts( filepath.Join( *sources , "freq_subtitles.txt" ) )
	if err != nil { log.Fatalf( "subtitle frequencies: %v" , err ) }
	webCounts , webTotal , err := readCounts( filepath.Join( *sources , "freq_web.txt" ) )
	if err != nil { log.Fatalf( "web frequencies: %v" , err ) }
	log.Printf( "  subtitles %d types / %.0f tokens" , len( subCounts ) , subTotal )
	log.Printf( "  web       %d types / %.0f tokens" , len( webCounts ) , webTotal )

	log.Println( "reading age-of-acquisition ratings" )
	aoaByWord , err := readAoA( filepath.Join( *sources , "aoa.tsv" ) )
	if err != nil { log.Fatalf( "aoa: %v" , err ) }
	log.Printf( "  %d rated words" , len( aoaByWord ) )

	log.Println( "reading WordNet" )
	dict := filepath.Join( *sources , "dict" )
	synsets := map[string]*synset{}
	for _ , name := range []string{ "data.noun" , "data.verb" , "data.adj" , "data.adv" } {
		if err = parseDataFile( filepath.Join( dict , name ) , synsets ); err != nil {
			log.Fatalf( "%s: %v" , name , err )
		}
	}
	log.Printf( "  %d synsets" , len( synsets ) )

	// A lemma's senses are gathered across all four parts of speech, keeping
	// the per-POS ordering WordNet gives us.
	type candidate struct {
		lemma  string
		senses []Sense
		// anyLowercase is false when every sense spells the word with a
		// capital -- a proper noun the index file happened to lowercase.
		anyLowercase bool
	}
	candidates := map[string]*candidate{}

	for _, name := range []string{ "index.noun" , "index.verb" , "index.adj" , "index.adv" } {
		entries , index_err := parseIndexFile( filepath.Join( dict , name ) )
		if index_err != nil { log.Fatalf( "%s: %v" , name , index_err ) }
		for _ , entry := range entries {
			lemma := strings.ToLower( entry.lemma )
			if validLemma.MatchString( lemma ) == false { continue }
			if len( lemma ) < 3 || len( lemma ) > 20 { continue }
			if vulgarLemmas[ lemma ] { continue }

			item := candidates[ lemma ]
			if item == nil {
				item = &candidate{ lemma: lemma }
				candidates[ lemma ] = item
			}
			for _ , offset := range entry.offsets {
				found := synsets[ entry.pos + offset ]
				// Adjective satellites live in data.adj under type "s", but
				// index.adj refers to them with pos "a".
				if found == nil && entry.pos == "a" { found = synsets[ "s" + offset ] }
				if found == nil { continue }
				if glossIsUnsuitable( found.definition ) { continue }

				for _ , spelling := range found.words {
					if strings.ToLower( spelling ) == lemma && spelling == lemma {
						item.anyLowercase = true
					}
				}
				item.senses = append( item.senses , Sense{
					Pos:        found.pos,
					Definition: found.definition,
					Example:    found.example,
					lexFile:    found.lexFile,
				} )
			}
		}
	}
	log.Printf( "  %d candidate lemmas" , len( candidates ) )

	// Per-billion rates, so the two corpora are comparable despite very
	// different sizes.
	zipfOf := func( count float64 , total float64 ) ( result float64 ) {
		if count <= 0 || total <= 0 { return }
		result = math.Log10( count / total * 1e9 )
		return
	}

	words := []Word{}
	dropped := map[string]int{}

	for lemma , item := range candidates {
		if len( item.senses ) == 0 { dropped[ "no usable sense" ] += 1; continue }
		if item.anyLowercase == false { dropped[ "proper noun" ] += 1; continue }

		subCount , webCount := subCounts[ lemma ] , webCounts[ lemma ]
		aoa , hasAoA := aoaByWord[ lemma ]

		// Inclusion gate. A word earns its place by being rated in the AoA
		// study or by actually occurring often enough in one of the corpora
		// to be worth a learner's time. Without this, WordNet's taxonomic
		// tail (every genus, every mineral) floods the hardest tiers.
		if hasAoA == false && subCount < 60 && zipfOf( webCount , webTotal ) < 1.3 {
			dropped[ "too rare to be useful" ] += 1
			continue
		}

		zipfSub := zipfOf( subCount , subTotal )
		zipfWeb := zipfOf( webCount , webTotal )
		combined := 0.0
		if subTotal > 0 { combined += subCount / subTotal * 1e9 * 0.5 }
		if webTotal > 0 { combined += webCount / webTotal * 1e9 * 0.5 }
		zipfEffective := 0.0
		if combined > 0 { zipfEffective = math.Log10( combined ) }

		tier , score := estimateTier( lemma , item.senses[ 0 ].lexFile , zipfEffective , zipfSub , zipfWeb , aoa , hasAoA )

		senses := item.senses
		if len( senses ) > *maxSenses { senses = senses[ :*maxSenses ] }

		record := Word{
			Word:   lemma,
			Tier:   tier,
			Senses: senses,
			Zipf:   math.Round( zipfEffective*100 ) / 100,
		}
		if hasAoA { record.AoA = math.Round( aoa.years*10 ) / 10 }
		record.score = score
		words = append( words , record )
	}

	assignAdvancedTiers( words )

	sort.Slice( words , func( a int , b int ) bool {
		if words[ a ].Tier != words[ b ].Tier { return words[ a ].Tier < words[ b ].Tier }
		if words[ a ].Zipf != words[ b ].Zipf { return words[ a ].Zipf > words[ b ].Zipf }
		return words[ a ].Word < words[ b ].Word
	} )

	if err = os.MkdirAll( filepath.Dir( *output ) , 0o755 ); err != nil {
		log.Fatalf( "output directory: %v" , err )
	}
	handle , err := os.Create( *output )
	if err != nil { log.Fatalf( "create %s: %v" , *output , err ) }
	writer , _ := gzip.NewWriterLevel( handle , gzip.BestCompression )
	encoder := json.NewEncoder( writer )
	for index := range words {
		if err = encoder.Encode( &words[ index ] ); err != nil {
			log.Fatalf( "encode: %v" , err )
		}
	}
	if err = writer.Close(); err != nil { log.Fatalf( "gzip close: %v" , err ) }
	if err = handle.Close(); err != nil { log.Fatalf( "close: %v" , err ) }

	histogram := map[int]int{}
	for _ , word := range words { histogram[ word.Tier ] += 1 }
	log.Printf( "\nwrote %d words to %s" , len( words ) , *output )
	if info , stat_err := os.Stat( *output ); stat_err == nil {
		log.Printf( "%.1f MB compressed" , float64( info.Size() )/1024/1024 )
	}
	log.Println( "\ntier distribution" )
	for tier := TierMin; tier <= TierMax; tier += 1 {
		log.Printf( "  %2d  %6d  %s" , tier , histogram[ tier ] , strings.Repeat( "#" , histogram[ tier ]/200 ) )
	}
	log.Println( "\ndropped" )
	for reason , count := range dropped { log.Printf( "  %-24s %d" , reason , count ) }
	fmt.Println()
}
