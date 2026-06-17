// Package search implements an in-memory, fzf-style fuzzy matcher sharded
// across CPU cores. Generic fuzzy libraries do a single-threaded linear scan
// that stops being interactive around a million items; this matcher keeps
// per-keystroke latency low by parallelizing the scan and bounding the result
// set with a per-shard top-K heap.
package search

import (
	"sort"
	"sync"

	"github.com/jrsmile/goid3db/internal/model"
)

// Matcher holds the searchable corpus. It is safe for concurrent Search calls;
// mutations (Add/Update/Remove) must be externally serialized with searches.
//
// Alongside the authoritative tracks slice it keeps a cache-friendly
// Structure-of-Arrays "hot index" (contiguous masks + haystacks) so the
// per-keystroke scan streams small contiguous memory instead of chasing
// pointers through 2M large structs. The hot arrays stay index-aligned with
// tracks.
type Matcher struct {
	mu      sync.RWMutex
	tracks  []model.Track
	masks   [][2]uint64 // ASCII presence bitmap per track (index-aligned)
	hays    []string    // haystack per track (index-aligned)
	byID    map[int64]int
	workers int
}

// New builds a matcher over the given tracks (the slice is adopted, not copied).
func New(tracks []model.Track, workers int) *Matcher {
	if workers <= 0 {
		workers = 1
	}
	m := &Matcher{
		tracks:  tracks,
		masks:   make([][2]uint64, len(tracks)),
		hays:    make([]string, len(tracks)),
		byID:    make(map[int64]int, len(tracks)),
		workers: workers,
	}
	for i := range tracks {
		m.byID[tracks[i].ID] = i
		m.masks[i] = tracks[i].Mask
		m.hays[i] = tracks[i].Haystack
	}
	return m
}

// Hit is a scored search result.
type Hit struct {
	Track *model.Track
	Score int
}

// Len returns the number of tracks in the corpus.
func (m *Matcher) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.tracks)
}

// Upsert inserts or replaces a track by ID.
func (m *Matcher) Upsert(t model.Track) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if i, ok := m.byID[t.ID]; ok {
		m.tracks[i] = t
		m.masks[i] = t.Mask
		m.hays[i] = t.Haystack
		return
	}
	m.byID[t.ID] = len(m.tracks)
	m.tracks = append(m.tracks, t)
	m.masks = append(m.masks, t.Mask)
	m.hays = append(m.hays, t.Haystack)
}

// RemoveByPath deletes a track identified by path (used by the watcher).
func (m *Matcher) RemoveByPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tracks {
		if m.tracks[i].Path == path {
			m.removeIndex(i)
			return
		}
	}
}

func (m *Matcher) removeIndex(i int) {
	last := len(m.tracks) - 1
	delete(m.byID, m.tracks[i].ID)
	if i != last {
		m.tracks[i] = m.tracks[last]
		m.masks[i] = m.masks[last]
		m.hays[i] = m.hays[last]
		m.byID[m.tracks[i].ID] = i
	}
	m.tracks = m.tracks[:last]
	m.masks = m.masks[:last]
	m.hays = m.hays[:last]
}

// Search returns up to limit best matches for query, highest score first. The
// query may contain field filters (e.g. `year:1994 genre:rock`) plus free
// fuzzy text. An empty query returns the first `limit` tracks unscored (fast
// path for browsing).
func (m *Matcher) Search(query string, limit int) []Hit {
	return m.SearchQuery(ParseQuery(query), limit)
}

// SearchQuery runs an already-parsed query.
func (m *Matcher) SearchQuery(q Query, limit int) []Hit {
	m.mu.RLock()
	defer m.mu.RUnlock()

	n := len(m.tracks)
	if limit <= 0 {
		limit = 50
	}
	hasFilters := len(q.Filters) > 0

	// Browsing fast path: no fuzzy text. Apply filters (if any) in order.
	if q.Fuzzy == "" {
		out := make([]Hit, 0, min(limit, n))
		for i := 0; i < n && len(out) < limit; i++ {
			if hasFilters && !q.matchesFilters(&m.tracks[i]) {
				continue
			}
			out = append(out, Hit{Track: &m.tracks[i]})
		}
		return out
	}

	pat := []byte(lower(q.Fuzzy))
	patMask := model.AsciiMask(string(pat))
	pm0, pm1 := patMask[0], patMask[1]

	workers := m.workers
	if workers > n {
		workers = max(1, n)
	}
	chunk := (n + workers - 1) / max(1, workers)

	masks := m.masks
	hays := m.hays
	shardResults := make([][]Hit, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		start := w * chunk
		if start >= n {
			break
		}
		end := min(start+chunk, n)
		wg.Add(1)
		go func(w, start, end int) {
			defer wg.Done()
			h := newTopK(limit)
			for i := start; i < end; i++ {
				// O(1) rejection on the contiguous mask array: skip tracks
				// missing any query character without touching the haystack
				// or the large Track struct.
				if masks[i][0]&pm0 != pm0 || masks[i][1]&pm1 != pm1 {
					continue
				}
				if hasFilters && !q.matchesFilters(&m.tracks[i]) {
					continue
				}
				if score, ok := fuzzyScore(pat, hays[i]); ok {
					h.push(Hit{Track: &m.tracks[i], Score: score})
				}
			}
			shardResults[w] = h.drain()
		}(w, start, end)
	}
	wg.Wait()

	var merged []Hit
	for _, r := range shardResults {
		merged = append(merged, r...)
	}
	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].Track.Path < merged[j].Track.Path
	})
	if len(merged) > limit {
		merged = merged[:limit]
	}
	return merged
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
