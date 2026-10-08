package app

// Axiom span rows → spans (traceProviders/axiom.ts, "Parsing"). Rows come back with every field
// of the dataset as a column, dotted names flat (attributes.http.method), maps as objects
// (attributes.custom). These helpers also take nested objects and string-typed numbers, so a
// change in how Axiom serialises does not blank the page. Must match the TS exactly
// (docs/go/spec/observability.md §7).

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

// pick is path from a row: a flat dotted key, or the same path through nested objects/maps.
// ok=false is undefined; a present null is (nil, true) and ends the search.
func pick(obj any, path string) (any, bool) {
	switch obj.(type) {
	case *JSONObject, []any:
	default:
		return nil, false
	}
	if v, ok := jsGet(obj, path); ok {
		return v, true
	}
	i := strings.IndexByte(path, '.')
	for i > 0 {
		head := path[:i]
		if hv, ok := jsGet(obj, head); ok {
			if found, ok := pick(hv, path[i+1:]); ok {
				return found, true
			}
		}
		j := strings.IndexByte(path[i+1:], '.')
		if j < 0 {
			break
		}
		i = i + 1 + j
	}
	return nil, false
}

// pickValue is pick without the undefined/null distinction.
func pickValue(obj any, path string) any {
	v, _ := pick(obj, path)
	return v
}

// attr is a span attribute, whether Axiom filed it under its semantic conventions or custom.
func attr(row *JSONObject, name string) any {
	if v := pickValue(row, "attributes."+name); v != nil {
		return v
	}
	return pickValue(row, "attributes.custom."+name)
}

var digitsRE = regexp.MustCompile(`^[0-9]+$`)

// timeOf: RFC 3339 with up to nanoseconds, or epoch ns / µs / ms → epoch ms (fractional).
func timeOf(v any) float64 {
	switch x := v.(type) {
	case float64:
		if x > 1e17 {
			return x / 1e6
		}
		if x > 1e14 {
			return x / 1e3
		}
		return x
	case string:
		if x == "" {
			return 0
		}
		if digitsRE.MatchString(x) {
			return timeOf(jsNumber(x))
		}
		return preciseTime(x)
	}
	return 0
}

