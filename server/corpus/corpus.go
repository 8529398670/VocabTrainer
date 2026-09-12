// Package corpus holds the word list: every word the trainer can show, with
// its definitions and the reading grade it was placed in.
//
// The list is generated offline by cmd/builddata from WordNet, two frequency
// corpora and a set of age-of-acquisition ratings, then compiled into the
// binary. It is read-only at runtime and shared by every request, so it is
// loaded once at startup and never locked.
//
// Nothing here knows about users or progress -- that lives in
// server/models/progress.go. This package answers one question: "which words
// exist, and how hard are they".
package corpus

import (
	bufio "bufio"
	gzip "compress/gzip"
	bytes "bytes"
	_ "embed"
	json "encoding/json"
	fmt "fmt"
	rand "math/rand"
	sort "sort"
	strings "strings"
)

//go:embed data/words.jsonl.gz
var embeddedWords []byte

// Tier bounds. 1-12 are US school grades; 13, 14 and 15 are undergraduate,
// graduate and doctoral. Their display names are not here on purpose -- they
// are user-facing text, so they live in language.yaml like every other
// string, and the API speaks in numbers.
const (
	TierMin = 1
	TierMax = 15
)

type Sense struct {
	Pos        string `json:"pos"`
	Definition string `json:"definition"`
	Example    string `json:"example,omitempty"`
}

type Word struct {
	Word   string  `json:"word"`
	Tier   int     `json:"tier"`
	Senses []Sense `json:"senses"`
	Zipf   float64 `json:"zipf"`
	AoA    float64 `json:"aoa,omitempty"`
}

// rawWord mirrors the one-letter keys the generator writes. Keeping the wire
// format terse and the in-memory shape readable costs one struct and saves
// about a third of the embedded file.
type rawWord struct {
	Word   string `json:"w"`
	Tier   int    `json:"t"`
	Senses []struct {
		Pos        string `json:"p"`
		Definition string `json:"d"`
		Example    string `json:"x"`
	} `json:"s"`
	Zipf float64 `json:"z"`
	AoA  float64 `json:"a"`
}

type Corpus struct {
	words   []Word
	byTier  map[int][]int
	byWord  map[string]int
}

func Load() ( corpus *Corpus , err error ) {
	reader , err := gzip.NewReader( bytes.NewReader( embeddedWords ) )
	if err != nil {
		err = fmt.Errorf( "corpus: opening embedded word list: %w" , err )
		return
	}
	defer reader.Close()

	corpus = &Corpus{
		byTier: map[int][]int{},
		byWord: map[string]int{},
	}

	scanner := bufio.NewScanner( reader )
	scanner.Buffer( make( []byte , 256*1024 ) , 256*1024 )
	for scanner.Scan() {
		line := scanner.Bytes()
		if len( line ) == 0 { continue }
		raw := rawWord{}
		if err = json.Unmarshal( line , &raw ); err != nil {
			err = fmt.Errorf( "corpus: parsing word list: %w" , err )
			corpus = nil
			return
		}
		word := Word{ Word: raw.Word , Tier: raw.Tier , Zipf: raw.Zipf , AoA: raw.AoA }
		for _ , sense := range raw.Senses {
			word.Senses = append( word.Senses , Sense{
				Pos: sense.Pos , Definition: sense.Definition , Example: sense.Example ,
			} )
		}
		if len( word.Senses ) == 0 { continue }

		at := len( corpus.words )
		corpus.words = append( corpus.words , word )
		corpus.byTier[ word.Tier ] = append( corpus.byTier[ word.Tier ] , at )
		corpus.byWord[ word.Word ] = at
	}
	if err = scanner.Err(); err != nil {
		err = fmt.Errorf( "corpus: reading word list: %w" , err )
		corpus = nil
		return
	}
	if len( corpus.words ) == 0 {
		err = fmt.Errorf( "corpus: embedded word list is empty -- run cmd/builddata" )
		corpus = nil
	}
	return
}

func ( corpus *Corpus ) Size() ( result int ) {
	result = len( corpus.words )
	return
}

func ( corpus *Corpus ) CountByTier( tier int ) ( result int ) {
	result = len( corpus.byTier[ tier ] )
	return
}

// Lookup finds one word. Used when a saved card names a word, so the stored
// record can stay small: progress records hold the word, not a copy of its
// definitions.
func ( corpus *Corpus ) Lookup( word string ) ( result *Word , found bool ) {
	at , ok := corpus.byWord[ strings.ToLower( strings.TrimSpace( word ) ) ]
	if ok == false { return }
	result , found = &corpus.words[ at ] , true
	return
}

