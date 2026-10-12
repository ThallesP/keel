package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

type axiomFixture struct {
	SpanRows []json.RawMessage `json:"spanRows"`
	Demux    []demuxCase       `json:"demux"`
}

type demuxCase struct {
	Name   string       `json:"name"`
	Raw    string       `json:"raw"`
	Frames []demuxFrame `json:"frames"`
	Tail   []byte       `json:"tail"`
}

type demuxFrame struct {
	Stream  byte   `json:"stream"`
	Payload string `json:"payload"`
}

func readAxiomFixture(t *testing.T) axiomFixture {
	t.Helper()
	return ReadJSON[axiomFixture](t, "testdata/axiom_scenarios.json")
}

func obsRow(t *testing.T, raw string) AxiomRow {
	t.Helper()
	var row AxiomRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestAxiomSpanOf(t *testing.T) {
	fx := readAxiomFixture(t)
	spans := make([]domain.Span, len(fx.SpanRows))
	for i, raw := range fx.SpanRows {
		span, err := axiomSpanOf(obsRow(t, string(raw)))
		if err != nil {
			t.Fatalf("span row %d: %v", i, err)
		}
		spans[i] = span
	}
	if *UpdateGolden {
		RewriteAxiomGolden(t, func(g *AxiomGolden) { g.Spans = spans })
		return
	}
	golden := ReadAxiomGolden(t)
	if len(spans) != len(golden.Spans) {
		t.Fatalf("%d span rows, %d golden spans", len(spans), len(golden.Spans))
	}
	for i, span := range spans {
		if !reflect.DeepEqual(span, golden.Spans[i]) {
			t.Errorf("span row %d\n got: %+v\nwant: %+v", i, span, golden.Spans[i])
		}
	}
}

func TestAxiomSpanOfRejectsForeignShapes(t *testing.T) {
	for _, raw := range []string{
		`{"span_id":12}`,
		`{"name":{"nested":true}}`,
		`{"error":"true"}`,
		`{"_time":"nope"}`,
		`{"duration":"garbage"}`,
		`{"events":{"not":"an array"}}`,
		`{"events":["str"]}`,
		`{"events":[[1,2]]}`,
		`{"events":[{"attributes":[1]}]}`,
		`{"events":[{"timestamp":""}]}`,
		`{"kind":5}`,
		`{"status.code":3}`,
	} {
		if _, err := axiomSpanOf(obsRow(t, raw)); err == nil {
			t.Errorf("%s: no error", raw)
		}
	}
	if _, err := axiomTraceSummaryOf(obsRow(t, `{"events":[{"timestamp":""}]}`)); err != nil {
		t.Errorf("a request summary read the events: %v", err)
	}
	if _, err := axiomTraceSummaryOf(obsRow(t, `{"kind":5}`)); err == nil {
		t.Error("a request summary took a number kind")
	}
	span, err := axiomSpanOf(obsRow(t, `{"events":[null]}`))
	if err != nil || span.Events == nil || len(span.Events) != 0 {
		t.Errorf("null event: %+v %v", span.Events, err)
	}
}

func TestSpanAttributes(t *testing.T) {
	row := obsRow(t, `{
		"attributes.http.request.method": "GET",
		"attributes.http.response.status_code": 200,
		"attributes.custom": {"http.route": "/u", "app": {"id": 4.50, "tags": ["a",null], "ok": false}, "empty": "", "nil": null},
		"resource.custom": null,
		"resource.service.name": "api",
		"attributesx": "not an attribute",
		"name": "GET"
	}`)
	want := []domain.Attribute{
		{Key: "app.id", Value: "4.50"}, {Key: "app.ok", Value: "false"}, {Key: "app.tags", Value: `["a",null]`},
		{Key: "http.request.method", Value: "GET"}, {Key: "http.response.status_code", Value: "200"}, {Key: "http.route", Value: "/u"},
	}
	if got := sortedAttributes(spanAttributes(row, "attributes")); !reflect.DeepEqual(got, want) {
		t.Errorf("attributes %+v", got)
	}
	if got := sortedAttributes(spanAttributes(row, "resource")); !reflect.DeepEqual(got, []domain.Attribute{{Key: "service.name", Value: "api"}}) {
		t.Errorf("resource %+v", got)
	}
	collide := obsRow(t, `{"attributes.a.b":"flat","attributes.custom":{"a":{"b":"nested"},"a.b":"dotted"}}`)
	for range 50 {
		if got := spanAttributes(collide, "attributes")["a.b"]; got != "dotted" {
			t.Fatalf("colliding key a.b = %q, want the last in byte order", got)
		}
	}
	for _, c := range []struct {
		row  string
		want *int
	}{
		{`{"attributes.http.response.status_code":503}`, new(503)},
		{`{"attributes.custom":{"http.status_code":"404"}}`, new(404)},
		{`{"attributes.http.response.status_code":null,"attributes.custom":{"http.status_code":"502"}}`, new(502)},
		{`{"attributes.http.response.status_code":"abc"}`, nil},
		{`{"attributes.http.response.status_code":-5}`, nil},
		{`{}`, nil},
	} {
		if got := httpStatusOf(spanAttributes(obsRow(t, c.row), "attributes")); !reflect.DeepEqual(got, c.want) {
			t.Errorf("httpStatusOf(%s) = %v, want %v", c.row, got, c.want)
		}
	}
}

func TestDemuxDockerLogs(t *testing.T) {
	fx := readAxiomFixture(t)
	got := make([]DemuxGolden, len(fx.Demux))
	for i, d := range fx.Demux {
		buf := []byte(d.Raw)
		for _, f := range d.Frames {
			buf = append(buf, DockerFrame(f.Stream, f.Payload)...)
		}
		got[i] = DemuxGolden{Name: d.Name, Lines: demuxDockerLogs(append(buf, d.Tail...))}
	}
	if *UpdateGolden {
		RewriteAxiomGolden(t, func(g *AxiomGolden) { g.Demux = got })
		return
	}
	if want := ReadAxiomGolden(t).Demux; !reflect.DeepEqual(got, want) {
		t.Errorf("demux\n got: %+v\nwant: %+v", got, want)
	}
}

func TestDemuxDockerLogsJoinsSplitCharacters(t *testing.T) {
	buf := append(DockerFrame(1, "2026-10-08T12:00:00Z caf\xc3"), DockerFrame(1, "\xa9\n")...)
	want := []domain.ServiceLogLine{{Time: 1791460800000, Text: "café", Stream: "stdout"}}
	if got := demuxDockerLogs(buf); !reflect.DeepEqual(got, want) {
		t.Errorf("split character %+v", got)
	}
}

func TestAxiomDuration(t *testing.T) {
	for in, want := range map[string]float64{
		`1500000`:            1.5,
		`1234567.5`:          1.2345675,
		`"1500000"`:          1.5,
		`"1.5"`:              1.5e-6,
		`"5ms"`:              5,
		`"1m30.5s"`:          90_500,
		`"2h"`:               7_200_000,
		`"250µs"`:            0.25,
		`"250μs"`:            0.25,
		`"250us"`:            0.25,
		`"10ns"`:             1e-5,
		`"00:00:01.5"`:       1500,
		`"1.00:00:00"`:       86_400_000,
		`"00:00:00.0025000"`: 2.5,
		`null`:               0,
	} {
		var d axiomDuration
		if err := json.Unmarshal([]byte(in), &d); err != nil || float64(d) != want {
			t.Errorf("duration %s = %v (%v), want %v", in, float64(d), err, want)
		}
	}
	for _, in := range []string{`"garbage"`, `""`, `true`, `{}`, `"1.5.5"`} {
		var d axiomDuration
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("duration %s = %v, want an error", in, float64(d))
		}
	}
	var stats axiomStatsRow
	if err := json.Unmarshal([]byte(`{"requests":3,"p50":null,"p95":"2.5ms","p99":2500000}`), &stats); err != nil {
		t.Fatal(err)
	}
	if s := stats.stats(); s.Requests != 3 || s.P50 != nil || *s.P95 != 2.5 || *s.P99 != 2.5 {
		t.Errorf("stats %+v", s)
	}
}

