// Package canonicalcsv reads and writes the canonical OBX import format
// (docs/import-format.md): products.csv (one row per variant), the optional
// assets.csv, and image file names of the form
// {style}_{color}_{position}[_{qualifier}…].{ext}.
//
// Parsing never stops at the first problem: every row and file is checked on
// its own and problems are returned as obx.ImportRowError values, exactly as
// a node reports them in an import run. BuildPlan turns parsed files and a
// list of images into the ordered writes a node applies.
package canonicalcsv

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/openbrandexchange/obx-go/obx"
)

// MaxFileBytes bounds how much of one CSV is read (100 MiB).
const MaxFileBytes = 100 << 20

// ErrUnreadable is wrapped by errors for files that cannot be parsed at all
// (not UTF-8 text, no header row). Nodes report them as import_invalid.
var ErrUnreadable = errors.New("import file unreadable")

// table is a CSV read into memory with a normalised header.
type table struct {
	file    string
	columns []column
	rows    [][]string // data rows; rows[i] is data row i+1
}

// column is one header cell: base name (normalised, aliases resolved) and an
// optional language tag (for name:nl and friends).
type column struct {
	raw  string
	base string
	lang string
}

// normalizeHeader lowercases and trims a header cell, turns spaces and "-"
// into "_" and splits off a ":<lang>" suffix (keeping the tag's case rules).
func normalizeHeader(raw string) column {
	s := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	base, lang, hasLang := strings.Cut(s, ":")
	base = strings.ToLower(strings.TrimSpace(base))
	base = strings.NewReplacer(" ", "_", "-", "_").Replace(base)
	c := column{raw: raw, base: base}
	if hasLang {
		if base == "attr" {
			c.lang = strings.TrimSpace(lang) // attribute key, kept as written
		} else {
			c.lang = NormalizeLanguageTag(lang)
		}
	}
	return c
}

// NormalizeLanguageTag turns "NL", "nl_be" or "nl-be" into "nl" / "nl-BE".
// It returns "" for anything that is not language or language-REGION.
func NormalizeLanguageTag(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "_", "-"))
	lang, region, hasRegion := strings.Cut(s, "-")
	lang = strings.ToLower(lang)
	if len(lang) < 2 || len(lang) > 3 || !isLetters(lang) {
		return ""
	}
	if !hasRegion {
		return lang
	}
	region = strings.ToUpper(region)
	if len(region) != 2 || !isLetters(strings.ToLower(region)) {
		return ""
	}
	return lang + "-" + region
}

func isLetters(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return s != ""
}

// detectDelimiter picks "," or ";" (or tab) from the header line: whichever
// occurs most often outside quotes; ties go to ",".
func detectDelimiter(header []byte) rune {
	counts := map[rune]int{}
	inQuotes := false
	for _, r := range string(header) {
		switch {
		case r == '"':
			inQuotes = !inQuotes
		case !inQuotes && (r == ',' || r == ';' || r == '\t'):
			counts[r]++
		}
	}
	best := ','
	for _, r := range []rune{';', '\t'} {
		if counts[r] > counts[best] {
			best = r
		}
	}
	return best
}

// readTable parses a CSV file into a table.
func readTable(r io.Reader, file string) (*table, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, fmt.Errorf("%w: %s is larger than %d bytes", ErrUnreadable, file, MaxFileBytes)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: %s is not UTF-8 text", ErrUnreadable, file)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: %s is empty (no header row)", ErrUnreadable, file)
	}
	firstLine, _, _ := bufio.NewReader(bytes.NewReader(data)).ReadLine()
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = detectDelimiter(firstLine)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = false
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrUnreadable, file, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: %s has no header row", ErrUnreadable, file)
	}
	t := &table{file: file}
	for _, h := range records[0] {
		t.columns = append(t.columns, normalizeHeader(h))
	}
	t.rows = records[1:]
	return t, nil
}

// cell returns the trimmed value of column index i in row, or "".
func cell(row []string, i int) string {
	if i < 0 || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func emptyRow(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// noRow marks a problem that belongs to a whole file (row null).
const noRow = -1

// problem builds an ImportRowError. row is the 1-based data row, 0 for the
// header and noRow for file-level problems.
func problem(file string, row int, col, code, severity, detail, value string) obx.ImportRowError {
	e := obx.ImportRowError{File: file, Severity: severity, ObxError: code, Detail: detail}
	if row >= 0 {
		r := row
		e.Row = &r
	}
	if col != "" {
		e.Column = &col
	}
	if value != "" {
		v := value
		if len(v) > 200 {
			v = v[:200]
		}
		e.Value = &v
	}
	return e
}
