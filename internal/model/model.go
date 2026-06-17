// Package model defines the core data types shared across goid3db.
package model

import "strings"

// Track is a single indexed audio file with its denormalized metadata.
type Track struct {
	ID      int64
	Path    string
	ModTime int64 // unix seconds, used to detect changes cheaply
	Size    int64
	Title   string
	Album   string
	Artist  string
	Genre   string
	Year    int
	Folder  string

	// Haystack is the precomputed lower-cased blob of all searchable fields.
	// Keeping it on the struct lets the in-memory fuzzy matcher avoid
	// rebuilding strings on every keystroke.
	Haystack string

	// Mask is an ASCII presence bitmap of Haystack: bit (b%64) of word (b/64)
	// is set when byte b (b<128) appears. The fuzzy matcher uses it to reject
	// non-matching tracks in O(1) before the full scan — a large win when most
	// of the corpus does not contain every query character.
	Mask [2]uint64
}

// BuildHaystack concatenates every searchable field into a single lower-cased
// string used by the fuzzy matcher and computes the ASCII presence Mask.
func (t *Track) BuildHaystack() {
	var b strings.Builder
	b.Grow(len(t.Title) + len(t.Album) + len(t.Artist) + len(t.Genre) + len(t.Folder) + len(t.Path) + 16)
	write := func(s string) {
		if s == "" {
			return
		}
		b.WriteString(s)
		b.WriteByte(' ')
	}
	write(t.Title)
	write(t.Album)
	write(t.Artist)
	write(t.Genre)
	write(t.Folder)
	write(t.Path)
	if t.Year > 0 {
		b.WriteString(itoa(t.Year))
	}
	t.Haystack = strings.ToLower(b.String())
	t.Mask = AsciiMask(t.Haystack)
}

// AsciiMask returns the ASCII (0..127) presence bitmap of s. Bytes >= 128 are
// ignored; matching on them falls back to the full scan.
func AsciiMask(s string) [2]uint64 {
	var m [2]uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 128 {
			m[c>>6] |= 1 << (c & 63)
		}
	}
	return m
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