var (
	decimalRE  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	unitRE     = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)(ns|us|µs|μs|ms|h|m|s)`)
	dotnetRE   = regexp.MustCompile(`^(?:([0-9]+)\.)?([0-9]+):([0-9]+):([0-9]+(?:\.[0-9]+)?)$`)
	unitToMs   = map[string]float64{"ns": 1e-6, "us": 1e-3, "µs": 1e-3, "μs": 1e-3, "ms": 1, "s": 1000, "m": 60_000, "h": 3_600_000}
	parseFloat = func(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
)

// durationOf: nanoseconds (Axiom's OTel duration), a Go duration string (1m30.5s, 5ms) or .NET
// [d.]hh:mm:ss[.f] → ms; anything else 0.
func durationOf(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x / 1e6
	case string:
		if x == "" {
			return 0
		}
		if decimalRE.MatchString(x) {
			return parseFloat(x) / 1e6
		}
		total, matched := 0.0, false
		for _, m := range unitRE.FindAllStringSubmatch(x, -1) {
			matched = true
			total += parseFloat(m[1]) * unitToMs[m[2]]
		}
		if matched {
			return total
		}
		t := dotnetRE.FindStringSubmatch(x)
		if t == nil {
			return 0
		}
		hours := parseFloat(t[1])*24 + parseFloat(t[2])
		return (hours*3600 + parseFloat(t[3])*60 + parseFloat(t[4])) * 1000
	}
	return 0
}

// msOrNull: null or "" → null, else durationOf.
func msOrNull(v any) *float64 {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	d := durationOf(v)
	return &d
}

func statsOf(r *JSONObject) domain.TraceStats {
	get := func(k string) any { v, _ := r.Get(k); return v }
	return domain.TraceStats{
		Requests: num(get("requests")),
		Errors:   num(get("errors")),
		P50:      msOrNull(get("p50")),
		P95:      msOrNull(get("p95")),
		P99:      msOrNull(get("p99")),
	}
}

// kindOf: SPAN_KIND_SERVER, Server, server → server; unspecified → "".
func kindOf(v any) string {
	k := strings.TrimPrefix(strings.ToLower(jsString(v)), "span_kind_")
	if k == "unspecified" {
		return ""
	}
	return k
}

// statusOf is error (Axiom's error flag, or a status code containing "error"), ok or unset.
func statusOf(row *JSONObject) string {
	code := strings.ToLower(jsString(pickValue(row, "status.code")))
	if e, ok := pickValue(row, "error").(bool); (ok && e) || strings.Contains(code, "error") {
		return "error"
	}
	if strings.Contains(code, "ok") {
		return "ok"
	}
	return "unset"
}

// strMap is a JS Map<string, string>: insertion order, a set on an existing key keeps its place.
type strMap struct {
	keys []string
	vals map[string]string
}

func newStrMap() *strMap { return &strMap{vals: map[string]string{}} }

func (m *strMap) set(k, v string) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

// attrText: a string as is, an object or array as JSON, anything else String(x).
func attrText(v any) string {
	switch v.(type) {
	case *JSONObject, []any:
		return jsStringify(v)
	}
	return jsString(v)
}

// flatten: the leaves of a value as key.path → text; objects recurse, arrays and scalars are
// leaves; null and "" are skipped.
func flatten(out *strMap, key string, v any) {
	if v == nil {
		return
	}
	if s, ok := v.(string); ok && s == "" {
		return
	}
	if o, ok := v.(*JSONObject); ok {
		for _, k := range o.Keys() {
			child, _ := o.Get(k)
			if key != "" {
				flatten(out, key+"."+k, child)
			} else {
				flatten(out, k, child)
			}
		}
		return
	}
	if key != "" {
		out.set(key, attrText(v))
	}
}

// sortedAttributes is the map's entries sorted by key (localeCompare).
func sortedAttributes(m *strMap) []domain.Attribute {
	out := make([]domain.Attribute, len(m.keys))
	for i, k := range m.keys {
		out[i] = domain.Attribute{k, m.vals[k]}
	}
	sort.SliceStable(out, func(i, j int) bool { return localeCompare(out[i][0], out[j][0]) < 0 })
	return out
}

// collect is everything under attributes or resource, with Axiom's custom map folded back in.
func collect(row *JSONObject, root string) []domain.Attribute {
	out := newStrMap()
	for _, k := range row.Keys() {
		v, _ := row.Get(k)
		if k == root {
			flatten(out, "", v)
		} else if strings.HasPrefix(k, root+".") {
			flatten(out, k[len(root)+1:], v)
		}
	}
	unwrapped := newStrMap()
	for _, k := range out.keys {
		unwrapped.set(strings.TrimPrefix(k, "custom."), out.vals[k])
	}
	return sortedAttributes(unwrapped)
}

// eventsOf is a span's events (exceptions with their stack traces, …).
func eventsOf(v any) []domain.SpanEvent {
	arr, ok := v.([]any)
	if !ok {
		return []domain.SpanEvent{}
	}
	out := []domain.SpanEvent{}
	for _, e := range arr {
		switch e.(type) {
		case *JSONObject, []any:
		default:
			continue
		}
		get := func(k string) any { x, _ := jsGet(e, k); return x }
		t := get("time")
		for _, k := range []string{"timestamp", "_time", "timeUnixNano"} {
			if t != nil {
				break
			}
			t = get(k)
		}
		attrs := newStrMap()
		flatten(attrs, "", get("attributes"))
		out = append(out, domain.SpanEvent{Time: timeOf(t), Name: jsString(get("name")), Attributes: sortedAttributes(attrs)})
	}
	return out
}

// spanOf maps one span row.
func spanOf(r *JSONObject) domain.Span {
	get := func(k string) any { v, _ := r.Get(k); return v }
	return domain.Span{
		SpanID:        jsString(get("span_id")),
		ParentID:      jsString(get("parent_span_id")),
		Name:          jsString(get("name")),
		Service:       jsString(pickValue(r, "service.name")),
		Kind:          kindOf(get("kind")),
		Start:         timeOf(get("_time")),
		Duration:      durationOf(get("duration")),
		Status:        statusOf(r),
		StatusMessage: jsString(pickValue(r, "status.message")),
		Scope:         jsString(pickValue(r, "scope.name")),
		Attributes:    collect(r, "attributes"),
		Resource:      collect(r, "resource"),
		Events:        eventsOf(pickValue(r, "events")),
	}
}

// httpStatusOf: http.response.status_code ?? http.status_code, kept if a finite number > 0.
func httpStatusOf(r *JSONObject) *float64 {
	v := attr(r, "http.response.status_code")
	if v == nil {
		v = attr(r, "http.status_code")
	}
	n := jsNumber(v)
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return nil
	}
	return &n
}
