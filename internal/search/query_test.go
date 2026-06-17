package search

import (
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

func TestParseQuerySplitsFiltersAndFuzzy(t *testing.T) {
	q := ParseQuery(`year:1994 genre:rock dark side`)
	if q.Fuzzy != "dark side" {
		t.Errorf("fuzzy = %q, want %q", q.Fuzzy, "dark side")
	}
	if len(q.Filters) != 2 {
		t.Fatalf("expected 2 filters, got %d", len(q.Filters))
	}
}

func TestParseQueryQuotedValue(t *testing.T) {
	q := ParseQuery(`artist:"pink floyd" time`)
	if len(q.Filters) != 1 || q.Filters[0].Field != "artist" {
		t.Fatalf("expected one artist filter, got %+v", q.Filters)
	}
	tr := &model.Track{Artist: "Pink Floyd"}
	if !q.matchesFilters(tr) {
		t.Error("expected quoted multi-word artist filter to match")
	}
	tr2 := &model.Track{Artist: "Queen"}
	if q.matchesFilters(tr2) {
		t.Error("expected non-matching artist to fail")
	}
}

func TestYearFilters(t *testing.T) {
	cases := []struct {
		expr string
		year int
		want bool
	}{
		{"year:1994", 1994, true},
		{"year:1994", 1995, false},
		{"year:1990..1999", 1995, true},
		{"year:1990..1999", 2000, false},
		{"year:>1990", 1991, true},
		{"year:>1990", 1990, false},
		{"year:>=1990", 1990, true},
		{"year:<2000", 1999, true},
		{"year:<2000", 2000, false},
		{"year:<=2000", 2000, true},
	}
	for _, c := range cases {
		q := ParseQuery(c.expr)
		if len(q.Filters) != 1 {
			t.Fatalf("%s: expected 1 filter, got %d", c.expr, len(q.Filters))
		}
		got := q.matchesFilters(&model.Track{Year: c.year})
		if got != c.want {
			t.Errorf("%s with year=%d: got %v, want %v", c.expr, c.year, got, c.want)
		}
	}
}

func TestMalformedFilterFallsBackToFuzzy(t *testing.T) {
	q := ParseQuery(`year:abc hello`)
	if len(q.Filters) != 0 {
		t.Errorf("expected malformed year to not become a filter, got %+v", q.Filters)
	}
	if q.Fuzzy != "year:abc hello" {
		t.Errorf("fuzzy = %q", q.Fuzzy)
	}
}

func TestUnknownFieldIsFuzzy(t *testing.T) {
	q := ParseQuery(`bpm:120 groove`)
	if len(q.Filters) != 0 {
		t.Errorf("expected unknown field to be fuzzy, got filters %+v", q.Filters)
	}
}

func TestMatcherWithFilters(t *testing.T) {
	tracks := []model.Track{
		mkTrack(1, "Time", "Pink Floyd", "The Wall", "Rock", 1979),
		mkTrack(2, "So What", "Miles Davis", "Kind of Blue", "Jazz", 1959),
		mkTrack(3, "Money", "Pink Floyd", "Dark Side", "Rock", 1973),
	}
	m := New(tracks, 2)

	// Filter only (no fuzzy): genre:rock should yield the two Pink Floyd tracks.
	hits := m.Search("genre:rock", 10)
	if len(hits) != 2 {
		t.Fatalf("genre:rock expected 2, got %d", len(hits))
	}

	// Filter + fuzzy: genre:rock + "money" should yield only track 3.
	hits = m.Search("genre:rock money", 10)
	if len(hits) != 1 || hits[0].Track.ID != 3 {
		t.Fatalf("genre:rock money expected track 3, got %+v", hits)
	}

	// Year range filter.
	hits = m.Search("year:1970..1980", 10)
	if len(hits) != 2 {
		t.Fatalf("year range expected 2, got %d", len(hits))
	}
}