// tierWeights spreads a draw across the chosen level and its neighbours.
//
// Drawing only from the exact tier would be both monotonous and a worse way
// to learn: the words just below the level are the ones worth consolidating,
// and the ones just above are where anything new is actually picked up. The
// level still dominates, so the setting means what it says.
var tierWeights = []struct {
	offset int
	weight int
}{
	{ offset: -1 , weight: 2 },
	{ offset: 0 , weight: 5 },
	{ offset: 1 , weight: 3 },
}

// Draw picks up to count words around the given tier that skip reports false
// for, without repeating any within one call.
//
// The shuffle is over the candidate index lists rather than the whole corpus,
// and it stops as soon as it has enough, so the cost tracks how many cards
// were asked for rather than how many words exist.
func ( corpus *Corpus ) Draw( tier int , count int , skip func( word string ) bool , random *rand.Rand ) ( result []*Word ) {
	result = []*Word{}
	if count <= 0 { return }

	// How many to try to take from each neighbouring tier.
	totalWeight := 0
	for _ , entry := range tierWeights { totalWeight += entry.weight }

	taken := map[string]bool{}
	for _ , entry := range tierWeights {
		target := tier + entry.offset
		if target < TierMin || target > TierMax { continue }
		want := count * entry.weight / totalWeight
		if want < 1 { want = 1 }
		corpus.drawFromTier( target , want , skip , taken , random , &result )
	}

	// Neighbours may have been exhausted (or the level sits at an edge).
	// Widen outwards until the batch is full or there is nothing left.
	for distance := 0; len( result ) < count && distance <= TierMax; distance += 1 {
		for _ , target := range []int{ tier - distance , tier + distance } {
			if target < TierMin || target > TierMax { continue }
			corpus.drawFromTier( target , count-len( result ) , skip , taken , random , &result )
			if len( result ) >= count { break }
		}
	}

	random.Shuffle( len( result ) , func( a int , b int ) {
		result[ a ] , result[ b ] = result[ b ] , result[ a ]
	} )
	if len( result ) > count { result = result[ :count ] }
	return
}

func ( corpus *Corpus ) drawFromTier( tier int , want int , skip func( word string ) bool , taken map[string]bool , random *rand.Rand , into *[]*Word ) {
	pool := corpus.byTier[ tier ]
	if len( pool ) == 0 || want <= 0 { return }

	// Walk the tier from a random offset in a random stride rather than
	// shuffling a copy of it: the tiers hold thousands of entries and this is
	// on the path of every deck request.
	//
	// The stride has to be coprime with the pool size or the walk only ever
	// reaches a fraction of it -- a stride of 2 over an even-sized tier visits
	// half the words and never the rest. That is invisible until someone has
	// worked through most of a level, at which point their deck would come up
	// short while unseen words sat in the half the walk could not reach.
	start := random.Intn( len( pool ) )
	stride := 1 + random.Intn( 7 )
	if greatestCommonDivisor( stride , len( pool ) ) != 1 { stride = 1 }
	added := 0
	for step := 0; step < len( pool ) && added < want; step += 1 {
		word := &corpus.words[ pool[ ( start + step*stride ) % len( pool ) ] ]
		if taken[ word.Word ] { continue }
		if skip != nil && skip( word.Word ) { continue }
		taken[ word.Word ] = true
		*into = append( *into , word )
		added += 1
	}
	return
}

// TierCounts reports how many words sit in each tier, for the level picker.
func greatestCommonDivisor( a int , b int ) ( result int ) {
	for b != 0 { a , b = b , a%b }
	result = a
	return
}

func ( corpus *Corpus ) TierCounts() ( result []int ) {
	result = make( []int , 0 , TierMax )
	for tier := TierMin; tier <= TierMax; tier += 1 {
		result = append( result , len( corpus.byTier[ tier ] ) )
	}
	return
}

// Search backs the "find a word" box in the word lists. Prefix matches rank
// above substring matches, which is what someone typing into a search box
// expects to see.
func ( corpus *Corpus ) Search( query string , limit int ) ( result []*Word ) {
	result = []*Word{}
	needle := strings.ToLower( strings.TrimSpace( query ) )
	if needle == "" || limit <= 0 { return }

	prefixed := []*Word{}
	contained := []*Word{}
	for index := range corpus.words {
		word := &corpus.words[ index ]
		switch {
		case strings.HasPrefix( word.Word , needle ):
			prefixed = append( prefixed , word )
		case strings.Contains( word.Word , needle ):
			contained = append( contained , word )
		}
		if len( prefixed ) >= limit { break }
	}
	sort.Slice( prefixed , func( a int , b int ) bool { return len( prefixed[ a ].Word ) < len( prefixed[ b ].Word ) } )
	result = append( result , prefixed... )
	for _ , word := range contained {
		if len( result ) >= limit { break }
		result = append( result , word )
	}
	if len( result ) > limit { result = result[ :limit ] }
	return
}
