package app

// JavaScript semantics the observability read side depends on. The TypeScript providers parsed
// Axiom rows with JS rules (Number(), String(), Date.parse, JSON key order, localeCompare, \s);
// the Go port reproduces them here so the same rows give the same JSON
// (docs/go/spec/observability.md §3, §7).

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"

	"github.com/ThallesP/keel/internal/domain"
)

// JSONObject is a decoded JSON object that keeps JavaScript's property order: array-index keys
// ascending, then the others in insertion order. A duplicate key keeps its first position and
// its last value (JSON.parse). Values are nil (null), bool, float64, string, []any or
// *JSONObject.
type JSONObject struct {
	keys []string
	vals map[string]any
}

// NewJSONObject is an empty object.
func NewJSONObject() *JSONObject { return &JSONObject{vals: map[string]any{}} }

// Set adds or replaces a property.
func (o *JSONObject) Set(key string, v any) {
	if o.vals == nil {
		o.vals = map[string]any{}
	}
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

// Get is the property's value; ok=false when absent (JS undefined).
func (o *JSONObject) Get(key string) (any, bool) {
	if o == nil {
		return nil, false
	}
	v, ok := o.vals[key]
	return v, ok
}

// Keys in JavaScript property order (Object.keys / Object.entries).
func (o *JSONObject) Keys() []string {
	if o == nil {
		return nil
	}
	var index, other []string
	for _, k := range o.keys {
		if _, ok := jsArrayIndex(k); ok {
			index = append(index, k)
		} else {
			other = append(other, k)
		}
	}
	if len(index) == 0 {
		return other
	}
	sort.Slice(index, func(i, j int) bool {
		a, _ := jsArrayIndex(index[i])
		b, _ := jsArrayIndex(index[j])
		return a < b
	})
	return append(index, other...)
}

// jsArrayIndex: k is a canonical array index ("0", "17"; not "01", not ≥ 2^32-1).
func jsArrayIndex(k string) (int, bool) {
	if k == "" || len(k) > 10 || (len(k) > 1 && k[0] == '0') {
		return 0, false
	}
	n := 0
	for i := 0; i < len(k); i++ {
		if k[i] < '0' || k[i] > '9' {
			return 0, false
		}
		n = n*10 + int(k[i]-'0')
	}
	if n >= math.MaxUint32 {
		return 0, false
	}
	return n, true
}

// DecodeJSON decodes one JSON value into nil, bool, float64, string, []any and *JSONObject.
func DecodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON: trailing data")
	}
	return v, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := NewJSONObject()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k, _ := kt.(string)
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				obj.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				v, err := decodeJSONValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return arr, nil
		}
		return nil, errors.New("invalid JSON")
	case json.Number:
		f, err := strconv.ParseFloat(string(t), 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return nil, err
		}
		return f, nil
	default: // string, bool, nil
		return t, nil
	}
}

// jsGet is JS property access obj[key] on a decoded value; ok=false is undefined.
func jsGet(obj any, key string) (any, bool) {
	switch o := obj.(type) {
	case *JSONObject:
		return o.Get(key)
	case []any:
		if key == "length" {
			return float64(len(o)), true
		}
		if i, ok := jsArrayIndex(key); ok && i < len(o) {
			return o[i], true
		}
	}
	return nil, false
}

// jsTruthy is JS truthiness.
func jsTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// jsString is the providers' str(): a string as is, null/undefined → "", else String(x).
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return jsNumberString(x)
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = jsString(e) // Array.prototype.join: null/undefined → ""
		}
		return strings.Join(parts, ",")
	case *JSONObject:
		return "[object Object]"
	}
	return ""
}

// jsNumberString is Number.prototype.toString().
func jsNumberString(x float64) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	case x == 0:
		return "0"
	}
	abs := math.Abs(x)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(x, 'e', -1, 64) // 1.5e-07 → 1.5e-7, 1e+21 stays
		mant, exp, _ := strings.Cut(s, "e")
		sign := exp[:1]
		exp = strings.TrimLeft(exp[1:], "0")
		if exp == "" {
			exp = "0"
		}
		return mant + "e" + sign + exp
	}
	return strconv.FormatFloat(x, 'f', -1, 64)
}

