#!/usr/bin/env python3
"""Convert the Kuperman age-of-acquisition .xlsx into a plain TSV.

Only three columns matter downstream: the word, the mean AoA rating in years,
and the fraction of raters who knew the word well enough to rate it. Reading
those out of an OOXML zip with the standard library is a dozen lines; adding
an xlsx parser to the Go build to avoid it would not be.

    python3 scripts/aoa_to_tsv.py corpus-sources/aoa_kuperman.xlsx corpus-sources/aoa.tsv
"""
import sys
import zipfile
import xml.etree.ElementTree as ET

NS = "{http://schemas.openxmlformats.org/spreadsheetml/2006/main}"


def read_sheet(path):
    archive = zipfile.ZipFile(path)
    shared = []
    if "xl/sharedStrings.xml" in archive.namelist():
        for item in ET.fromstring(archive.read("xl/sharedStrings.xml")):
            shared.append("".join(node.text or "" for node in item.iter(NS + "t")))
    sheet = ET.fromstring(archive.read("xl/worksheets/sheet1.xml"))
    for row in sheet.iter(NS + "row"):
        cells = []
        for cell in row.iter(NS + "c"):
            value = cell.find(NS + "v")
            text = value.text if value is not None else None
            if cell.get("t") == "s" and text is not None:
                text = shared[int(text)]
            cells.append(text)
        yield cells


def main():
    if len(sys.argv) != 3:
        sys.exit("usage: aoa_to_tsv.py <in.xlsx> <out.tsv>")
    rows = read_sheet(sys.argv[1])
    header = next(rows)
    word_at = header.index("Word")
    aoa_at = header.index("Rating.Mean")
    # Published as "Dunno", but the values are OccurNum/OccurTotal -- the
    # fraction of raters who knew the word. Low values mark obscure words
    # whose AoA is an average over the few people who recognised them.
    known_at = header.index("Dunno")

    written = 0
    with open(sys.argv[2], "w", encoding="utf8") as out:
        out.write("word\taoa_years\tknown_fraction\n")
        for cells in rows:
            if len(cells) <= max(word_at, aoa_at, known_at):
                continue
            word = (cells[word_at] or "").strip().lower()
            if not word.isalpha():
                continue
            try:
                aoa = float(cells[aoa_at])
            except (TypeError, ValueError):
                continue
            try:
                known = float(cells[known_at])
            except (TypeError, ValueError):
                known = 1.0
            out.write("%s\t%.2f\t%.3f\n" % (word, aoa, known))
            written += 1
    print("wrote %d rows to %s" % (written, sys.argv[2]))


if __name__ == "__main__":
    main()
