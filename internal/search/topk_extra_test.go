package search

import (
	"container/heap"
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

func TestClassOfViaFuzzy(t *testing.T) {
	// The scoring loop calls classOf on every byte in the matched span, so a
	// span containing upper-case, digit and non-word bytes exercises all
	// charClass branches.
	if _, ok := fuzzyScore([]byte("xz"), "xA3-z"); !ok {
		t.Fatal("expected match across mixed classes")
	}
}

func TestFuzzyEdgeCases(t *testing.T) {
	if s, ok := fuzzyScore([]byte(""), "anything"); !ok || s != 0 {
		t.Errorf("empty pattern should match with score 0, got %d,%v", s, ok)
	}
	if _, ok := fuzzyScore([]byte("abcd"), "ab"); ok {
		t.Error("pattern longer than text must not match")
	}
	// Two consecutive gaps exercise the gap-extension branch.
	if _, ok := fuzzyScore([]byte("az"), "axxz"); !ok {
		t.Error("expected scattered match with gap extension")
	}
}

func TestNewClampsWorkers(t *testing.T) {
	m := New(nil, 0)
	if m.workers != 1 {
		t.Errorf("expected workers clamped to 1, got %d", m.workers)
	}
}

func TestUpsertUpdatesExisting(t *testing.T) {
	m := New(nil, 2)
	m.Upsert(mkTrack(1, "Old Title", "A", "Al", "G", 2000))
	m.Upsert(mkTrack(1, "New Title", "A", "Al", "G", 2000)) // same ID -> update
	if m.Len() != 1 {
		t.Fatalf("expected 1 track after update, got %d", m.Len())
	}
	if hits := m.Search("new title", 5); len(hits) != 1 {
		t.Fatalf("expected updated track to be searchable, got %d", len(hits))
	}
	if hits := m.Search("old title", 5); len(hits) != 0 {
		t.Errorf("old title should no longer match, got %d", len(hits))
	}
}

func TestRemoveSwapsNonLast(t *testing.T) {
	m := New([]model.Track{
		mkTrack(1, "Alpha", "", "", "", 0),
		mkTrack(2, "Bravo", "", "", "", 0),
		mkTrack(3, "Charlie", "", "", "", 0),
	}, 2)
	// Remove the first (non-last) element to exercise the swap-with-last path.
	m.RemoveByPath("Alpha")
	if m.Len() != 2 {
		t.Fatalf("expected 2 tracks, got %d", m.Len())
	}
	if hits := m.Search("bravo", 5); len(hits) != 1 {
		t.Errorf("expected Bravo to survive removal, got %d", len(hits))
	}
	if hits := m.Search("charlie", 5); len(hits) != 1 {
		t.Errorf("expected Charlie to survive removal, got %d", len(hits))
	}
	// Removing a non-existent path is a no-op.
	m.RemoveByPath("nope")
	if m.Len() != 2 {
		t.Errorf("removing missing path changed length")
	}
}

func TestSearchQueryDefaultLimit(t *testing.T) {
	tracks := make([]model.Track, 60)
	for i := range tracks {
		tracks[i] = mkTrack(int64(i+1), "song", "", "", "", 0)
	}
	m := New(tracks, 3)
	// limit<=0 falls back to 50.
	if hits := m.SearchQuery(ParseQuery("song"), 0); len(hits) != 50 {
		t.Errorf("expected default limit 50, got %d", len(hits))
	}
}

func TestSearchBrowseWithFilter(t *testing.T) {
	tracks := []model.Track{
		mkTrack(1, "A", "", "", "rock", 1990),
		mkTrack(2, "B", "", "", "pop", 1991),
		mkTrack(3, "C", "", "", "rock", 1992),
	}
	m := New(tracks, 2)
	// Empty fuzzy + filter exercises the browsing fast path with filtering.
	hits := m.Search("genre:rock", 10)
	if len(hits) != 2 {
		t.Errorf("expected 2 rock tracks via browse filter, got %d", len(hits))
	}
}

func TestSearchFewerTracksThanWorkers(t *testing.T) {
	// n < workers exercises the workers clamp inside SearchQuery.
	m := New([]model.Track{mkTrack(1, "solo", "", "", "", 0)}, 8)
	if hits := m.Search("solo", 5); len(hits) != 1 {
		t.Errorf("expected single match, got %d", len(hits))
	}
}

func TestSearchTruncatesToLimit(t *testing.T) {
	tracks := make([]model.Track, 20)
	for i := range tracks {
		tracks[i] = mkTrack(int64(i+1), "match", "", "", "", 0)
	}
	m := New(tracks, 4)
	if hits := m.Search("match", 5); len(hits) != 5 {
		t.Errorf("expected results truncated to 5, got %d", len(hits))
	}
}

func TestHasConstraints(t *testing.T) {
	if ParseQuery("").HasConstraints() {
		t.Error("empty query should have no constraints")
	}
	if !ParseQuery("hello").HasConstraints() {
		t.Error("fuzzy text is a constraint")
	}
	if !ParseQuery("year:1990").HasConstraints() {
		t.Error("a filter is a constraint")
	}
}

func TestTopKEviction(t *testing.T) {
	k := newTopK(2)
	k.push(Hit{Score: 1})
	k.push(Hit{Score: 5})
	k.push(Hit{Score: 3}) // evicts the 1
	k.push(Hit{Score: 0}) // below root, ignored
	got := k.drain()
	if len(got) != 2 {
		t.Fatalf("expected 2 retained, got %d", len(got))
	}
	for _, h := range got {
		if h.Score < 3 {
			t.Errorf("low score %d should have been evicted", h.Score)
		}
	}
}

func TestTopKMinLimit(t *testing.T) {
	if k := newTopK(0); k.limit != 1 {
		t.Errorf("expected limit clamped to 1, got %d", k.limit)
	}
}

func TestMinHeapInterface(t *testing.T) {
	// Drive the heap directly to cover Push/Pop/Swap/Less/Len.
	h := &minHeap{}
	heap.Init(h)
	heap.Push(h, Hit{Score: 3})
	heap.Push(h, Hit{Score: 1})
	heap.Push(h, Hit{Score: 2})
	if h.Len() != 3 {
		t.Fatalf("expected len 3, got %d", h.Len())
	}
	// Pop yields ascending scores (min-heap).
	prev := -1
	for h.Len() > 0 {
		x := heap.Pop(h).(Hit)
		if x.Score < prev {
			t.Errorf("heap not ordered: %d after %d", x.Score, prev)
		}
		prev = x.Score
	}
}

func TestFuzzyUpperCaseQueryLowered(t *testing.T) {
	// An upper-case fuzzy query exercises the lower() conversion branch.
	m := New([]model.Track{mkTrack(1, "money", "", "", "", 0)}, 2)
	if hits := m.Search("MONEY", 5); len(hits) != 1 {
		t.Errorf("expected upper-case query to match lower-cased corpus, got %d", len(hits))
	}
}

func TestFilterRejectInFuzzyPath(t *testing.T) {
	// Two tracks both pass the ASCII mask for "money" but only one satisfies
	// the genre filter, exercising the filter-reject branch inside the fuzzy
	// shard loop.
	m := New([]model.Track{
		mkTrack(1, "money", "", "", "rock", 0),
		mkTrack(2, "money", "", "", "pop", 0),
	}, 2)
	hits := m.Search("money genre:rock", 10)
	if len(hits) != 1 || hits[0].Track.ID != 1 {
		t.Errorf("expected only the rock 'money' track, got %+v", hits)
	}
}

func TestSearchWorkerStartPastEnd(t *testing.T) {
	// n=5 with workers=4 gives chunk=2 and a final shard whose start (6) is
	// past the end, covering the `start >= n` break.
	tracks := make([]model.Track, 5)
	for i := range tracks {
		tracks[i] = mkTrack(int64(i+1), "alpha", "", "", "", 0)
	}
	m := New(tracks, 4)
	if hits := m.Search("alpha", 10); len(hits) != 5 {
		t.Errorf("expected 5 matches, got %d", len(hits))
	}
}

func TestFuzzyConsecutiveBoundaryInheritance(t *testing.T) {
	// A run that begins off-boundary then crosses a non-word→word transition
	// inside the consecutive run exercises the boundary-inheritance branch.
	if _, ok := fuzzyScore([]byte("a-b"), "xa-b"); !ok {
		t.Error("expected consecutive match with embedded separator")
	}
}
