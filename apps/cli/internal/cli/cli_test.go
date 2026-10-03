package cli

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/apps/cli/internal/keel"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

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
		p, err := pickProject(tc.projects, tc.slug)
		got := output.CodeOf(err)
		if p != nil {
			got = p.Slug
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Missing or bad input fails as USAGE before keel connects anywhere; no install is configured
// here, so getting past the checks is NOT_AUTHENTICATED.
func TestUsageBeforeConnecting(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	t.Setenv("KEEL_URL", "")
	t.Setenv("KEEL_INSTANCE", "")
	for args, want := range map[string]string{
		"project create --json":                                            output.CodeUsage,
		"project create acme-api --link --json":                            output.CodeNotAuthenticated,
		"service create api --json":                                        output.CodeUsage, // no --image
		"service create api --image nginx --port x --json":                 output.CodeUsage,
		"service create api --image nginx --port 3000 --replicas 2 --json": output.CodeNotAuthenticated,
		"service delete api --json":                                        output.CodeUsage, // no terminal to ask, no --yes
		"service delete api --yes --json":                                  output.CodeNotAuthenticated,
		"service rm api -y --json":                                         output.CodeNotAuthenticated,
	} {
		root := (&app{}).root("test")
		root.SetArgs(strings.Fields(args))
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		_, err := root.ExecuteC()
		if got := output.CodeOf(err); got != want {
			t.Errorf("keel %s: %q (%v), want %s", args, got, err, want)
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
	if output.CodeOf(err) != output.CodeServiceNotFound || err.(*output.Error).Fix != "Services: api, postgres" {
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

func TestJSONArg(t *testing.T) {
	for args, want := range map[string]bool{
		"nope --json":               true,
		"nope --json=true":          true,
		"nope --json=1":             true,
		"nope --json=false":         false,
		"nope --json --json=0":      false,
		"nope --json=maybe":         false,
		"var set -- --json":         false,
		"nope":                      false,
		"nope --jsonx --json=t --x": true,
	} {
		if got := jsonArg(strings.Fields(args)); got != want {
			t.Errorf("jsonArg(%q) = %v, want %v", args, got, want)
		}
	}
}
