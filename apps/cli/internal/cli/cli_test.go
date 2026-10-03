package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/ThallesP/keel/apps/cli/internal/keel"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

func code(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func TestPickProject(t *testing.T) {
	one := []keel.Project{{ID: "p1", Slug: "api"}}
	two := append(one, keel.Project{ID: "p2", Slug: "web"})
	for _, tc := range []struct {
		name     string
		projects []keel.Project
		slug     string
		want     string // slug, or error code
	}{
		{"none", nil, "", output.CodeNoProjects},
		{"only one, nothing asked", one, "", "api"},
		{"by slug", two, "web", "web"},
		{"by id", two, "p2", "web"},
		{"ambiguous", two, "", output.CodeProjectRequired},
		{"unknown", two, "nope", output.CodeProjectNotFound},
	} {
		p, err := pickProject(tc.projects, tc.slug, "https://keel.test")
		got := code(err)
		if p != nil {
			got = p.Slug
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestFindService(t *testing.T) {
	services := []keel.Service{{ID: "n1", Name: "api"}, {ID: "n2", Name: "postgres"}}
	if s, err := findService(services, "postgres"); err != nil || s.ID != "n2" {
		t.Errorf("by name: %v, %v", s, err)
	}
	if s, err := findService(services, "n1"); err != nil || s.Name != "api" {
		t.Errorf("by id: %v, %v", s, err)
	}
	_, err := findService(services, "apu")
	if code(err) != output.CodeServiceNotFound || err.(*output.Error).Fix != "Services: api, postgres" {
		t.Errorf("unknown: %v", err)
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://keel.example.ts.net/":    "https://keel.example.ts.net",
		"http://100.64.0.1:8080/p/acme":   "http://100.64.0.1:8080",
		" https://dev.tail8eb3d.ts.net\n": "https://dev.tail8eb3d.ts.net",
		"keel.example.ts.net":             "",
		"ftp://keel.example.ts.net":       "",
	} {
		got, err := normalizeURL(in)
		if got != want || (want == "") != (err != nil) {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestLineSetSkipsWhatWasPrinted(t *testing.T) {
	at := func(sec int, text string) keel.LogLine {
		return keel.LogLine{Time: keel.Time{Time: time.Unix(int64(sec), 0)}, Text: text}
	}
	s := newLineSet()
	var printed []string
	for _, poll := range [][]keel.LogLine{
		{at(1, "a"), at(2, "b")},
		{at(1, "a"), at(2, "b"), at(2, "c"), at(3, "d")}, // overlapping tail, a new line at 2s
		{at(3, "d"), at(3, "d")},                         // nothing new
	} {
		for _, l := range poll {
			if s.add(l) {
				printed = append(printed, l.Text)
			}
		}
	}
	if got := len(printed); got != 4 || printed[2] != "c" || printed[3] != "d" {
		t.Errorf("printed %v, want [a b c d]", printed)
	}
}