func TestAxiomTime(t *testing.T) {
	for in, want := range map[string]float64{
		`1791460800123`:                    1_791_460_800_123,
		`1791460800123456`:                 1_791_460_800_123.456,
		`1791460800123456789`:              1_791_460_800_123.456789,
		`"1791460800123"`:                  1_791_460_800_123,
		`"1791460800123456789"`:            1_791_460_800_123.456789,
		`"1791460800750000000"`:            1_791_460_800_750,
		`"2026-10-08T12:00:00.5Z"`:         1_791_460_800_500,
		`"2026-10-08T12:00:00.123456789Z"`: 1_791_460_800_123 + 0.456789,
		`"2026-10-08T12:00:00Z"`:           1_791_460_800_000,
		`"2026-10-08T12:00:01+02:00"`:      1_791_453_601_000,
		`null`:                             0,
	} {
		var at axiomTime
		if err := json.Unmarshal([]byte(in), &at); err != nil || float64(at) != want {
			t.Errorf("time %s = %v (%v), want %v", in, float64(at), err, want)
		}
	}
	for _, in := range []string{`"nope"`, `""`, `"-5"`, `-5`, `1791460800123.5`, `true`, `"2026-10-08"`, `"2026-10-08 12:00:00Z"`} {
		var at axiomTime
		if err := json.Unmarshal([]byte(in), &at); err == nil {
			t.Errorf("time %s = %v, want an error", in, float64(at))
		}
	}
}

func TestSpanKindAndStatus(t *testing.T) {
	for in, want := range map[string]string{"SPAN_KIND_SERVER": "server", "Client": "client", "span_kind_unspecified": "", "unspecified": "", "": ""} {
		if got := (axiomRootRow{Kind: in}).kind(); got != want {
			t.Errorf("kind(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		row  axiomRootRow
		want string
	}{
		{axiomRootRow{StatusCode: "STATUS_CODE_OK"}, "ok"},
		{axiomRootRow{StatusCode: "Ok", Error: true}, "error"},
		{axiomRootRow{StatusCode: "STATUS_CODE_ERROR"}, "error"},
		{axiomRootRow{StatusCode: "unset"}, "unset"},
		{axiomRootRow{}, "unset"},
	} {
		if got := c.row.status(); got != c.want {
			t.Errorf("status(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}
