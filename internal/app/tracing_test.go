package app

import (
	"os"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

// The prompt text is byte-exact with convex/tracingPrompt.ts (testdata/*.txt were printed by
// running the TypeScript agentPrompt).
func TestAgentPromptMatchesTypeScript(t *testing.T) {
	for file, args := range map[string][2]string{
		"testdata/tracing_prompt_none.txt":    {"", ""},
		"testdata/tracing_prompt_service.txt": {"api", "shop"},
		"testdata/tracing_prompt_project.txt": {"", "shop"},
	} {
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if got := agentPrompt(args[0], args[1]); got != string(want) {
			t.Errorf("%s differs:\n%s", file, got)
		}
	}
}

func TestTracingEnv(t *testing.T) {
	node := domain.Node{ID: "n1", Name: "api"}
	env := domain.Environment{ID: "e/1", Name: "prod uction"}
	deployed := tracingEnv("http://x/otlp", node, env, "keel_otlp_K", false)
	want := [][2]string{
		{"OTEL_EXPORTER_OTLP_ENDPOINT", "http://x/otlp"},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf"},
		{"OTEL_EXPORTER_OTLP_HEADERS", "Authorization=Bearer%20keel_otlp_K"},
		{"OTEL_SERVICE_NAME", "api"},
		{"OTEL_RESOURCE_ATTRIBUTES", "keel.service_id=n1,keel.environment_id=e%2F1,deployment.environment.name=prod%20uction"},
		{"OTEL_TRACES_EXPORTER", "otlp"},
		{"OTEL_METRICS_EXPORTER", "none"},
		{"OTEL_LOGS_EXPORTER", "none"},
	}
	if !reflect.DeepEqual(deployed, want) {
		t.Fatalf("deployed env\n got %q\nwant %q", deployed, want)
	}
	local := tracingEnv("http://x/otlp", node, env, "keel_otlp_K", true)
	if len(local) != 7 || local[0][0] != "OTEL_EXPORTER_OTLP_PROTOCOL" ||
		local[3][1] != "keel.service_id=n1,keel.environment_id=e%2F1,deployment.environment.name=local" {
		t.Fatalf("local env %q", local)
	}
	for i, kv := range deployed {
		if TracingVarOrder(kv[0]) != i {
			t.Errorf("TracingVarOrder(%s) = %d", kv[0], TracingVarOrder(kv[0]))
		}
	}
	if TracingVarOrder("PATH") != -1 {
		t.Error("TracingVarOrder of a non-tracing key")
	}
}

func TestTracingOverridden(t *testing.T) {
	cases := []struct {
		own  []string
		key  string
		want bool
	}{
		{nil, "OTEL_SERVICE_NAME", false},
		{[]string{"OTEL_SERVICE_NAME"}, "OTEL_SERVICE_NAME", true},
		{[]string{"OTEL_EXPORTER_OTLP_ENDPOINT"}, "OTEL_EXPORTER_OTLP_HEADERS", true},
		{[]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"}, "OTEL_EXPORTER_OTLP_HEADERS", true},
		{[]string{"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"}, "OTEL_EXPORTER_OTLP_ENDPOINT", false},
		{[]string{"OTEL_EXPORTER_OTLP_HEADERS"}, "OTEL_EXPORTER_OTLP_HEADERS", true},
	}
	for _, c := range cases {
		own := map[string]bool{}
		for _, k := range c.own {
			own[k] = true
		}
		if got := tracingOverridden(own, c.key); got != c.want {
			t.Errorf("overridden(%v, %s) = %v", c.own, c.key, got)
		}
	}
}

// tracingTx serves the two reads withTracing makes; any other Tx method panics (nil embedded).
type tracingTx struct {
	Tx
	env domain.Environment
	key string
}

func (t tracingTx) Environment(id string) (domain.Environment, error) {
	if t.env.ID != id {
		return domain.Environment{}, ErrNoRow
	}
	return t.env, nil
}

func (t tracingTx) OTLPKeyOf(string) (string, error) {
	if t.key == "" {
		return "", ErrNoRow
	}
	return t.key, nil
}

func TestWithTracing(t *testing.T) {
	a := New(App{Config: Config{SiteURL: "https://keel.example.com"}})
	env := domain.Environment{ID: "env", Name: "production"}
	tx := tracingTx{env: env, key: "keel_otlp_secret"}
	node := domain.Node{ID: "n1", EnvironmentID: "env", Name: "api", Desired: &domain.Desired{Image: "x", Tracing: true}}
	own := map[string]string{"PORT": "8080", "OTEL_SERVICE_NAME": "mine"}

	got, err := a.withTracing(tx, node, own)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"PORT":                        "8080",
		"OTEL_SERVICE_NAME":           "mine",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "https://keel.example.com/otlp",
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/protobuf",
		"OTEL_EXPORTER_OTLP_HEADERS":  "Authorization=Bearer%20keel_otlp_secret",
		"OTEL_RESOURCE_ATTRIBUTES":    "keel.service_id=n1,keel.environment_id=env,deployment.environment.name=production",
		"OTEL_TRACES_EXPORTER":        "otlp",
		"OTEL_METRICS_EXPORTER":       "none",
		"OTEL_LOGS_EXPORTER":          "none",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("withTracing\n got %v\nwant %v", got, want)
	}
	if len(own) != 2 {
		t.Fatal("withTracing modified its input")
	}

	// The service's own endpoint: no ingest key is sent there.
	got, _ = a.withTracing(tx, node, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://mine"})
	if _, ok := got["OTEL_EXPORTER_OTLP_HEADERS"]; ok || got["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://mine" {
		t.Fatalf("own endpoint: %v", got)
	}

	// Off, no desired, no key, environment gone: unchanged.
	for name, c := range map[string]struct {
		tx   Tx
		node domain.Node
	}{
		"off":        {tx, domain.Node{ID: "n1", EnvironmentID: "env", Desired: &domain.Desired{}}},
		"no desired": {tx, domain.Node{ID: "n1", EnvironmentID: "env"}},
		"no key":     {tracingTx{env: env}, node},
		"no env":     {tracingTx{key: "k"}, node},
	} {
		got, err := a.withTracing(c.tx, c.node, map[string]string{"A": "1"})
		if err != nil || !reflect.DeepEqual(got, map[string]string{"A": "1"}) {
			t.Errorf("%s: %v %v", name, got, err)
		}
	}
}

func TestOTLPEndpoint(t *testing.T) {
	for _, c := range []struct{ otlp, site, want string }{
		{"", "https://keel.example.com", "https://keel.example.com/otlp"},
		{"http://100.64.0.1:3211/otlp/", "https://keel.example.com", "http://100.64.0.1:3211/otlp"},
		{"", "", "/otlp"},
	} {
		a := New(App{Config: Config{OTLPURL: c.otlp, SiteURL: c.site}})
		if got := a.otlpEndpoint(); got != c.want {
			t.Errorf("endpoint(%q, %q) = %q, want %q", c.otlp, c.site, got, c.want)
		}
	}
}
