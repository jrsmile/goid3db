package search

import (
	"fmt"
	"math/rand"
	"runtime"
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

// syntheticCorpus builds n pseudo-random tracks with realistic-looking
// metadata so benchmarks exercise the matcher at scale.
func syntheticCorpus(n int) []model.Track {
	rng := rand.New(rand.NewSource(1))
	artists := []string{"Pink Floyd", "Queen", "Led Zeppelin", "Miles Davis", "Daft Punk",
		"Radiohead", "The Beatles", "Nina Simone", "Aphex Twin", "Bonobo",
		"Fleetwood Mac", "Kendrick Lamar", "Bjork", "Portishead", "Massive Attack"}
	albums := []string{"The Wall", "A Night at the Opera", "IV", "Kind of Blue", "Discovery",
		"OK Computer", "Abbey Road", "Pastel Blues", "Selected Ambient Works", "Black Sands"}
	genres := []string{"Rock", "Jazz", "Electronic", "Hip-Hop", "Pop", "Ambient", "Trip-Hop", "Soul"}
	words := []string{"Time", "Money", "Dream", "Night", "Light", "Echoes", "Shine", "Run",
		"Blue", "Gold", "Rain", "Fire", "Ocean", "Sky", "Ghost", "Machine", "Heart", "Wave"}

	tracks := make([]model.Track, n)
	for i := 0; i < n; i++ {
		title := fmt.Sprintf("%s %s", words[rng.Intn(len(words))], words[rng.Intn(len(words))])
		artist := artists[rng.Intn(len(artists))]
		album := albums[rng.Intn(len(albums))]
		genre := genres[rng.Intn(len(genres))]
		year := 1965 + rng.Intn(60)
		folder := fmt.Sprintf("%s - %s", artist, album)
		t := model.Track{
			ID:     int64(i + 1),
			Path:   fmt.Sprintf("/music/%s/%05d %s.mp3", folder, i, title),
			Title:  title,
			Artist: artist,
			Album:  album,
			Genre:  genre,
			Year:   year,
			Folder: folder,
		}
		t.BuildHaystack()
		tracks[i] = t
	}
	return tracks
}

// BenchmarkSearch1M measures fuzzy-search latency over a 1,000,000-track corpus
// — the design's headline constraint. Run with:
//
//	go test ./internal/search/ -run=^$ -bench=BenchmarkSearch1M -benchmem
func BenchmarkSearch1M(b *testing.B) {
	const n = 1_000_000
	m := New(syntheticCorpus(n), runtime.GOMAXPROCS(0))

	queries := []string{"pink time", "blue ocean", "queen night", "machine heart", "xyzzy"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := queries[i%len(queries)]
		_ = m.Search(q, 200)
	}
}

// BenchmarkSearchFiltered1M measures latency when a structured filter prunes
// the corpus before fuzzy scoring.
func BenchmarkSearchFiltered1M(b *testing.B) {
	const n = 1_000_000
	m := New(syntheticCorpus(n), runtime.GOMAXPROCS(0))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Search("genre:jazz year:1965..1980 blue", 200)
	}
}

// BenchmarkSearch2M measures fuzzy-search latency over a 2,000,000-track corpus
// to confirm the design stays interactive at double the headline scale.
func BenchmarkSearch2M(b *testing.B) {
	const n = 2_000_000
	m := New(syntheticCorpus(n), runtime.GOMAXPROCS(0))

	queries := []string{"pink time", "blue ocean", "queen night", "machine heart", "xyzzy"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := queries[i%len(queries)]
		_ = m.Search(q, 200)
	}
}

// BenchmarkSearchFiltered2M measures filtered latency at 2,000,000 tracks.
func BenchmarkSearchFiltered2M(b *testing.B) {
	const n = 2_000_000
	m := New(syntheticCorpus(n), runtime.GOMAXPROCS(0))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Search("genre:jazz year:1965..1980 blue", 200)
	}
}

// BenchmarkParseQuery isolates the cost of parsing the structured query.
func BenchmarkParseQuery(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ParseQuery(`year:1990..1999 genre:rock artist:"pink floyd" time money`)
	}
}
