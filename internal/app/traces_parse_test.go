package app

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

func obsJSONEqual(t *testing.T, got any, want json.RawMessage) bool {
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

func obsRow(t *testing.T, raw string) AxiomRow {
	t.Helper()
	var row AxiomRow
	if err := json.Unmarshal([]byte(raw), &row); err != nil {
		t.Fatal(err)
	}
	return row
}

func TestAxiomSpanOf(t *testing.T) {
	fx, golden := loadScenarioFiles(t)
	if len(fx.SpanRows) != len(golden.Spans) {
		t.Fatalf("%d span rows, %d golden spans", len(fx.SpanRows), len(golden.Spans))
	}
	for i, raw := range fx.SpanRows {
		got, err := axiomSpanOf(obsRow(t, string(raw)))
		if err != nil {
			t.Fatalf("span row %d: %v", i, err)
		}
		if !obsJSONEqual(t, got, golden.Spans[i]) {
			gb, _ := json.MarshalIndent(got, "", " ")
			t.Errorf("span row %d\n got: %s\nwant: %s", i, gb, golden.Spans[i])
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
	} {
		if _, err := axiomSpanOf(obsRow(t, raw)); err == nil {
			t.Errorf("%s: no error", raw)
		}
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
	got := sortedAttributes(spanAttributes(row, "attributes"))
	want := `[{"key":"app.id","value":"4.50"},{"key":"app.ok","value":"false"},{"key":"app.tags","value":"[\"a\",null]"},` +
		`{"key":"http.request.method","value":"GET"},{"key":"http.response.status_code","value":"200"},{"key":"http.route","value":"/u"}]`
	if !obsJSONEqual(t, got, json.RawMessage(want)) {
		gb, _ := json.Marshal(got)
		t.Errorf("attributes %s", gb)
	}
	if got := sortedAttributes(spanAttributes(row, "resource")); !obsJSONEqual(t, got, json.RawMessage(`[{"key":"service.name","value":"api"}]`)) {
		t.Errorf("resource %v", got)
	}
	for attributes, want := range map[string]float64{
		`{"attributes.http.response.status_code":503}`:                                                 503,
		`{"attributes.custom":{"http.status_code":"404"}}`:                                             404,
		`{"attributes.http.response.status_code":null,"attributes.custom":{"http.status_code":"502"}}`: 502,
		`{"attributes.http.response.status_code":"abc"}`:                                               0,
		`{"attributes.http.response.status_code":-5}`:                                                  0,
		`{}`: 0,
	} {
		got := httpStatusOf(spanAttributes(obsRow(t, attributes), "attributes"))
		if (got == nil) != (want == 0) || (got != nil && *got != want) {
			t.Errorf("httpStatusOf(%s) = %v, want %v", attributes, got, want)
		}
	}
}

func TestDemuxDockerLogs(t *testing.T) {
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
		if !obsJSONEqual(t, got, golden.Demux[i].Lines) {
			gb, _ := json.Marshal(got)
			t.Errorf("%s\n got: %s\nwant: %s", d.Name, gb, golden.Demux[i].Lines)
		}
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
		`1791460800123456789`:              1.791460800123456789e18 / 1e6,
		`"1791460800123"`:                  1_791_460_800_123,
		`"1791460800123456789"`:            1.791460800123456789e18 / 1e6,
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
	for _, in := range []string{`"nope"`, `""`, `"-5"`, `true`, `"2026-10-08"`} {
		var at axiomTime
		if err := json.Unmarshal([]byte(in), &at); err == nil {
			t.Errorf("time %s = %v, want an error", in, float64(at))
		}
	}
}

func TestSpanKindAndStatus(t *testing.T) {
	for in, want := range map[string]string{"SPAN_KIND_SERVER": "server", "Client": "client", "span_kind_unspecified": "", "unspecified": "", "": ""} {
		if got := (axiomSpanRow{Kind: in}).kind(); got != want {
			t.Errorf("kind(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		row  axiomSpanRow
		want string
	}{
		{axiomSpanRow{StatusCode: "STATUS_CODE_OK"}, "ok"},
		{axiomSpanRow{StatusCode: "Ok", Error: true}, "error"},
		{axiomSpanRow{StatusCode: "STATUS_CODE_ERROR"}, "error"},
		{axiomSpanRow{StatusCode: "unset"}, "unset"},
		{axiomSpanRow{}, "unset"},
	} {
		if got := c.row.status(); got != c.want {
			t.Errorf("status(%+v) = %q, want %q", c.row, got, c.want)
		}
	}
}
