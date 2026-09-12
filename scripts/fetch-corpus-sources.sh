#!/usr/bin/env bash
# Downloads the raw linguistic data the word corpus is built from.
#
# Nothing here is committed -- the sources are large and belong to their
# authors. What *is* committed is the generated, filtered result in
# server/corpus/data/words.jsonl.gz, which is what the binary embeds.
#
# Run this, then `go run ./cmd/builddata`, to regenerate the corpus from
# scratch. See ATTRIBUTION.md for the licence of each source.
set -euo pipefail

here="$( cd "$( dirname "${BASH_SOURCE[0]}" )/.." && pwd )"
out="${here}/corpus-sources"
mkdir -p "${out}"

fetch () {
  local url="$1" dest="$2"
  if [ -s "${dest}" ]; then
    echo "  have  $( basename "${dest}" )"
    return
  fi
  echo "  get   $( basename "${dest}" )"
  curl --fail --silent --show-error --location --max-time 600 --output "${dest}" "${url}"
}

echo "Fetching corpus sources into ${out}"

# Princeton WordNet 3.1 -- lemmas, parts of speech, definitions, examples.
fetch "https://wordnetcode.princeton.edu/wn3.1.dict.tar.gz" "${out}/wn31.dict.tar.gz"
if [ ! -d "${out}/dict" ]; then
  echo "  untar wn31.dict.tar.gz"
  tar -xzf "${out}/wn31.dict.tar.gz" -C "${out}"
fi

# OpenSubtitles 2018 unigram counts -- spoken/colloquial register.
fetch "https://raw.githubusercontent.com/hermitdave/FrequencyWords/master/content/2018/en/en_full.txt" \
      "${out}/freq_subtitles.txt"

# Google Web Trillion Word Corpus unigram counts (via Peter Norvig) --
# written/web register. The gap between this and the subtitle counts is what
# tells an academic word apart from a colloquial one.
fetch "https://norvig.com/ngrams/count_1w.txt" "${out}/freq_web.txt"

# Age-of-acquisition ratings (Kuperman, Stadthagen-Gonzalez & Brysbaert 2012).
# Distributed as .xlsx, so it is converted to a plain TSV here; the TSV is
# committed because parsing xlsx in Go to read three columns is not worth it.
if [ ! -s "${here}/corpus-sources/aoa.tsv" ] && [ ! -s "${here}/server/corpus/data/aoa.tsv" ]; then
  fetch "https://osf.io/download/vb9je/" "${out}/aoa_kuperman.xlsx"
  echo "  convert aoa_kuperman.xlsx -> aoa.tsv"
  python3 "${here}/scripts/aoa_to_tsv.py" "${out}/aoa_kuperman.xlsx" "${out}/aoa.tsv"
fi

echo "Done. Now run:  go run ./cmd/builddata"
