package search

import (
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

func mkTrack(id int64, title, artist, album, genre string, year int) model.Track {
	t := model.Track{ID: id, Title: title, Artist: artist, Album: album, Genre: genre, Year: year, Path: title}
	t.BuildHaystack()
	return t
}

func TestFuzzyScoreOrdering(t *testing.T) {
	// Exact substring should outscore a scattered subsequence.
	exact, ok1 := fuzzyScore([]byte("beat"), "the beatles greatest")
	scattered, ok2 := fuzzyScore([]byte("beat"), "b e a t pattern")
	if !ok1 || !ok2 {
		t.Fatalf("expected both to match: %v %v", ok1, ok2)
	}
	if exact <= scattered {
		t.Errorf("expected consecutive match to score higher: exact=%d scattered=%d", exact, scattered)
	}
}

func TestFuzzyNoMatch(t *testing.T) {
	if _, ok := fuzzyScore([]byte("xyz"), "abcdef"); ok {
		t.Error("expected no match")
	}
}

func TestMatcherSearch(t *testing.T) {
	tracks := []model.Track{
		mkTrack(1, "Bohemian Rhapsody", "Queen", "A Night at the Opera", "Rock", 1975),
		mkTrack(2, "Stairway to Heaven", "Led Zeppelin", "Led Zeppelin IV", "Rock", 1971),
		mkTrack(3, "Billie Jean", "Michael Jackson", "Thriller", "Pop", 1982),
		mkTrack(4, "Thriller", "Michael Jackson", "Thriller", "Pop", 1982),
	}
	m := New(tracks, 2)

	hits := m.Search("thriller", 10)
	if len(hits) == 0 {
		t.Fatal("expected matches for 'thriller'")
	}
	// Both the "Thriller" track and the "Thriller" album entries should appear.
	found := map[int64]bool{}
	for _, h := range hits {
		found[h.Track.ID] = true
	}
	if !found[3] || !found[4] {
		t.Errorf("expected tracks 3 and 4 in results, got %v", found)
	}

	// Search by artist.
	hits = m.Search("queen", 10)
	if len(hits) != 1 || hits[0].Track.ID != 1 {
		t.Errorf("expected only Queen track, got %d hits", len(hits))
	}

	// Search by year.
	hits = m.Search("1982", 10)
	if len(hits) < 2 {
		t.Errorf("expected year match to find both 1982 tracks, got %d", len(hits))
	}
}

func TestMatcherUpsertRemove(t *testing.T) {
	m := New(nil, 2)
	tr := mkTrack(1, "New Song", "Artist", "Album", "Genre", 2020)
	m.Upsert(tr)
	if m.Len() != 1 {
		t.Fatalf("expected 1 track, got %d", m.Len())
	}
	if hits := m.Search("new song", 5); len(hits) != 1 {
		t.Fatalf("expected to find upserted track, got %d", len(hits))
	}
	m.RemoveByPath("New Song")
	if m.Len() != 0 {
		t.Errorf("expected 0 tracks after removal, got %d", m.Len())
	}
}

func TestEmptyQueryBrowses(t *testing.T) {
	tracks := []model.Track{
		mkTrack(1, "A", "", "", "", 0),
		mkTrack(2, "B", "", "", "", 0),
	}
	m := New(tracks, 2)
	if hits := m.Search("", 10); len(hits) != 2 {
		t.Errorf("expected empty query to browse all, got %d", len(hits))
	}
}