var jsDecimalRE = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*(?:[eE][+-]?[0-9]+)?|\.[0-9]+(?:[eE][+-]?[0-9]+)?)$`)

// jsNumber is Number(x). nil (null and undefined alike) → 0.
func jsNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		return jsNumberFromString(x)
	case []any:
		return jsNumberFromString(jsString(x))
	}
	return math.NaN()
}

func jsNumberFromString(s string) float64 {
	s = domain.TrimJS(s)
	switch s {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			val := 0.0
			for _, c := range s[2:] {
				d := strings.IndexRune("0123456789abcdef", toLowerASCII(c))
				if d < 0 || d >= base {
					return math.NaN()
				}
				val = val*float64(base) + float64(d)
			}
			return val
		}
	}
	if !jsDecimalRE.MatchString(s) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return math.NaN()
	}
	return f
}

func toLowerASCII(c rune) rune {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// jsNum is the providers' num(): a number as is, else Number(x) || 0.
func jsNum(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	f := jsNumber(v)
	if math.IsNaN(f) {
		return 0
	}
	return f
}

// jsStringify is JSON.stringify for decoded values.
func jsStringify(v any) string {
	var b strings.Builder
	writeJSValue(&b, v)
	return b.String()
}

func writeJSValue(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(jsString(x))
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			b.WriteString("null")
		} else {
			b.WriteString(jsNumberString(x))
		}
	case string:
		writeJSQuoted(b, x)
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSValue(b, e)
		}
		b.WriteByte(']')
	case *JSONObject:
		b.WriteByte('{')
		for i, k := range x.Keys() {
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSQuoted(b, k)
			b.WriteByte(':')
			val, _ := x.Get(k)
			writeJSValue(b, val)
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

// writeJSQuoted is JSON.stringify of a string: only ", \ and control characters are escaped.
func writeJSQuoted(b *strings.Builder, s string) {
	const hex = "0123456789abcdef"
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte(hex[r>>4])
				b.WriteByte(hex[r&0xf])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}

// jsSpaceClass is JS \s (WhiteSpace and LineTerminator).
const jsSpaceClass = `[\t\n\v\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var jsSpaceRunRE = regexp.MustCompile(jsSpaceClass + `+`)

// jsSlice is s.slice(0, n): at most n UTF-16 code units (a split surrogate pair is dropped).
func jsSlice(s string, n int) string {
	units := 0
	for i, r := range s {
		w := 1
		if r > 0xffff {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}

// CompactDetail is how an upstream error body is quoted: every whitespace run collapsed to one
// space, trimmed, at most 200 characters (UTF-16 units, like JS).
func CompactDetail(body string) string {
	if !utf8.ValidString(body) {
		body = strings.ToValidUTF8(body, "�")
	}
	return jsSlice(domain.TrimJS(jsSpaceRunRE.ReplaceAllString(body, " ")), 200)
}

// jsEncodeURIComponent is encodeURIComponent.
func jsEncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

// jsFormEncode is URLSearchParams.toString(): application/x-www-form-urlencoded, keys in order.
func jsFormEncode(pairs [][2]string) string {
	const hex = "0123456789ABCDEF"
	enc := func(b *strings.Builder, s string) {
		for i := 0; i < len(s); i++ {
			c := s[i]
			switch {
			case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("*-._", c) >= 0:
				b.WriteByte(c)
			case c == ' ':
				b.WriteByte('+')
			default:
				b.WriteByte('%')
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
			}
		}
	}
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		enc(&b, p[0])
		b.WriteByte('=')
		enc(&b, p[1])
	}
	return b.String()
}

var (
	jsCollatorMu sync.Mutex
	jsCollator   = collate.New(language.English)
)

// localeCompare is a.localeCompare(b) (ICU root/English collation).
func localeCompare(a, b string) int {
	jsCollatorMu.Lock()
	defer jsCollatorMu.Unlock()
	return jsCollator.CompareString(a, b)
}

// jsDateRE is the date-time strings Date.parse accepts that Axiom and Docker produce: ISO 8601
// date or date-time (T, t or a space), seconds and any number of fraction digits optional, Z or
// an offset (±HH:mm or ±HHmm). A date-time without an offset is UTC (Convex ran in UTC).
var jsDateRE = regexp.MustCompile(`^([+-][0-9]{6}|[0-9]{4})(?:-([0-9]{2})(?:-([0-9]{2}))?)?` +
	`(?:[Tt ]([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]+))?)?)?` +
	`([Zz]|[+-][0-9]{2}:?[0-9]{2})?$`)

// jsDateParse is Date.parse for those formats (ms since the epoch); ok=false is NaN.
func jsDateParse(s string) (float64, bool) {
	m := jsDateRE.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	atoi := func(x string, def int) int {
		if x == "" {
			return def
		}
		n, _ := strconv.Atoi(x)
		return n
	}
	year := atoi(strings.TrimPrefix(m[1], "+"), 0)
	month, day := atoi(m[2], 1), atoi(m[3], 1)
	hour, minute, sec := atoi(m[4], 0), atoi(m[5], 0), atoi(m[6], 0)
	ms := 0
	if m[7] != "" {
		frac := (m[7] + "00")[:3]
		ms = atoi(frac, 0)
	}
	if month < 1 || month > 12 || day < 1 || day > 31 || minute > 59 || sec > 59 {
		return 0, false
	}
	if hour > 24 || (hour == 24 && (minute != 0 || sec != 0 || strings.Trim(m[7], "0") != "")) {
		return 0, false
	}
	t := time.Date(year, time.Month(month), day, hour, minute, sec, 0, time.UTC).UnixMilli() + int64(ms)
	if off := m[8]; off != "" && off != "Z" && off != "z" {
		digits := strings.ReplaceAll(off[1:], ":", "")
		oh, om := atoi(digits[:2], 0), atoi(digits[2:], 0)
		if oh > 23 || om > 59 {
			return 0, false
		}
		delta := int64(oh*60+om) * 60_000
		if off[0] == '+' {
			t -= delta
		} else {
			t += delta
		}
	}
	if t > 8.64e15 || t < -8.64e15 {
		return 0, false
	}
	return float64(t), true
}

var jsSubMsRE = regexp.MustCompile(`\.[0-9]{3}([0-9]+)`)

// axiomPreciseTime is Date.parse plus the sub-millisecond digits of the stamp; unparseable → 0.
func axiomPreciseTime(v any) float64 {
	iso := jsString(v)
	ms, ok := jsDateParse(iso)
	if !ok {
		return 0
	}
	if m := jsSubMsRE.FindStringSubmatch(iso); m != nil {
		frac, _ := strconv.ParseFloat("0."+m[1], 64)
		return ms + frac
	}
	return ms
}

// jsISOTime is new Date(ms).toISOString() (the time value truncated toward zero).
func jsISOTime(ms float64) string {
	return time.UnixMilli(int64(math.Trunc(ms))).UTC().Format("2006-01-02T15:04:05.000Z")
}
