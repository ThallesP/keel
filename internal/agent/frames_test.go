package agent

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// frame builds one multiplexed frame.
func frame(typ byte, payload string) []byte {
	b := make([]byte, 8, 8+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
	return append(b, payload...)
}

func TestFrameParser(t *testing.T) {
	two := append(frame(1, "a\n"), frame(2, "b\n")...)
	tests := []struct {
		name   string
		chunks [][]byte
		want   [][]Frame // per Push
	}{
		{"stdout", [][]byte{frame(1, "hello\n")}, [][]Frame{{{"stdout", "hello\n"}}}},
		{"stderr", [][]byte{frame(2, "oops\n")}, [][]Frame{{{"stderr", "oops\n"}}}},
		{"type 0 is stdout", [][]byte{frame(0, "x")}, [][]Frame{{{"stdout", "x"}}}},
		{"empty payload", [][]byte{frame(1, "")}, [][]Frame{{{"stdout", ""}}}},
		{"two frames in one chunk", [][]byte{two}, [][]Frame{{{"stdout", "a\n"}, {"stderr", "b\n"}}}},
		{"header split across chunks", [][]byte{two[:5], two[5:]}, [][]Frame{nil, {{"stdout", "a\n"}, {"stderr", "b\n"}}}},
		{"payload split across chunks", [][]byte{two[:9], two[9:13], two[13:]}, [][]Frame{nil, {{"stdout", "a\n"}}, {{"stderr", "b\n"}}}},
		{"tty: raw text", [][]byte{[]byte("hello world\n")}, [][]Frame{{{"stdout", "hello world\n"}}}},
		{"tty: under 8 bytes waits", [][]byte{[]byte("hi\n"), []byte("there\n")}, [][]Frame{nil, {{"stdout", "hi\nthere\n"}}}},
		{"frame then tty text", [][]byte{append(frame(1, "a"), []byte("raw text!")...)}, [][]Frame{{{"stdout", "a"}, {"stdout", "raw text!"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p FrameParser
			for i, c := range tt.chunks {
				got := p.Push(c)
				if !reflect.DeepEqual(got, tt.want[i]) {
					t.Fatalf("push %d = %#v, want %#v", i, got, tt.want[i])
				}
			}
		})
	}
}

func TestFrameParserDoesNotAliasInput(t *testing.T) {
	var p FrameParser
	buf := append(frame(1, "abc"), frame(1, "de")[:9]...) // second frame incomplete
	got := p.Push(buf)
	for i := range buf {
		buf[i] = 'Z'
	}
	if got[0].Text != "abc" {
		t.Fatalf("frame text changed with the input buffer: %q", got[0].Text)
	}
	if got := p.Push([]byte("e")); len(got) != 1 || got[0].Text != "de" {
		t.Fatalf("carry changed with the input buffer: %#v", got)
	}
}

func TestLineSplitter(t *testing.T) {
	var s LineSplitter
	steps := []struct {
		in   Frame
		want []Line
	}{
		{Frame{"stdout", "a"}, nil},
		{Frame{"stderr", "y\n"}, []Line{{Text: "y", Stream: "stderr"}}},
		{Frame{"stdout", "b\nc"}, []Line{{Text: "ab", Stream: "stdout"}}},
		{Frame{"stdout", "\n\n\nd\r\n"}, []Line{{Text: "c", Stream: "stdout"}, {Text: "d", Stream: "stdout"}}},
		{Frame{"stdout", "e\r\r\n"}, []Line{{Text: "e\r", Stream: "stdout"}}},
	}
	for i, st := range steps {
		if got := s.Push(st.in); !reflect.DeepEqual(got, st.want) {
			t.Fatalf("step %d = %#v, want %#v", i, got, st.want)
		}
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		raw        string
		time, text string
	}{
		{"2026-10-08T12:00:00.123456789Z hello world", "2026-10-08T12:00:00.123456789Z", "hello world"},
		{"2026-10-08T12:00:00Z x", "2026-10-08T12:00:00Z", "x"},
		{"2026-10-08T12:00:00.1Z ", "2026-10-08T12:00:00.1Z", ""},
		{"2026-10-08T12:00:00.1Z hi\r", "2026-10-08T12:00:00.1Z", "hi"},
		{"hello world", "", "hello world"},
		{"2026-10-08Z hi", "", "2026-10-08Z hi"},                                          // under 20 chars
		{"2026-10-08T12:00:00.123+00:00 hi", "", "2026-10-08T12:00:00.123+00:00 hi"},      // no Z
		{"20261-10-08T12:00:00.0Z x", "", "20261-10-08T12:00:00.0Z x"},                    // index 4 is not "-"
		{" 2026-10-08T12:00:00.123456789Z x", "", " 2026-10-08T12:00:00.123456789Z x"},    // space at 0
		{"2026-10-08T12:00:00.000000000Z", "", "2026-10-08T12:00:00.000000000Z"},          // no space
		{"2026-10-08T12:00:00.000000000Z a b ", "2026-10-08T12:00:00.000000000Z", "a b "}, // rest kept
	}
	for _, tt := range tests {
		got := parseLine(tt.raw, "stdout")
		if got.Time != tt.time || got.Text != tt.text {
			t.Errorf("parseLine(%q) = (%q, %q), want (%q, %q)", tt.raw, got.Time, got.Text, tt.time, tt.text)
		}
	}
}

// Expected values computed with the worker's TypeScript (node).
func TestSinceAfter(t *testing.T) {
	tests := map[string]string{
		"2024-01-01T00:00:00.123456789Z":  "1704067200.123456790",
		"2024-01-01T00:00:00Z":            "1704067200.000000001",
		"2024-01-01T00:00:00.5Z":          "1704067200.500000001",
		"2024-01-01T00:00:00.999999999Z":  "1704067201.000000000",
		"2024-01-01T00:00:00.000000000Z":  "1704067200.000000001",
		"1970-01-01T00:00:00.000000001Z":  "0.000000002",
		"2024-01-01T00:00:00.1234567890Z": "1704067200.000000001",
		"2026-10-08T12:34:56.789012345Z":  "1791462896.789012346",
		"not a stamp":                     "",
		"2024-01-01T00:00:00.123+01:00":   "",
		"garbageZ":                        "",
		"":                                "",
	}
	for in, want := range tests {
		if got := sinceAfter(in); got != want {
			t.Errorf("sinceAfter(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDockerSince(t *testing.T) {
	tests := []struct {
		ms   float64
		want string
	}{
		{1704067200000, "1704067200.000000000"},
		{1704067200000.25, "1704067200.000250000"},
		{1704067200123, "1704067200.123000000"},
		{1727600000000.123, "1727600000.000123047"}, // float ms, as JavaScript computes it
		{999.9999999999, "0.999999999"},             // clamped
		{1759912345678.25, "1759912345.678250000"},
		{0.5, "0.000500000"},
		{1.0000005, "0.001000001"},
	}
	for _, tt := range tests {
		if got := dockerSince(tt.ms); got != tt.want {
			t.Errorf("dockerSince(%v) = %q, want %q", tt.ms, got, tt.want)
		}
	}
}

func TestEventsSinceAfter(t *testing.T) {
	tests := map[int64]string{
		1704067200123456789: "1704067200.123456790",
		1704067200999999999: "1704067201.000000000",
		1:                   "0.000000002",
	}
	for in, want := range tests {
		if got := eventsSinceAfter(in); got != want {
			t.Errorf("eventsSinceAfter(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestReplicaOf(t *testing.T) {
	tests := map[string]int{
		"svc-x.3.abc": 3, "svc-x": 0, "": 0, "svc-x..abc": 0, "svc-x. 2 .t": 2,
		"svc-x.abc.t": 0, "svc-x.1.5": 1, "svc-x.NaN.t": 0, "svc-x.Inf.t": 0,
	}
	for in, want := range tests {
		if got := replicaOf(in); got != want {
			t.Errorf("replicaOf(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestServiceIDOf(t *testing.T) {
	tests := []struct {
		labels map[string]string
		id     string
		ok     bool
	}{
		{map[string]string{labelServiceName: "svc-abc"}, "abc", true},
		{map[string]string{labelServiceName: "svc-"}, "", true},
		{map[string]string{labelServiceName: "keel-agent"}, "", false},
		{map[string]string{}, "", false},
		{nil, "", false},
	}
	for _, tt := range tests {
		id, ok := serviceIDOf(tt.labels)
		if id != tt.id || ok != tt.ok {
			t.Errorf("serviceIDOf(%v) = (%q, %v), want (%q, %v)", tt.labels, id, ok, tt.id, tt.ok)
		}
	}
}

func TestSinkKey(t *testing.T) {
	tests := []struct {
		cfg  SinkConfig
		want string
	}{
		{SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel", Token: "xaat-123456789"}, "axiom:api.axiom.co:keel:456789"},
		{SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel", Token: "abc"}, "axiom:api.axiom.co:keel:abc"},
		{SinkConfig{Kind: "axiom", Domain: "api.axiom.co", Dataset: "keel", Token: "123456"}, "axiom:api.axiom.co:keel:123456"},
	}
	for _, tt := range tests {
		if got := sinkKey(tt.cfg); got != tt.want {
			t.Errorf("sinkKey(%+v) = %q, want %q", tt.cfg, got, tt.want)
		}
	}
}

func TestAxiomIngestURL(t *testing.T) {
	tests := []struct{ domain, dataset, want string }{
		{"api.axiom.co", "keel-logs", "https://api.axiom.co/v1/datasets/keel-logs/ingest"},
		{"api.eu.axiom.co", "keel", "https://api.eu.axiom.co/v1/datasets/keel/ingest"},
		{"eu-central-1.aws.edge.axiom.co", "keel-logs", "https://eu-central-1.aws.edge.axiom.co/v1/ingest/keel-logs"},
		{"http://127.0.0.1:9999/", "keel", "http://127.0.0.1:9999/v1/datasets/keel/ingest"},
		{"https://api.axiom.co//", "a b/c", "https://api.axiom.co/v1/datasets/a%20b%2Fc/ingest"},
	}
	for _, tt := range tests {
		if got := axiomIngestURL(tt.domain, tt.dataset); got != tt.want {
			t.Errorf("axiomIngestURL(%q, %q) = %q, want %q", tt.domain, tt.dataset, got, tt.want)
		}
	}
}

func TestEncodeURIComponent(t *testing.T) {
	tests := map[string]string{
		"keel-logs": "keel-logs", "a b/c": "a%20b%2Fc", "é": "%C3%A9",
		"a/b?c=d&e": "a%2Fb%3Fc%3Dd%26e", "abc-_.!~*'()": "abc-_.!~*'()",
	}
	for in, want := range tests {
		if got := encodeURIComponent(in); got != want {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorText(t *testing.T) {
	if got := errorText(errors.New("  a\n  b\tc  ")); got != "a b c" {
		t.Errorf("errorText = %q", got)
	}
	long := strings.Repeat("é", 400)
	if got := errorText(errors.New(long)); got != strings.Repeat("é", 300) {
		t.Errorf("errorText keeps %d runes, want 300", len([]rune(got)))
	}
}

func TestLoggerFormat(t *testing.T) {
	var buf bytes.Buffer
	l := NewLogger(&buf)
	l.now = func() time.Time { return time.Date(2024, 1, 1, 0, 0, 0, 5_000_000, time.FixedZone("x", 3600)) }
	l.Log("logs", "following svc-x (abc)", "since", "now")
	l.Log("axiom", "rejected 400, dropping 2 events", "text", "<a&b>")
	l.Log("logs", "config applied", "sinks", 1, "services", 2)
	l.Log("worker", "SIGTERM, flushing")
	want := `2023-12-31T23:00:00.005Z [logs] following svc-x (abc) {"since":"now"}
2023-12-31T23:00:00.005Z [axiom] rejected 400, dropping 2 events {"text":"<a&b>"}
2023-12-31T23:00:00.005Z [logs] config applied {"sinks":1,"services":2}
2023-12-31T23:00:00.005Z [worker] SIGTERM, flushing
`
	if buf.String() != want {
		t.Fatalf("log =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestLogEventShape(t *testing.T) {
	ev := LogEvent{
		Time: "2024-01-01T00:00:00.000000001Z", Message: "<b>&é", Stream: "stdout", ServiceID: "n1",
		Service: "svc-n1", Task: "t1", Replica: 1, Node: "node1", Container: "abcdef123456",
	}
	want := `{"_time":"2024-01-01T00:00:00.000000001Z","message":"<b>&é","stream":"stdout","service_id":"n1","service":"svc-n1","task":"t1","replica":1,"node":"node1","container":"abcdef123456"}`
	if got := string(marshal(ev)); got != want {
		t.Fatalf("event = %s\nwant    %s", got, want)
	}
}
