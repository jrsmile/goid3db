package model

import "testing"

func TestBuildHaystackFull(t *testing.T) {
	tr := Track{
		Title:  "Bohemian Rhapsody",
		Album:  "A Night at the Opera",
		Artist: "Queen",
		Genre:  "Rock",
		Folder: "Queen",
		Path:   "/music/Queen/bohemian.mp3",
		Year:   1975,
	}
	tr.BuildHaystack()

	for _, want := range []string{"bohemian rhapsody", "queen", "rock", "1975", "/music/"} {
		if !contains(tr.Haystack, want) {
			t.Errorf("haystack %q missing %q", tr.Haystack, want)
		}
	}
	if tr.Haystack != lower(tr.Haystack) {
		t.Errorf("haystack should be lower-cased: %q", tr.Haystack)
	}
	if tr.Mask != AsciiMask(tr.Haystack) {
		t.Errorf("mask not consistent with haystack")
	}
}

func TestBuildHaystackEmptyFields(t *testing.T) {
	// All optional fields empty and Year == 0 exercises the early-return in the
	// write helper and the skipped year branch.
	var tr Track
	tr.BuildHaystack()
	if tr.Haystack != "" {
		t.Errorf("expected empty haystack, got %q", tr.Haystack)
	}
	if tr.Mask != [2]uint64{} {
		t.Errorf("expected zero mask, got %v", tr.Mask)
	}
}

func TestAsciiMask(t *testing.T) {
	m := AsciiMask("ab")
	if m[1]&(1<<('a'&63)) == 0 || m[1]&(1<<('b'&63)) == 0 {
		t.Errorf("expected a and b bits set: %v", m)
	}
	if AsciiMask("c") == m {
		t.Error("different strings should usually have different masks")
	}

	// Bytes >= 128 (non-ASCII) must be ignored without panicking.
	if AsciiMask("é") != ([2]uint64{}) {
		t.Error("non-ASCII bytes must not set any mask bit")
	}
}

func TestItoa(t *testing.T) {
	cases := map[int]string{0: "0", 7: "7", 1975: "1975", -42: "-42"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q, want %q", in, got, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
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
