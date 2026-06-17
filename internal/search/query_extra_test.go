package search

import (
	"testing"

	"github.com/jrsmile/goid3db/internal/model"
)

func TestBuildFilterAllFields(t *testing.T) {
	tr := &model.Track{
		Title:  "Time",
		Album:  "Dark Side",
		Artist: "Pink Floyd",
		Genre:  "Rock",
		Folder: "Albums",
	}
	cases := []struct {
		field, value string
	}{
		{"title", "time"},
		{"album", "dark"},
		{"artist", "floyd"},
		{"genre", "rock"},
		{"folder", "album"},
	}
	for _, c := range cases {
		f, ok := buildFilter(c.field, c.value)
		if !ok {
			t.Fatalf("buildFilter(%s) not ok", c.field)
		}
		if !f.match(tr) {
			t.Errorf("expected %s:%s to match", c.field, c.value)
		}
	}
}

func TestBuildFilterUnknownField(t *testing.T) {
	// Directly exercise the defensive default branch (unreachable via ParseQuery
	// because unknown fields never reach buildFilter).
	if _, ok := buildFilter("bogus", "x"); ok {
		t.Error("expected unknown field to be rejected")
	}
}

func TestBuildYearFilterErrors(t *testing.T) {
	bad := []string{"1990..", "..1999", ">=x", "<=y", ">a", "<b", "notnum"}
	for _, v := range bad {
		if _, ok := buildYearFilter(v); ok {
			t.Errorf("buildYearFilter(%q) should fail", v)
		}
	}
}

func TestContainsFoldEmptyNeedle(t *testing.T) {
	if !containsFold("anything", "") {
		t.Error("empty needle should match everything")
	}
	if containsFold("abc", "xyz") {
		t.Error("non-substring should not match")
	}
	if !containsFold("Hello World", "world") {
		t.Error("case-insensitive substring should match")
	}
}

func TestSplitFieldEdges(t *testing.T) {
	if _, _, ok := splitField("noColon"); ok {
		t.Error("token without colon should not split")
	}
	if _, _, ok := splitField(":leading"); ok {
		t.Error("colon at start should not split")
	}
	if _, _, ok := splitField("trailing:"); ok {
		t.Error("empty value should not split")
	}
	f, v, ok := splitField("Genre:Rock")
	if !ok || f != "genre" || v != "Rock" {
		t.Errorf("unexpected split: %q %q %v", f, v, ok)
	}
}
