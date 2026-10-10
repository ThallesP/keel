package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestCanvasEngineOf(t *testing.T) {
	cases := map[string]Engine{
		"postgres:16":                 EnginePostgres,
		"docker.io/library/mysql:8.4": EngineMySQL,
		"mongo":                       EngineMongo,
		"redis:7@sha256:abc":          EngineRedis,
		"ghcr.io/acme/redis-ui:1":     "",
		"localhost:5000/redis":        EngineRedis,
		"nginx":                       "",
		"":                            "",
		"a/constructor:1":             "",
	}
	for in, want := range cases {
		if got := EngineOf(in); got != want {
			t.Errorf("EngineOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanvasNameFromImage(t *testing.T) {
	long := strings.Repeat("a", 45)
	cases := []struct{ image, want string }{
		{"ghcr.io/acme/api-server:1.2", "api-server"},
		{"nginx:alpine", "nginx"},
		{"postgres@sha256:abc", "postgres"},
		{"my_app.v2:1", "my-app-v2"},
		{"__x__", "x"},
		{"___", "service"},
		{long, long[:40]},
		{strings.Repeat("a", 39) + "_b", strings.Repeat("a", 39) + "-"},
	}
	for _, c := range cases {
		if got := NameFromImage(c.image, "service"); got != c.want {
			t.Errorf("NameFromImage(%q) = %q, want %q", c.image, got, c.want)
		}
	}
}

func TestCanvasUniqueName(t *testing.T) {
	taken := map[string]bool{"api": true, "api-2": true, "web": true}
	cases := []struct{ base, want string }{
		{"db", "db"},
		{"api", "api-3"},
		{"web", "web-2"},
	}
	for _, c := range cases {
		if got := UniqueName(c.base, taken); got != c.want {
			t.Errorf("UniqueName(%q) = %q, want %q", c.base, got, c.want)
		}
	}
	base := strings.Repeat("x", 40)
	got := UniqueName(base, map[string]bool{base: true})
	if got != strings.Repeat("x", 38)+"-2" || ValidName(got) != nil {
		t.Errorf("UniqueName(40 chars) = %q", got)
	}
	copyBase := strings.Repeat("y", 32) + "-copy"
	if got := UniqueName(copyBase, map[string]bool{copyBase: true}); got != copyBase+"-2" {
		t.Errorf("copy name = %q", got)
	}
}

func TestCanvasNextPosition(t *testing.T) {
	w := 500.0
	cases := []struct {
		name  string
		nodes []Node
		want  Position
	}{
		{"empty", nil, Position{0, 0}},
		{"one", []Node{{Position: Position{10, 20}}}, Position{290, 20}},
		{"group width", []Node{{Position: Position{0, 5}, Config: NodeConfig{Width: &w}}, {Position: Position{100, 7}}}, Position{560, 5}},
		{"children ignored", []Node{{Position: Position{0, 0}}, {ParentID: "g", Position: Position{9000, 1}}}, Position{280, 0}},
		{"first wins ties", []Node{{Position: Position{0, 1}}, {Position: Position{0, 2}}}, Position{280, 1}},
	}
	for _, c := range cases {
		if got := NextPosition(c.nodes); got != c.want {
			t.Errorf("%s: NextPosition = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestCanvasPortAndReplicasNumbers(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	ports := []struct {
		in *float64
		ok bool
	}{{nil, true}, {f(80), true}, {f(80.0), true}, {f(80.5), false}, {f(0), false}, {f(1), true}, {f(65535), true}, {f(65536), false}, {f(-1), false}}
	for _, c := range ports {
		_, err := PortNumber(c.in)
		if (err == nil) != c.ok {
			t.Errorf("PortNumber(%v) err = %v", c.in, err)
		}
		if err != nil && err.Error() != "Port must be 1–65535" {
			t.Errorf("port message %q", err)
		}
	}
	reps := []struct {
		in *float64
		ok bool
	}{{nil, true}, {f(0), true}, {f(20), true}, {f(21), false}, {f(1.5), false}, {f(-1), false}}
	for _, c := range reps {
		_, err := ReplicasNumber(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ReplicasNumber(%v) err = %v", c.in, err)
		}
		if err != nil && (err.Error() != "Replicas must be 0–20" || CodeOf(err) != CodeInvalidInput) {
			t.Errorf("replicas error %q", err)
		}
	}
	if p, _ := PortNumber(f(8080)); p == nil || *p != 8080 {
		t.Errorf("PortNumber(8080) = %v", p)
	}
}

func TestCanvasSeedVariables(t *testing.T) {
	keys := func(e Engine) (out []string) {
		for _, v := range SeedVariables(e) {
			out = append(out, v.Key)
			if v.Secret != strings.Contains(v.Key, "PASSWORD") {
				t.Errorf("%s secret = %v", v.Key, v.Secret)
			}
			if v.Secret && len(v.Value) != 20 {
				t.Errorf("%s = %q", v.Key, v.Value)
			}
			if !v.Secret && v.Value != "app" {
				t.Errorf("%s = %q", v.Key, v.Value)
			}
		}
		return out
	}
	cases := map[Engine][]string{
		EnginePostgres: {"POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_DB"},
		EngineMySQL:    {"MYSQL_ROOT_PASSWORD", "MYSQL_USER", "MYSQL_PASSWORD", "MYSQL_DATABASE"},
		EngineMongo:    {"MONGO_INITDB_ROOT_USERNAME", "MONGO_INITDB_ROOT_PASSWORD"},
		EngineRedis:    {"REDIS_PASSWORD"},
		"":             nil,
	}
	for e, want := range cases {
		if got := keys(e); !reflect.DeepEqual(got, want) {
			t.Errorf("SeedVariables(%q) = %v, want %v", e, got, want)
		}
	}
}

func TestCanvasRandomSecret(t *testing.T) {
	seen := map[rune]bool{}
	for range 200 {
		s := RandomSecret(20)
		if len(s) != 20 {
			t.Fatalf("len %d", len(s))
		}
		for _, r := range s {
			if !strings.ContainsRune(canvasSecretAlphabet, r) {
				t.Fatalf("%q outside the alphabet", r)
			}
			seen[r] = true
		}
	}
	for _, r := range "loIO01" {
		if seen[r] {
			t.Errorf("ambiguous %q drawn", r)
		}
	}
	if len(seen) < 50 {
		t.Errorf("only %d distinct symbols", len(seen))
	}
}

func TestCanvasUTF16LenAndTrimJS(t *testing.T) {
	cases := map[string]int{"": 0, "abc": 3, "é": 1, "€": 1, "😀": 2, "a😀b": 4, string([]byte{0xff, 'a'}): 2}
	for in, want := range cases {
		if got := UTF16Len(in); got != want {
			t.Errorf("UTF16Len(%q) = %d, want %d", in, got, want)
		}
	}
	trims := map[string]string{
		"  My API \t\n":       "My API",
		"\ufeffx\u3000":       "x",
		"\u0085x":             "\u0085x",
		"\u00a0\u2028y\u200a": "y",
	}
	for in, want := range trims {
		if got := TrimJS(in); got != want {
			t.Errorf("TrimJS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanvasDefaultConfig(t *testing.T) {
	if c := DefaultConfig(NodeVolume); c.SizeGb == nil || *c.SizeGb != 10 || c.Width != nil {
		t.Errorf("volume config %+v", c)
	}
	if c := DefaultConfig(NodeGroup); c.Width == nil || *c.Width != 300 || *c.Height != 180 {
		t.Errorf("group config %+v", c)
	}
	if c := DefaultConfig(NodeService); c != (NodeConfig{}) {
		t.Errorf("service config %+v", c)
	}
}
