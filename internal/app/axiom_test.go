package app

import (
	"math"
	"strings"
	"testing"
)

func TestAPLLiteral(t *testing.T) {
	for in, want := range map[string]string{`abc`: `"abc"`, `a"b`: `"a\"b"`, `a\b`: `"a\\b"`, `\"`: `"\\\""`, ``: `""`} {
		if got := aplLit(in); got != want {
			t.Errorf("aplLit(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestAPLTime(t *testing.T) {
	for in, want := range map[float64]string{
		0:               "1970-01-01T00:00:00.000Z",
		1.7:             "1970-01-01T00:00:00.001Z",
		1791460812345.9: "2026-10-08T12:00:12.345Z",
		1791457320000:   "2026-10-08T11:02:00.000Z",
	} {
		if got := aplTime(in); got != want {
			t.Errorf("aplTime(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestClampTail(t *testing.T) {
	for in, want := range map[float64]int{200: 200, 200.9: 200, 0: 1, -5: 1, 1000: 1000, 1001: 1000, 5000: 1000, 0.5: 1, math.NaN(): 1} {
		if got := clampLogTail(in); got != want {
			t.Errorf("clampTail(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 200, "abc"},
		{"abc", 2, "ab"},
		{"é𝄞x", 2, "é𝄞"},
		{"", 3, ""},
		{"abc", 0, ""},
	} {
		if got := truncateRunes(c.in, c.n); got != c.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestCompactDetail(t *testing.T) {
	for in, want := range map[string]string{
		"  {\"message\":\n\t\"forbidden\"}  ":               `{"message": "forbidden"}`,
		"a\u00a0b\u2003c\u2028d":                            "a b c d",
		strings.Repeat("x", 250):                            strings.Repeat("x", 200),
		strings.Repeat("é", 150) + strings.Repeat("😀", 100): strings.Repeat("é", 150) + strings.Repeat("😀", 50),
		"":    "",
		"   ": "",
	} {
		if got := CompactDetail(in); got != want {
			t.Errorf("CompactDetail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAxiomJWTAudience(t *testing.T) {
	for token, want := range map[string]string{
		"h.eyJhdWQiOiJtY3AifQ.s":                 `"mcp"`,
		"h.eyJhdWQiOlsiYSIsImIiXX0.s":            `["a","b"]`,
		"h.e30.s":                                "null",
		"opaque":                                 "(not a JWT)",
		"h.!!.s":                                 "(not a JWT)",
		"h.eyJheGlvbURlZmF1bHRPcmciOjV9.s":       "(not a JWT)",
		"h.eyJhdWQiOiJtY3AifQ==.s":               `"mcp"`,
		"h.eyJhdWQiOiJtY3AiLCJ4IjoiPz8_In0.s":    `"mcp"`,
		"h.eyJhdWQiOiJtY3AiLCJ4IjoiPz8/In0.s":    `"mcp"`,
		"h.eyJheGlvbURlZmF1bHRPcmciOiJvMSJ9.sig": "null",
	} {
		if got := axiomJWTAudience(token); got != want {
			t.Errorf("axiomJWTAudience(%q) = %s, want %s", token, got, want)
		}
	}
	if got := axiomChosenOrg("h.eyJheGlvbURlZmF1bHRPcmciOiJvMSJ9.sig"); got != "o1" {
		t.Errorf("axiomChosenOrg = %q", got)
	}
	if got := axiomChosenOrg("opaque"); got != "" {
		t.Errorf("axiomChosenOrg(opaque) = %q", got)
	}
}
