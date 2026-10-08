package app_test

// The Axiom read side against the TypeScript it replaces. testdata/axiom_scenarios.json holds
// canned Axiom answers (matched on the APL they receive); axiom_scenarios.golden.json is what the
// TypeScript providers (logProviders/axiom.ts, traceProviders/axiom.ts, convex/traces.ts get)
// returned for them, and the requests they sent, with Date.now pinned. Here the Go use cases run
// on a real SQLite store and the real Axiom adapter against an httptest server serving the same
// answers; results and requests must be identical.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ThallesP/keel/internal/adapters/axiom"
	"github.com/ThallesP/keel/internal/domain"
)

type scenarioRule struct {
	Contains string   `json:"contains"`
	Fields   []string `json:"fields"`
	Rows     [][]any  `json:"rows"`
	SpanRows []int    `json:"spanRows"`
	NoTables bool     `json:"noTables"`
	Status   int      `json:"status"`
	Text     string   `json:"text"`
}

type scenario struct {
	Name     string          `json:"name"`
	Call     string          `json:"call"`
	NoTraces bool            `json:"noTraces"`
	Args     map[string]any  `json:"args"`
	Rules    []scenarioRule  `json:"rules"`
	Raw      json.RawMessage `json:"-"`
}

type scenarioFixture struct {
	Now        int64             `json:"now"`
	Sink       domain.LogSink    `json:"sink"`
	ServiceIDs []string          `json:"serviceIds"`
	SpanRows   []json.RawMessage `json:"spanRows"`
	Scenarios  []scenario        `json:"scenarios"`
}

type goldenCall struct {
	Method        string         `json:"method"`
	Path          string         `json:"path"`
	Authorization string         `json:"authorization"`
	ContentType   string         `json:"contentType"`
	Body          map[string]any `json:"body"`
}

type goldenScenario struct {
	Name   string          `json:"name"`
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
	Calls  []goldenCall    `json:"calls"`
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// fakeAxiomAPL answers APL queries by the first rule whose `contains` is in the APL, and records
// every request.
type fakeAxiomAPL struct {
	mu       sync.Mutex
	rules    []scenarioRule
	spanRows []json.RawMessage
	calls    []goldenCall
}

func (f *fakeAxiomAPL) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, goldenCall{
		Method: r.Method, Path: r.URL.RequestURI(), Authorization: r.Header.Get("Authorization"),
		ContentType: r.Header.Get("Content-Type"), Body: body,
	})
	apl, _ := body["apl"].(string)
	for _, rule := range f.rules {
		if !strings.Contains(apl, rule.Contains) {
			continue
		}
		if rule.Status != 0 {
			w.WriteHeader(rule.Status)
			_, _ = w.Write([]byte(rule.Text))
			return
		}
		_, _ = w.Write(f.table(rule))
		return
	}
	_, _ = w.Write([]byte(`{"tables":[]}`))
}

// table builds Axiom's tabular answer (column-major) by hand, keeping the fixture's key order.
func (f *fakeAxiomAPL) table(rule scenarioRule) []byte {
	if rule.NoTables {
		return []byte(`{"tables":[]}`)
	}
	fields := rule.Fields
	var rows [][]json.RawMessage
	if rule.SpanRows != nil {
		var objs []map[string]json.RawMessage
		fields = nil
		for _, i := range rule.SpanRows {
			raw := f.spanRows[i]
			fields = appendKeys(fields, raw)
			var m map[string]json.RawMessage
			_ = json.Unmarshal(raw, &m)
			objs = append(objs, m)
		}
		for _, o := range objs {
			row := make([]json.RawMessage, len(fields))
			for c, k := range fields {
				if v, ok := o[k]; ok {
					row[c] = v
				} else {
					row[c] = json.RawMessage("null")
				}
			}
			rows = append(rows, row)
		}
	} else {
		for _, r := range rule.Rows {
			row := make([]json.RawMessage, len(r))
			for c, v := range r {
				b, _ := json.Marshal(v)
				row[c] = b
			}
			rows = append(rows, row)
		}
	}
	var b strings.Builder
	b.WriteString(`{"tables":[{"fields":[`)
	for i, name := range fields {
		if i > 0 {
			b.WriteByte(',')
		}
		n, _ := json.Marshal(name)
		b.WriteString(`{"name":` + string(n) + `}`)
	}
	b.WriteString(`],"columns":[`)
	for c := range fields {
		if c > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('[')
		for i, r := range rows {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(r[c])
		}
		b.WriteByte(']')
	}
	b.WriteString(`]}]}`)
	return []byte(b.String())
}

