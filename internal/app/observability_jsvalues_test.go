package app

// JS semantics the providers rely on, against what Node printed for the same inputs
// (testdata/js_semantics.golden.json).

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sort"
	"testing"
)

type jsGolden struct {
	Dates   [][2]any          `json:"dates"`
	Numbers [][2]any          `json:"numbers"`
	Floats  [][2]any          `json:"floats"`
	Sorted  []string          `json:"sorted"`
	Words   []string          `json:"words"`
	Details [][2]string       `json:"details"`
	Encode  [][2]string       `json:"encode"`
	Form    []json.RawMessage `json:"form"`
	JSONs   [][2]string       `json:"jsons"`
	Strings [][2]string       `json:"strings"`
	ISO     [][2]any          `json:"iso"`
}

func loadJSGolden(t *testing.T) jsGolden {
	t.Helper()
	b, err := os.ReadFile("testdata/js_semantics.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var g jsGolden
	if err := json.Unmarshal(b, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestJSDateParse(t *testing.T) {
	for _, c := range loadJSGolden(t).Dates {
		in := c[0].(string)
		got, ok := jsDateParse(in)
		if c[1] == nil {
			if ok {
				t.Errorf("Date.parse(%q) = %v, want NaN", in, got)
			}
			continue
		}
		if !ok || got != c[1].(float64) {
			t.Errorf("Date.parse(%q) = %v (%v), want %v", in, got, ok, c[1])
		}
	}
}

func TestJSNumber(t *testing.T) {
	for _, c := range loadJSGolden(t).Numbers {
		in := c[0].(string)
		got := jsNumber(in)
		switch want := c[1].(type) {
		case string:
			ok := (want == "NaN" && math.IsNaN(got)) || (want == "Infinity" && math.IsInf(got, 1)) || (want == "-Infinity" && math.IsInf(got, -1))
			if !ok {
				t.Errorf("Number(%q) = %v, want %s", in, got, want)
			}
		case float64:
			if got != want {
				t.Errorf("Number(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

func TestJSNumberString(t *testing.T) {
	for _, c := range loadJSGolden(t).Floats {
		if got := jsNumberString(c[0].(float64)); got != c[1].(string) {
			t.Errorf("String(%v) = %q, want %q", c[0], got, c[1])
		}
	}
	for x, want := range map[float64]string{math.NaN(): "NaN", math.Inf(1): "Infinity", math.Inf(-1): "-Infinity", 0: "0"} {
		if got := jsNumberString(x); got != want {
			t.Errorf("String(%v) = %q, want %q", x, got, want)
		}
	}
}

func TestLocaleCompareSort(t *testing.T) {
	g := loadJSGolden(t)
	got := append([]string(nil), g.Words...)
	sort.SliceStable(got, func(i, j int) bool { return localeCompare(got[i], got[j]) < 0 })
	if !reflect.DeepEqual(got, g.Sorted) {
		t.Errorf("localeCompare order\n got %q\nwant %q", got, g.Sorted)
	}
}

func TestCompactDetail(t *testing.T) {
	for _, c := range loadJSGolden(t).Details {
		if got := CompactDetail(c[0]); got != c[1] {
			t.Errorf("CompactDetail(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestJSEncoders(t *testing.T) {
	g := loadJSGolden(t)
	for _, c := range g.Encode {
		if got := jsEncodeURIComponent(c[0]); got != c[1] {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	var pairs [][2]string
	var want string
	_ = json.Unmarshal(g.Form[0], &pairs)
	_ = json.Unmarshal(g.Form[1], &want)
	if got := jsFormEncode(pairs); got != want {
		t.Errorf("URLSearchParams = %q, want %q", got, want)
	}
}

func TestJSONOrderAndStringify(t *testing.T) {
	g := loadJSGolden(t)
	for _, c := range g.JSONs {
		v, err := DecodeJSON([]byte(c[0]))
		if err != nil {
			t.Fatal(err)
		}
		if got := jsStringify(v); got != c[1] {
			t.Errorf("JSON.stringify(JSON.parse(%s)) = %s, want %s", c[0], got, c[1])
		}
	}
	for _, c := range g.Strings {
		v, err := DecodeJSON([]byte(c[0]))
		if err != nil {
			t.Fatal(err)
		}
		if got := jsString(v); got != c[1] {
			t.Errorf("str(%s) = %q, want %q", c[0], got, c[1])
		}
	}
	if _, err := DecodeJSON([]byte(`{"a":1} x`)); err == nil {
		t.Error("trailing data accepted")
	}
	// Duplicate keys: first position, last value (JSON.parse).
	v, _ := DecodeJSON([]byte(`{"a":1,"b":2,"a":3}`))
	if got := jsStringify(v); got != `{"a":3,"b":2}` {
		t.Errorf("duplicate keys: %s", got)
	}
}

func TestISOTime(t *testing.T) {
	for _, c := range loadJSGolden(t).ISO {
		if got := jsISOTime(c[0].(float64)); got != c[1].(string) {
			t.Errorf("toISOString(%v) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestPreciseTimeAndSlice(t *testing.T) {
	cases := map[string]float64{
		"2026-10-08T12:00:00.123456789Z": 1791460800123 + 0.456789,
		"2026-10-08T12:00:00.123Z":       1791460800123,
		"2026-10-08T12:00:00Z":           1791460800000,
		"bad":                            0,
	}
	for in, want := range cases {
		if got := axiomPreciseTime(in); got != want {
			t.Errorf("preciseTime(%q) = %v, want %v", in, got, want)
		}
	}
	if got := jsSlice("é𝄞x", 2); got != "é" {
		t.Errorf("jsSlice split surrogate: %q", got)
	}
	if got := jsSlice("abc", 200); got != "abc" {
		t.Errorf("jsSlice short: %q", got)
	}
}

func TestAPLLiteral(t *testing.T) {
	for in, want := range map[string]string{`abc`: `"abc"`, `a"b`: `"a\"b"`, `a\b`: `"a\\b"`, `\"`: `"\\\""`, ``: `""`} {
		if got := aplLit(in); got != want {
			t.Errorf("aplLit(%q) = %s, want %s", in, got, want)
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
