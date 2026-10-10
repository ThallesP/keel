package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ThallesP/keel/internal/adapters/axiom"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type obsScenarioRule struct {
	Contains string              `json:"contains"`
	Fields   []string            `json:"fields"`
	Rows     [][]json.RawMessage `json:"rows"`
	SpanRows []int               `json:"spanRows"`
	NoTables bool                `json:"noTables"`
	Status   int                 `json:"status"`
	Text     string              `json:"text"`
}

type obsScenarioArgs struct {
	Range     domain.TimeRange `json:"range"`
	Search    string           `json:"search"`
	ServiceID string           `json:"serviceId"`
	TraceID   string           `json:"traceId"`
	Tail      float64          `json:"tail"`
	At        float64          `json:"at"`
}

type obsScenario struct {
	Name     string            `json:"name"`
	Call     string            `json:"call"`
	NoTraces bool              `json:"noTraces"`
	Args     obsScenarioArgs   `json:"args"`
	Rules    []obsScenarioRule `json:"rules"`
}

type obsScenarioFixture struct {
	Now       int64                        `json:"now"`
	Sink      domain.LogSink               `json:"sink"`
	SpanRows  []map[string]json.RawMessage `json:"spanRows"`
	Scenarios []obsScenario                `json:"scenarios"`
}

type aplCall struct {
	APL       string `json:"apl"`
	StartTime string `json:"startTime"`
	EndTime   string `json:"endTime"`
}

type obsGoldenCall struct {
	Method        string  `json:"method"`
	Path          string  `json:"path"`
	Authorization string  `json:"authorization"`
	ContentType   string  `json:"contentType"`
	Body          aplCall `json:"body"`
}

type obsGoldenScenario struct {
	Name   string          `json:"name"`
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
	Calls  []obsGoldenCall `json:"calls"`
}

type aplField struct {
	Name string `json:"name"`
}

type aplTable struct {
	Fields  []aplField          `json:"fields"`
	Columns [][]json.RawMessage `json:"columns"`
}

type aplAnswer struct {
	Tables []aplTable `json:"tables"`
}

type obsAPLServer struct {
	mu       sync.Mutex
	rules    []obsScenarioRule
	spanRows []map[string]json.RawMessage
	calls    []obsGoldenCall
}

func (f *obsAPLServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, _ := io.ReadAll(r.Body)
	var body aplCall
	_ = json.Unmarshal(data, &body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, obsGoldenCall{
		Method: r.Method, Path: r.URL.RequestURI(), Authorization: r.Header.Get("Authorization"),
		ContentType: r.Header.Get("Content-Type"), Body: body,
	})
	for _, rule := range f.rules {
		if !strings.Contains(body.APL, rule.Contains) {
			continue
		}
		if rule.Status != 0 {
			w.WriteHeader(rule.Status)
			_, _ = w.Write([]byte(rule.Text))
			return
		}
		_ = json.NewEncoder(w).Encode(f.answer(rule))
		return
	}
	_ = json.NewEncoder(w).Encode(aplAnswer{Tables: []aplTable{}})
}

func (f *obsAPLServer) answer(rule obsScenarioRule) aplAnswer {
	if rule.NoTables {
		return aplAnswer{Tables: []aplTable{}}
	}
	fields, rows := rule.Fields, rule.Rows
	if rule.SpanRows != nil {
		fields, rows = nil, nil
		for _, i := range rule.SpanRows {
			for _, k := range slices.Sorted(maps.Keys(f.spanRows[i])) {
				if !slices.Contains(fields, k) {
					fields = append(fields, k)
				}
			}
		}
		for _, i := range rule.SpanRows {
			row := make([]json.RawMessage, len(fields))
			for c, k := range fields {
				row[c] = f.spanRows[i][k]
				if row[c] == nil {
					row[c] = json.RawMessage("null")
				}
			}
			rows = append(rows, row)
		}
	}
	table := aplTable{Fields: make([]aplField, len(fields)), Columns: make([][]json.RawMessage, len(fields))}
	for c, name := range fields {
		table.Fields[c] = aplField{Name: name}
		table.Columns[c] = []json.RawMessage{}
		for _, row := range rows {
			table.Columns[c] = append(table.Columns[c], row[c])
		}
	}
	return aplAnswer{Tables: []aplTable{table}}
}

