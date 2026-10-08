package app

// Span row parsing and Docker demux against the TypeScript (testdata/axiom_scenarios.golden.json,
// produced by running traceProviders/axiom.ts spanOf and logProviders/docker.ts demux on the
// same inputs).

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func loadScenarioFiles(t *testing.T) (fixture struct {
	SpanRows []json.RawMessage `json:"spanRows"`
	Demux    []struct {
		Name   string  `json:"name"`
		Raw    *string `json:"raw"`
		Frames [][2]any
		Tail   string `json:"tail"`
	} `json:"demux"`
}, golden struct {
	Spans []json.RawMessage `json:"spans"`
	Demux []struct {
		Name  string          `json:"name"`
		Lines json.RawMessage `json:"lines"`
	} `json:"demux"`
}) {
	t.Helper()
	for path, v := range map[string]any{"testdata/axiom_scenarios.json": &fixture, "testdata/axiom_scenarios.golden.json": &golden} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	return fixture, golden
}

func jsonEqual(t *testing.T, got any, want json.RawMessage) bool {
	t.Helper()
	gb, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var g, w any
	_ = json.Unmarshal(gb, &g)
	_ = json.Unmarshal(want, &w)
	return reflect.DeepEqual(g, w)
}

func TestSpanOfMatchesTypeScript(t *testing.T) {
	fx, golden := loadScenarioFiles(t)
	for i, raw := range fx.SpanRows {
		v, err := DecodeJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		got := spanOf(v.(*JSONObject))
		if !jsonEqual(t, got, golden.Spans[i]) {
			gb, _ := json.MarshalIndent(got, "", " ")
			t.Errorf("span row %d\n got: %s\nwant: %s", i, gb, golden.Spans[i])
		}
	}
}

func TestDemuxMatchesTypeScript(t *testing.T) {
	fx, golden := loadScenarioFiles(t)
	for i, d := range fx.Demux {
		var buf bytes.Buffer
		if d.Raw != nil {
			buf.WriteString(*d.Raw)
		}
		for _, f := range d.Frames {
			payload := []byte(f[1].(string))
			head := make([]byte, 8)
			head[0] = byte(f[0].(float64))
			binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
			buf.Write(head)
			buf.Write(payload)
		}
		if d.Tail != "" {
			b, _ := base64.StdEncoding.DecodeString(d.Tail)
			buf.Write(b)
		}
		got := demuxDockerLogs(buf.Bytes())
		if golden.Demux[i].Name != d.Name {
			t.Fatalf("golden out of order")
		}
		if !jsonEqual(t, got, golden.Demux[i].Lines) {
			gb, _ := json.Marshal(got)
			t.Errorf("%s\n got: %s\nwant: %s", d.Name, gb, golden.Demux[i].Lines)
		}
	}
}

func TestDurationAndTime(t *testing.T) {
	durations := []struct {
		in   any
		want float64
	}{
		{float64(1_500_000), 1.5},
		{"1500000", 1.5},
		{"1.5", 1.5e-6},
		{"5ms", 5},
		{"1m30.5s", 90_500},
		{"2h", 7_200_000},
		{"250µs", 0.25},
		{"250μs", 0.25},
		{"250us", 0.25},
		{"10ns", 9.999999999999999e-06}, // 10 * 1e-6 in float64, as JS computes it
		{"00:00:01.5", 1500},
		{"1.00:00:00", 86_400_000},
		{"garbage", 0},
		{"", 0},
		{nil, 0},
		{true, 0},
	}
	for _, c := range durations {
		if got := durationOf(c.in); got != c.want {
			t.Errorf("durationOf(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	times := []struct {
		in   any
		want float64
	}{
		{float64(1_791_460_800_123), 1_791_460_800_123},
		{float64(1_791_460_800_123_456), 1_791_460_800_123.456},
		{1.791460800123456789e18, 1.791460800123456789e18 / 1e6},
		{"1791460800123", 1_791_460_800_123},
		{"2026-10-08T12:00:00.5Z", 1_791_460_800_500},
		{"nope", 0},
		{"", 0},
		{nil, 0},
	}
	for _, c := range times {
		if got := timeOf(c.in); got != c.want {
			t.Errorf("timeOf(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	for in, want := range map[any]string{"SPAN_KIND_SERVER": "server", "Client": "client", "span_kind_unspecified": "", "unspecified": "", nil: "", float64(3): "3"} {
		if got := kindOf(in); got != want {
			t.Errorf("kindOf(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestPick(t *testing.T) {
	v, _ := DecodeJSON([]byte(`{"a.b":1,"a":{"b":2,"c":{"d":3}},"x":null,"x.y":4,"l":[{"k":5}]}`))
	row := v.(*JSONObject)
	cases := []struct {
		path string
		want any
		ok   bool
	}{
		{"a.b", float64(1), true},
		{"a.c.d", float64(3), true},
		{"x", nil, true},
		{"x.y", float64(4), true},
		{"missing", nil, false},
		{"l.0.k", float64(5), true},
		{".a", nil, false},
	}
	for _, c := range cases {
		got, ok := pick(row, c.path)
		if ok != c.ok || !reflect.DeepEqual(got, c.want) {
			t.Errorf("pick(%q) = %v, %v; want %v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
	// A present null ends the search: "n.y" is not looked for in "n" once "n.y" is null.
	v, _ = DecodeJSON([]byte(`{"n.y":null,"n":{"y":1}}`))
	if got, ok := pick(v, "n.y"); !ok || got != nil {
		t.Errorf("present null: %v %v", got, ok)
	}
	if _, ok := pick("str", "a"); ok {
		t.Error("pick on a string")
	}
}