// appendKeys adds the object's keys, in document order, that fields lacks.
func appendKeys(fields []string, raw json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	_, _ = dec.Token() // {
	for dec.More() {
		tok, _ := dec.Token()
		k := tok.(string)
		var skip json.RawMessage
		_ = dec.Decode(&skip)
		found := false
		for _, f := range fields {
			found = found || f == k
		}
		if !found {
			fields = append(fields, k)
		}
	}
	return fields
}

func (f *fakeAxiomAPL) reset(rules []scenarioRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules, f.calls = rules, nil
}

func (f *fakeAxiomAPL) sortedCalls() []goldenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]goldenCall(nil), f.calls...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Body["apl"].(string) < out[j].Body["apl"].(string)
	})
	return out
}

func argFloat(args map[string]any, k string) float64 {
	f, _ := args[k].(float64)
	return f
}

func argString(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

// asJSON normalizes v to what encoding/json decodes it to (numbers as float64).
func asJSON(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAxiomScenariosMatchTypeScript(t *testing.T) {
	var fx scenarioFixture
	readJSON(t, "testdata/axiom_scenarios.json", &fx)
	var golden struct {
		Scenarios []goldenScenario `json:"scenarios"`
	}
	readJSON(t, "testdata/axiom_scenarios.golden.json", &golden)

	fake := &fakeAxiomAPL{spanRows: fx.SpanRows}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	env := newObsEnv(t, fx.Now)
	env.app.Axiom = axiom.New()
	sink := fx.Sink
	sink.Kind = domain.SinkKindAxiom
	sink.Domain = srv.URL
	env.setSink(t, "org", sink)

	ctx := context.Background()
	actor := env.member
	for i, s := range fx.Scenarios {
		g := golden.Scenarios[i]
		if g.Name != s.Name {
			t.Fatalf("golden out of order: %s vs %s", g.Name, s.Name)
		}
		t.Run(s.Name, func(t *testing.T) {
			if s.NoTraces {
				old := sink
				old.Traces = ""
				env.setSink(t, "org", old)
				defer env.setSink(t, "org", sink)
			}
			fake.reset(s.Rules)
			var result any
			var err error
			a := s.Args
			switch s.Call {
			case "overview":
				result, err = env.app.TraceOverview(ctx, actor, "env", domain.TimeRange(argString(a, "range")), argString(a, "search"), "")
			case "tail":
				result, err = env.app.TailNodeLogs(ctx, actor, argString(a, "serviceId"), argFloat(a, "tail"))
			case "recent":
				result, err = env.app.EnvironmentLogs(ctx, actor, "env", argString(a, "search"), argFloat(a, "tail"), domain.TimeRange(argString(a, "range")))
			case "around":
				result, err = env.app.LogsAround(ctx, actor, "env", argFloat(a, "at"))
			case "get":
				result, err = env.app.GetTrace(ctx, actor, "env", argString(a, "traceId"), argFloat(a, "at"))
			case "tracesAround":
				result, err = env.app.TracesAround(ctx, actor, "env", argFloat(a, "at"))
			default:
				t.Fatalf("unknown call %s", s.Call)
			}
			if g.Error != nil {
				if err == nil || err.Error() != *g.Error {
					t.Fatalf("error = %v, want %q", err, *g.Error)
				}
				if domain.CodeOf(err) != domain.CodeInvalidInput {
					t.Errorf("code = %s, want INVALID_INPUT", domain.CodeOf(err))
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var want any
				_ = json.Unmarshal(g.Result, &want)
				if got := asJSON(t, result); !reflect.DeepEqual(got, want) {
					gb, _ := json.MarshalIndent(got, "", " ")
					wb, _ := json.MarshalIndent(want, "", " ")
					t.Fatalf("result differs\n got: %s\nwant: %s", gb, wb)
				}
			}
			calls := fake.sortedCalls()
			if len(calls) != len(g.Calls) {
				t.Fatalf("%d Axiom calls, want %d: %+v", len(calls), len(g.Calls), calls)
			}
			for i, c := range calls {
				w := g.Calls[i]
				if c.Method != w.Method || c.Path != w.Path || c.Authorization != w.Authorization || c.ContentType != w.ContentType ||
					!reflect.DeepEqual(c.Body, w.Body) {
					t.Errorf("call %d\n got: %+v\nwant: %+v", i, c, w)
				}
			}
		})
	}
}
