package domain

import (
	"errors"
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
	cases := []struct {
		name  string
		nodes []Node
		want  Position
	}{
		{"empty", nil, Position{0, 0}},
		{"one", []Node{{Position: Position{10, 20}}}, Position{290, 20}},
		{"group width", []Node{{Position: Position{0, 5}, Config: NodeConfig{Width: new(500.0)}}, {Position: Position{100, 7}}}, Position{560, 5}},
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
	ports := []struct {
		in *int
		ok bool
	}{{nil, true}, {new(80), true}, {new(0), false}, {new(1), true}, {new(65535), true}, {new(65536), false}, {new(-1), false}}
	for _, c := range ports {
		err := ValidPort(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ValidPort(%v) err = %v", c.in, err)
		}
		if err != nil && err.Error() != "Port must be 1–65535" {
			t.Errorf("port message %q", err)
		}
	}
	reps := []struct {
		in *int
		ok bool
	}{{nil, true}, {new(0), true}, {new(20), true}, {new(21), false}, {new(-1), false}}
	for _, c := range reps {
		err := ValidReplicas(c.in)
		if (err == nil) != c.ok {
			t.Errorf("ValidReplicas(%v) err = %v", c.in, err)
		}
		if de, ok := errors.AsType[*Error](err); err != nil && (!ok || *de != (Error{Code: CodeInvalidInput, Message: "Replicas must be 0–20"})) {
			t.Errorf("replicas error %q", err)
		}
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