func (f *obsAPLServer) reset(rules []obsScenarioRule) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules, f.calls = rules, nil
}

func (f *obsAPLServer) sortedCalls() []obsGoldenCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := slices.Clone(f.calls)
	slices.SortStableFunc(out, func(a, b obsGoldenCall) int { return strings.Compare(a.Body.APL, b.Body.APL) })
	return out
}

func TestAxiomScenarios(t *testing.T) {
	fx := app.ReadJSON[obsScenarioFixture](t, "testdata/axiom_scenarios.json")
	var golden []obsGoldenScenario
	if !*app.UpdateGolden {
		if err := json.Unmarshal(app.ReadAxiomGolden(t).Scenarios, &golden); err != nil {
			t.Fatal(err)
		}
		if len(golden) != len(fx.Scenarios) {
			t.Fatalf("%d scenarios, %d golden", len(fx.Scenarios), len(golden))
		}
	}

	fake := &obsAPLServer{spanRows: fx.SpanRows}
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
	got := make([]obsGoldenScenario, len(fx.Scenarios))
	for i, s := range fx.Scenarios {
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
				result, err = env.app.TraceOverview(ctx, actor, "env", a.Range, a.Search, "")
			case "tail":
				result, err = env.app.TailNodeLogs(ctx, actor, a.ServiceID, a.Tail)
			case "recent":
				result, err = env.app.EnvironmentLogs(ctx, actor, "env", a.Search, a.Tail, a.Range)
			case "around":
				result, err = env.app.LogsAround(ctx, actor, "env", a.At)
			case "get":
				result, err = env.app.GetTrace(ctx, actor, "env", a.TraceID, a.At)
			case "tracesAround":
				result, err = env.app.TracesAround(ctx, actor, "env", a.At)
			default:
				t.Fatalf("unknown call %s", s.Call)
			}
			got[i] = obsGoldenScenario{Name: s.Name, Calls: fake.sortedCalls()}
			if err != nil {
				msg := err.Error()
				got[i].Error = &msg
				var de *domain.Error
				if !errors.As(err, &de) || de.Code != domain.CodeInvalidInput {
					t.Errorf("error %v, want INVALID_INPUT", err)
				}
			} else if got[i].Result, err = json.Marshal(result); err != nil {
				t.Fatal(err)
			}
			if *app.UpdateGolden {
				return
			}
			want := golden[i]
			if want.Name != s.Name {
				t.Fatalf("golden out of order: %s vs %s", want.Name, s.Name)
			}
			if want.Error != nil {
				if got[i].Error == nil || *got[i].Error != *want.Error {
					t.Fatalf("error = %v, want %q", got[i].Error, *want.Error)
				}
			} else {
				if got[i].Error != nil {
					t.Fatal(*got[i].Error)
				}
				var wantResult bytes.Buffer
				if err := json.Compact(&wantResult, want.Result); err != nil || !bytes.Equal(got[i].Result, wantResult.Bytes()) {
					t.Fatalf("result differs\n got: %s\nwant: %s", got[i].Result, want.Result)
				}
			}
			if !slices.Equal(got[i].Calls, want.Calls) {
				t.Errorf("calls\n got: %+v\nwant: %+v", got[i].Calls, want.Calls)
			}
		})
	}
	if *app.UpdateGolden {
		scenarios, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		app.RewriteAxiomGolden(t, func(g *app.AxiomGolden) { g.Scenarios = scenarios })
	}
}
