package search

import (
	"strconv"
	"strings"

	"github.com/jrsmile/goid3db/internal/model"
)

// Query is a parsed search string: zero or more field filters plus the
// remaining free text used for fuzzy matching.
//
// Syntax: `field:value` tokens are extracted as filters; everything else is
// fuzzy text. Supported fields: title, album, artist, genre, folder, year.
// Year accepts exact (year:1994), open ranges (year:>1990, year:<=2000) and
// closed ranges (year:1990..1999). Text field values match as case-insensitive
// substrings; wrap multi-word values in quotes, e.g. album:"dark side".
type Query struct {
	Fuzzy   string
	Filters []Filter
}

// Filter is a single field constraint.
type Filter struct {
	Field string
	// match reports whether the track satisfies this filter.
	match func(t *model.Track) bool
	raw   string
}

// HasConstraints reports whether the query restricts results at all.
func (q Query) HasConstraints() bool {
	return q.Fuzzy != "" || len(q.Filters) > 0
}

var filterFields = map[string]bool{
	"title": true, "album": true, "artist": true,
	"genre": true, "folder": true, "year": true,
}

// ParseQuery splits raw into field filters and fuzzy text.
func ParseQuery(raw string) Query {
	var q Query
	var fuzzy []string

	for _, tok := range tokenize(raw) {
		field, value, ok := splitField(tok)
		if !ok || !filterFields[field] {
			fuzzy = append(fuzzy, tok)
			continue
		}
		if f, ok := buildFilter(field, value); ok {
			q.Filters = append(q.Filters, f)
		} else {
			// Malformed filter (e.g. year:abc) -> treat as fuzzy text.
			fuzzy = append(fuzzy, tok)
		}
	}
	q.Fuzzy = strings.Join(fuzzy, " ")
	return q
}

// matches reports whether a track passes every filter.
func (q Query) matchesFilters(t *model.Track) bool {
	for _, f := range q.Filters {
		if !f.match(t) {
			return false
		}
	}
	return true
}

func buildFilter(field, value string) (Filter, bool) {
	value = strings.Trim(value, `"`)
	if field == "year" {
		return buildYearFilter(value)
	}
	needle := strings.ToLower(value)
	f := Filter{Field: field, raw: field + ":" + value}
	switch field {
	case "title":
		f.match = func(t *model.Track) bool { return containsFold(t.Title, needle) }
	case "album":
		f.match = func(t *model.Track) bool { return containsFold(t.Album, needle) }
	case "artist":
		f.match = func(t *model.Track) bool { return containsFold(t.Artist, needle) }
	case "genre":
		f.match = func(t *model.Track) bool { return containsFold(t.Genre, needle) }
	case "folder":
		f.match = func(t *model.Track) bool { return containsFold(t.Folder, needle) }
	default:
		return Filter{}, false
	}
	return f, true
}

func buildYearFilter(value string) (Filter, bool) {
	f := Filter{Field: "year", raw: "year:" + value}
	switch {
	case strings.Contains(value, ".."):
		parts := strings.SplitN(value, "..", 2)
		lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			return Filter{}, false
		}
		f.match = func(t *model.Track) bool { return t.Year >= lo && t.Year <= hi }
	case strings.HasPrefix(value, ">="):
		n, err := strconv.Atoi(value[2:])
		if err != nil {
			return Filter{}, false
		}
		f.match = func(t *model.Track) bool { return t.Year >= n }
	case strings.HasPrefix(value, "<="):
		n, err := strconv.Atoi(value[2:])
		if err != nil {
			return Filter{}, false
		}
		f.match = func(t *model.Track) bool { return t.Year > 0 && t.Year <= n }
	case strings.HasPrefix(value, ">"):
		n, err := strconv.Atoi(value[1:])
		if err != nil {
			return Filter{}, false
		}
		f.match = func(t *model.Track) bool { return t.Year > n }
	case strings.HasPrefix(value, "<"):
		n, err := strconv.Atoi(value[1:])
		if err != nil {
			return Filter{}, false
		}
		f.match = func(t *model.Track) bool { return t.Year > 0 && t.Year < n }
	default:
		n, err := strconv.Atoi(value)
		if err != nil {
			return Filter{}, false
		}
		f.match = func(t *model.Track) bool { return t.Year == n }
	}
	return f, true
}

// splitField splits a "field:value" token. Returns ok=false if there is no
// colon or the value is empty.
func splitField(tok string) (field, value string, ok bool) {
	i := strings.IndexByte(tok, ':')
	if i <= 0 || i == len(tok)-1 {
		return "", "", false
	}
	return strings.ToLower(tok[:i]), tok[i+1:], true
}

// containsFold reports whether lower-cased haystack contains needle, where
// needle is already lower-cased. It avoids allocating a lower-cased copy of the
// haystack on the hot path (the corpus is scanned per keystroke).
func containsFold(haystack, lowerNeedle string) bool {
	if lowerNeedle == "" {
		return true
	}
	n := len(haystack) - len(lowerNeedle)
	for i := 0; i <= n; i++ {
		match := true
		for j := 0; j < len(lowerNeedle); j++ {
			if toLowerByte(haystack[i+j]) != lowerNeedle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func toLowerByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// tokenize splits on whitespace but keeps quoted spans (field:"two words")
// together as a single token.
func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return toks
}
