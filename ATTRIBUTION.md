# Data sources and licences

The word list this app ships with is generated from four public datasets by
`cmd/builddata`. None of them are redistributed here in their original form;
what is committed is the derived list in `server/corpus/data/words.jsonl.gz`
(words, reading levels, and WordNet definitions).

Run `./scripts/fetch-corpus-sources.sh` to download the originals, then
`go run ./cmd/builddata` to regenerate.

## Princeton WordNet 3.1 — definitions, parts of speech, examples

> WordNet 3.1 Copyright 2011 by Princeton University. All rights reserved.

Used under the WordNet licence, which permits use, copying, modification and
distribution for any purpose, commercially or otherwise, provided the
copyright notice and disclaimer appear in all copies. WordNet is provided
"as is" with no warranty.

- <https://wordnet.princeton.edu/>
- <https://wordnet.princeton.edu/license-and-commercial-use>

Fellbaum, C. (1998). *WordNet: An Electronic Lexical Database.* MIT Press.

## Age-of-acquisition ratings — the reading-level anchor for grades 1–12

Kuperman, V., Stadthagen-González, H., & Brysbaert, M. (2012).
Age-of-acquisition ratings for 30 thousand English words.
*Behavior Research Methods, 44*(4), 978–990.

- <https://doi.org/10.3758/s13428-012-0210-4>
- Dataset: <https://osf.io/d7x6q/>

## OpenSubtitles 2018 frequency list — spoken register

From the FrequencyWords project (MIT licence), derived from the OpenSubtitles
2018 corpus.

- <https://github.com/hermitdave/FrequencyWords>

Lison, P., & Tiedemann, J. (2016). OpenSubtitles2016: Extracting Large
Parallel Corpora from Movie and TV Subtitles. *LREC 2016.*

## Google Web Trillion Word Corpus unigrams — written register

Word counts published by Peter Norvig, derived from the Google Web 1T corpus.

- <https://norvig.com/ngrams/>

Norvig, P. (2009). Natural Language Corpus Data. In *Beautiful Data*
(Segaran & Hammerbacher, eds.), O'Reilly.

---

## How the reading levels are derived

Levels 1–12 correspond to US school grades and are anchored to real evidence:
age-of-acquisition ratings say when people report learning a word, and a
grade is an age. Frequency and semantic category adjust that estimate — see
the comments on `estimateTier` in `cmd/builddata/main.go`.

Levels 13–15 (undergraduate, graduate, doctoral) are **ranked, not measured**.
No dataset says which words are "doctoral", so claiming an absolute scale up
there would be inventing precision that does not exist. Instead the words
scoring past the top of the calibrated range are ordered by difficulty and cut
into three equal bands. The claim is "these are the hardest words in the
corpus, hardest third last", which the data supports.

These are estimates. They are useful for pacing a vocabulary trainer; they are
not a substitute for a validated readability assessment.
