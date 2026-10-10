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
		want   [][]Frame
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
	buf := append(frame(1, "abc"), frame(1, "de")[:9]...)
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
		{"2026-10-08T12:00:00.123+00:00 hi", "2026-10-08T12:00:00.123Z", "hi"},
		{"hello world", "", "hello world"},
		{"2026-10-08Z hi", "", "2026-10-08Z hi"},
		{"20261-10-08T12:00:00.0Z x", "", "20261-10-08T12:00:00.0Z x"},
		{" 2026-10-08T12:00:00.123456789Z x", "", " 2026-10-08T12:00:00.123456789Z x"},
		{"2026-10-08T12:00:00.000000000Z", "", "2026-10-08T12:00:00.000000000Z"},
		{"2026-10-08T12:00:00.000000000Z a b ", "2026-10-08T12:00:00Z", "a b "},
	}
	for _, tt := range tests {
		want, _ := time.Parse(time.RFC3339Nano, tt.time)
		if got := parseLine(tt.raw, "stdout"); !got.Time.Equal(want) || got.Text != tt.text {
			t.Errorf("parseLine(%q) = (%v, %q), want (%v, %q)", tt.raw, got.Time, got.Text, want, tt.text)
		}
	}
}

func TestDockerTime(t *testing.T) {
	tests := map[string]time.Time{
		"1704067200.123456790": time.Unix(1704067200, 123456790),
		"1704067200.123000000": time.UnixMilli(1704067200123),
		"0.000000002":          time.Unix(0, 2),
	}
	for want, in := range tests {
		if got := dockerTime(in); got != want {
			t.Errorf("dockerTime(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestReplicaOf(t *testing.T) {
	tests := map[string]int{"svc-x.3.abc": 3, "svc-x": 0, "": 0, "svc-x..abc": 0, "svc-x.abc.t": 0, "svc-x.1.5": 1}
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
		{map[string]string{labelServiceName: "keel-agent"}, "keel-agent", false},
		{nil, "", false},
	}
	for _, tt := range tests {
		id, ok := serviceIDOf(tt.labels)
		if id != tt.id || ok != tt.ok {
			t.Errorf("serviceIDOf(%v) = (%q, %v), want (%q, %v)", tt.labels, id, ok, tt.id, tt.ok)
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
	l.Log("worker", "SIGTERM, flushing")
	if want := "2023-12-31T23:00:00.005Z [worker] SIGTERM, flushing\n"; buf.String() != want {
		t.Fatalf("log = %q, want %q", buf.String(), want)
	}
}
