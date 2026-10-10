package cli

import (
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/output"
)

func TestPickProject(t *testing.T) {
	one := []client.Project{{ID: "p1", Slug: "api"}}
	two := append(one, client.Project{ID: "p2", Slug: "web"})
	for _, tc := range []struct {
		name     string
		projects []client.Project
		slug     string
		want     string
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

func TestUsageBeforeConnecting(t *testing.T) {
	t.Setenv("KEEL_CONFIG_DIR", t.TempDir())
	t.Setenv("KEEL_URL", "")
	t.Setenv("KEEL_INSTANCE", "")
	for args, want := range map[string]string{
		"project create --json":                                            output.CodeUsage,
		"project create acme-api --link --json":                            output.CodeNotAuthenticated,
		"service create api --json":                                        output.CodeUsage,
		"service create api --image nginx --port x --json":                 output.CodeUsage,
		"service create api --image nginx --port 3000 --replicas 2 --json": output.CodeNotAuthenticated,
		"service delete api --json":                                        output.CodeUsage,
		"service delete api --yes --json":                                  output.CodeNotAuthenticated,
		"service rm api -y --json":                                         output.CodeNotAuthenticated,
		"run api --json":                                                   output.CodeUsage,
		"run api npm start --json":                                         output.CodeUsage,
		"run --json -- npm start":                                          output.CodeUsage,
		"run api --json -- npm start":                                      output.CodeNotAuthenticated,
		"traces --since 2h --json":                                         output.CodeUsage,
		"traces api --since 1h --json":                                     output.CodeNotAuthenticated,
		"tracing enable --json":                                            output.CodeUsage,
		"tracing prompt --json":                                            output.CodeNotAuthenticated,
	} {
		root := (&app{}).root()
		root.SetArgs(strings.Fields(args))
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		_, err := root.ExecuteC()
		if got := output.CodeOf(err); got != want {
			t.Errorf("keel %s: %q (%v), want %s", args, got, err, want)
		}
	}
}

func TestRunEnv(t *testing.T) {
	shell := []string{"HOME=/home/me", "LOG_LEVEL=warn"}
	vars := []client.Variable{
		{Key: "LOG_LEVEL", Resolved: "debug"},
		{Key: "STRIPE_KEY", Resolved: "sk_test"},
		{Key: "DATABASE_URL", Resolved: "postgres://app:pw@svc-jn7ezbwt9755e1g1s3e7:5432/app"},
		{Key: "REDIS_URL", Resolved: "redis://:pw@svc-k3b7q2mx9wd4tz8hn5ra:6379"},
		{Key: "UPSTREAM", Resolved: "http://svc-api:8080"},
		{Key: "OTEL_SERVICE_NAME", Resolved: "api-custom"},
	}
	tracing := map[string]string{"OTEL_SERVICE_NAME": "api", "OTEL_TRACES_EXPORTER": "otlp"}
	env, skipped := runEnv(shell, vars, tracing, "https://keel.test/otlp")
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for k, want := range map[string]string{
		"LOG_LEVEL":                   "warn",
		"STRIPE_KEY":                  "sk_test",
		"OTEL_SERVICE_NAME":           "api-custom",
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://keel.test/otlp",
		"UPSTREAM":                    "http://svc-api:8080",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	_, hasDB := got["DATABASE_URL"]
	_, hasRedis := got["REDIS_URL"]
	if hasDB || hasRedis || !slices.Equal(skipped, []string{"DATABASE_URL", "REDIS_URL"}) {
		t.Errorf("cluster-only DATABASE_URL, REDIS_URL: env has them %v %v, skipped %v", hasDB, hasRedis, skipped)
	}
	if env, _ := runEnv(shell, nil, nil, "x"); len(env) != len(shell) {
		t.Errorf("no tracing: %v", env)
	}
	tracing["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Bearer%20keel_otlp_x"
	for _, own := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		env, _ := runEnv(append(slices.Clone(shell), own+"=https://collector.test"), nil, tracing, "x")
		if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "OTEL_EXPORTER_OTLP_HEADERS=") }) {
			t.Errorf("own %s: Keel's headers were added: %v", own, env)
		}
	}
	if env, _ := runEnv(shell, nil, tracing, "x"); !slices.Contains(env, "OTEL_EXPORTER_OTLP_HEADERS="+tracing["OTEL_EXPORTER_OTLP_HEADERS"]) {
		t.Errorf("Keel's endpoint: headers missing: %v", env)
	}
}

func TestFindService(t *testing.T) {
	services := []client.Service{{ID: "n1", Name: "api"}, {ID: "n2", Name: "postgres"}}
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
	at := func(sec int, text string) client.LogLine {
		return client.LogLine{Time: client.Time{Time: time.Unix(int64(sec), 0)}, Text: text}
	}
	s := newLineSet()
	var printed []string
	for _, poll := range [][]client.LogLine{
		{at(1, "a"), at(2, "b")},
		{at(1, "a"), at(2, "b"), at(2, "c"), at(3, "d")},
		{at(3, "d"), at(3, "d")},
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
