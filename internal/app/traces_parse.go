package app

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

func rowPick(obj any, path string) (any, bool) {
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
			if found, ok := rowPick(hv, path[i+1:]); ok {
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

func rowPickValue(obj any, path string) any {
	v, _ := rowPick(obj, path)
	return v
}

func spanAttr(row *JSONObject, name string) any {
	if v := rowPickValue(row, "attributes."+name); v != nil {
		return v
	}
	return rowPickValue(row, "attributes.custom."+name)
}

var timeDigitsRE = regexp.MustCompile(`^[0-9]+$`)

func axiomTimeOf(v any) float64 {
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
		if timeDigitsRE.MatchString(x) {
			return axiomTimeOf(jsNumber(x))
		}
		return axiomPreciseTime(x)
	}
	return 0
}

var (
	durationDecimalRE = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	durationUnitRE    = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)(ns|us|µs|μs|ms|h|m|s)`)
	durationDotnetRE  = regexp.MustCompile(`^(?:([0-9]+)\.)?([0-9]+):([0-9]+):([0-9]+(?:\.[0-9]+)?)$`)
	durationUnitMs    = map[string]float64{"ns": 1e-6, "us": 1e-3, "µs": 1e-3, "μs": 1e-3, "ms": 1, "s": 1000, "m": 60_000, "h": 3_600_000}
	durationFloat     = func(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
)

func durationOf(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x / 1e6
	case string:
		if x == "" {
			return 0
		}
		if durationDecimalRE.MatchString(x) {
			return durationFloat(x) / 1e6
		}
		total, matched := 0.0, false
		for _, m := range durationUnitRE.FindAllStringSubmatch(x, -1) {
			matched = true
			total += durationFloat(m[1]) * durationUnitMs[m[2]]
		}
		if matched {
			return total
		}
		t := durationDotnetRE.FindStringSubmatch(x)
		if t == nil {
			return 0
		}
		hours := durationFloat(t[1])*24 + durationFloat(t[2])
		return (hours*3600 + durationFloat(t[3])*60 + durationFloat(t[4])) * 1000
	}
	return 0
}

func durationOrNull(v any) *float64 {
	if v == nil || v == "" {
		return nil
	}
	d := durationOf(v)
	return &d
}

func traceStatsOf(r *JSONObject) domain.TraceStats {
	get := func(k string) any { v, _ := r.Get(k); return v }
	return domain.TraceStats{
		Requests: jsNum(get("requests")),
		Errors:   jsNum(get("errors")),
		P50:      durationOrNull(get("p50")),
		P95:      durationOrNull(get("p95")),
		P99:      durationOrNull(get("p99")),
	}
}

func spanKindOf(v any) string {
	k := strings.TrimPrefix(strings.ToLower(jsString(v)), "span_kind_")
	if k == "unspecified" {
		return ""
	}
	return k
}

func spanStatusOf(row *JSONObject) string {
	code := strings.ToLower(jsString(rowPickValue(row, "status.code")))
	if e, ok := rowPickValue(row, "error").(bool); (ok && e) || strings.Contains(code, "error") {
		return "error"
	}
	if strings.Contains(code, "ok") {
		return "ok"
	}
	return "unset"
}

type attrMap struct {
	keys []string
	vals map[string]string
}

func newAttrMap() *attrMap { return &attrMap{vals: map[string]string{}} }

func (m *attrMap) set(k, v string) {
	if _, ok := m.vals[k]; !ok {
		m.keys = append(m.keys, k)
	}
	m.vals[k] = v
}

func spanAttrText(v any) string {
	switch v.(type) {
	case *JSONObject, []any:
		return jsStringify(v)
	}
	return jsString(v)
}

func flattenAttrs(out *attrMap, key string, v any) {
	if v == nil || v == "" {
		return
	}
	if o, ok := v.(*JSONObject); ok {
		for _, k := range o.Keys() {
			child, _ := o.Get(k)
			if key != "" {
				flattenAttrs(out, key+"."+k, child)
			} else {
				flattenAttrs(out, k, child)
			}
		}
		return
	}
	if key != "" {
		out.set(key, spanAttrText(v))
	}
}

func sortedSpanAttributes(m *attrMap) []domain.Attribute {
	out := make([]domain.Attribute, len(m.keys))
	for i, k := range m.keys {
		out[i] = domain.Attribute{Key: k, Value: m.vals[k]}
	}
	sort.SliceStable(out, func(i, j int) bool { return localeCompare(out[i].Key, out[j].Key) < 0 })
	return out
}

func collectAttrs(row *JSONObject, root string) []domain.Attribute {
	out := newAttrMap()
	for _, k := range row.Keys() {
		v, _ := row.Get(k)
		if k == root {
			flattenAttrs(out, "", v)
		} else if strings.HasPrefix(k, root+".") {
			flattenAttrs(out, k[len(root)+1:], v)
		}
	}
	unwrapped := newAttrMap()
	for _, k := range out.keys {
		unwrapped.set(strings.TrimPrefix(k, "custom."), out.vals[k])
	}
	return sortedSpanAttributes(unwrapped)
}

func spanEventsOf(v any) []domain.SpanEvent {
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
		attrs := newAttrMap()
		flattenAttrs(attrs, "", get("attributes"))
		out = append(out, domain.SpanEvent{Time: axiomTimeOf(t), Name: jsString(get("name")), Attributes: sortedSpanAttributes(attrs)})
	}
	return out
}

func axiomSpanOf(r *JSONObject) domain.Span {
	get := func(k string) any { v, _ := r.Get(k); return v }
	return domain.Span{
		SpanID:        jsString(get("span_id")),
		ParentID:      jsString(get("parent_span_id")),
		Name:          jsString(get("name")),
		Service:       jsString(rowPickValue(r, "service.name")),
		Kind:          spanKindOf(get("kind")),
		Start:         axiomTimeOf(get("_time")),
		Duration:      durationOf(get("duration")),
		Status:        spanStatusOf(r),
		StatusMessage: jsString(rowPickValue(r, "status.message")),
		Scope:         jsString(rowPickValue(r, "scope.name")),
		Attributes:    collectAttrs(r, "attributes"),
		Resource:      collectAttrs(r, "resource"),
		Events:        spanEventsOf(rowPickValue(r, "events")),
	}
}

func spanHTTPStatus(r *JSONObject) *float64 {
	v := spanAttr(r, "http.response.status_code")
	if v == nil {
		v = spanAttr(r, "http.status_code")
	}
	n := jsNumber(v)
	if math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
		return nil
	}
	return &n
}
