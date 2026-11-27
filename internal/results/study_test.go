package results

import (
	"testing"
	"time"
)

func TestSortAndDedupe(t *testing.T) {
	d := func(day int) time.Time { return time.Date(2026, 11, day, 8, 0, 0, 0, time.UTC) }
	in := []Study{
		{File: "zero.pdf"},
		{File: "old.pdf", Date: d(1)},
		{File: "new.pdf", Date: d(20)},
		{File: "old.pdf", Date: d(1)},
		{File: "mid.pdf", Date: d(10)},
	}
	got := SortAndDedupe(in)
	want := []string{"new.pdf", "mid.pdf", "old.pdf", "zero.pdf"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i, f := range want {
		if got[i].File != f {
			t.Errorf("pos %d = %s, want %s", i, got[i].File, f)
		}
	}
}

func TestNameMatches(t *testing.T) {
	cases := []struct {
		first, surname, api string
		want                bool
	}{
		{"Ana", "Pérez", "ANA MARIA PEREZ GOMEZ", true},
		{"Ana", "Perez", "ANAMARIA PEREZ", false},
		{"", "Perez", "PEREZ", false},
		{"Ana", "", "ANA", false},
		{"Ana", "Perez", "ANA GOMEZ", false},
	}
	for _, c := range cases {
		if got := NameMatches(c.first, c.surname, c.api); got != c.want {
			t.Errorf("NameMatches(%q,%q,%q) = %v, want %v", c.first, c.surname, c.api, got, c.want)
		}
	}
}
