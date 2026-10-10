package app

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/domain"
)

type axiomDuration float64

var (
	durationNanosRE  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
	durationDotnetRE = regexp.MustCompile(`^(?:([0-9]+)\.)?([0-9]+):([0-9]+):([0-9]+(?:\.[0-9]+)?)$`)
)

func (d *axiomDuration) UnmarshalJSON(b []byte) error {
	var nanos float64
	if err := json.Unmarshal(b, &nanos); err == nil {
		*d = axiomDuration(nanos / 1e6)
		return nil
	}
	var text string
	if err := json.Unmarshal(b, &text); err != nil {
		return err
	}
	ms, err := parseAxiomDuration(text)
	if err != nil {
		return err
	}
	*d = axiomDuration(ms)
	return nil
}

func parseAxiomDuration(text string) (float64, error) {
	if durationNanosRE.MatchString(text) {
		nanos, err := strconv.ParseFloat(text, 64)
		return nanos / 1e6, err
	}
	if d, err := time.ParseDuration(text); err == nil {
		return float64(d) / 1e6, nil
	}
	m := durationDotnetRE.FindStringSubmatch(text)
	if m == nil {
		return 0, fmt.Errorf("Axiom duration %q: unknown format", text)
	}
	days, _ := strconv.ParseFloat(cmp.Or(m[1], "0"), 64)
	hours, _ := strconv.ParseFloat(m[2], 64)
	minutes, _ := strconv.ParseFloat(m[3], 64)
	seconds, _ := strconv.ParseFloat(m[4], 64)
	return ((days*24+hours)*3600 + minutes*60 + seconds) * 1000, nil
}

type axiomStatsRow struct {
	Time     axiomTime      `json:"_time"`
	Requests float64        `json:"requests"`
	Errors   float64        `json:"errors"`
	P50      *axiomDuration `json:"p50"`
	P95      *axiomDuration `json:"p95"`
	P99      *axiomDuration `json:"p99"`
}

func (r axiomStatsRow) stats() domain.TraceStats {
	return domain.TraceStats{
		Requests: r.Requests,
		Errors:   r.Errors,
		P50:      (*float64)(r.P50),
		P95:      (*float64)(r.P95),
		P99:      (*float64)(r.P99),
	}
}

type axiomTraceCountRow struct {
	TraceID string  `json:"trace_id"`
	Spans   float64 `json:"spans"`
	Errors  float64 `json:"errors"`
}

type axiomSpanRow struct {
	Time          axiomTime        `json:"_time"`
	TraceID       string           `json:"trace_id"`
	SpanID        string           `json:"span_id"`
	ParentID      string           `json:"parent_span_id"`
	Name          string           `json:"name"`
	Kind          string           `json:"kind"`
	Duration      axiomDuration    `json:"duration"`
	Error         bool             `json:"error"`
	StatusCode    string           `json:"status.code"`
	StatusMessage string           `json:"status.message"`
	Service       string           `json:"service.name"`
	Scope         string           `json:"scope.name"`
	Environment   string           `json:"resource.deployment.environment.name"`
	Events        []axiomSpanEvent `json:"events"`
}

func (s axiomSpanRow) kind() string {
	kind := strings.TrimPrefix(strings.ToLower(s.Kind), "span_kind_")
	if kind == "unspecified" {
		return ""
	}
	return kind
}

func (s axiomSpanRow) status() string {
	code := strings.ToLower(s.StatusCode)
	if s.Error || strings.Contains(code, "error") {
		return "error"
	}
	if strings.Contains(code, "ok") {
		return "ok"
	}
	return "unset"
}

type axiomSpanEvent struct {
	Name           string                     `json:"name"`
	Time           axiomTime                  `json:"time"`
	Timestamp      axiomTime                  `json:"timestamp"`
	UnderscoreTime axiomTime                  `json:"_time"`
	TimeUnixNano   axiomTime                  `json:"timeUnixNano"`
	Attributes     map[string]json.RawMessage `json:"attributes"`
}

func (e axiomSpanEvent) event() domain.SpanEvent {
	attributes := map[string]string{}
	for key, raw := range e.Attributes {
		flattenAttribute(attributes, key, raw)
	}
	return domain.SpanEvent{
		Time:       float64(cmp.Or(e.Time, e.Timestamp, e.UnderscoreTime, e.TimeUnixNano)),
		Name:       e.Name,
		Attributes: sortedAttributes(attributes),
	}
}

func spanAttributes(row AxiomRow, root string) map[string]string {
	attributes := map[string]string{}
	for column, raw := range row {
		key, ok := strings.CutPrefix(column, root+".")
		if !ok {
			continue
		}
		if key == "custom" {
			key = ""
		}
		flattenAttribute(attributes, key, raw)
	}
	return attributes
}

func flattenAttribute(attributes map[string]string, key string, raw json.RawMessage) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		for child, value := range object {
			if key != "" {
				child = key + "." + child
			}
			flattenAttribute(attributes, child, value)
		}
		return
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		text = string(raw)
	}
	if key != "" && text != "" {
		attributes[key] = text
	}
}

func sortedAttributes(attributes map[string]string) []domain.Attribute {
	out := make([]domain.Attribute, 0, len(attributes))
	for _, key := range slices.Sorted(maps.Keys(attributes)) {
		out = append(out, domain.Attribute{Key: key, Value: attributes[key]})
	}
	return out
}

func httpStatusOf(attributes map[string]string) *float64 {
	text, ok := attributes["http.response.status_code"]
	if !ok {
		text = attributes["http.status_code"]
	}
	status, err := strconv.Atoi(text)
	if err != nil || status <= 0 {
		return nil
	}
	return obsF64(float64(status))
}

func axiomSpanOf(row AxiomRow) (domain.Span, error) {
	var s axiomSpanRow
	if err := row.decode(&s); err != nil {
		return domain.Span{}, err
	}
	events := make([]domain.SpanEvent, len(s.Events))
	for i, e := range s.Events {
		events[i] = e.event()
	}
	return domain.Span{
		SpanID:        s.SpanID,
		ParentID:      s.ParentID,
		Name:          s.Name,
		Service:       s.Service,
		Kind:          s.kind(),
		Start:         float64(s.Time),
		Duration:      float64(s.Duration),
		Status:        s.status(),
		StatusMessage: s.StatusMessage,
		Scope:         s.Scope,
		Attributes:    sortedAttributes(spanAttributes(row, "attributes")),
		Resource:      sortedAttributes(spanAttributes(row, "resource")),
		Events:        events,
	}, nil
}

func axiomTraceSummaryOf(row AxiomRow) (domain.TraceSummary, error) {
	var s axiomSpanRow
	if err := row.decode(&s); err != nil {
		return domain.TraceSummary{}, err
	}
	summary := domain.TraceSummary{
		TraceID:    s.TraceID,
		Name:       s.Name,
		Service:    s.Service,
		Kind:       s.kind(),
		Start:      float64(s.Time),
		Duration:   float64(s.Duration),
		HTTPStatus: httpStatusOf(spanAttributes(row, "attributes")),
		Spans:      1,
		Error:      s.status() == "error",
		Local:      s.Environment == "local",
	}
	if summary.Error {
		summary.Errors = 1
	}
	return summary, nil
}
