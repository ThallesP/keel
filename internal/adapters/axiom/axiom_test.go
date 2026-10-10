package axiom

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
)

type seen struct {
	method, path, auth, orgID, ctype, encoding, dataset string
	body                                                []byte
}

func server(t *testing.T, handle func(w http.ResponseWriter, s seen)) (*httptest.Server, *[]seen) {
	t.Helper()
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("X-Axiom-Org-Id"),
			r.Header.Get("Content-Type"), r.Header.Get("Content-Encoding"), r.Header.Get("X-Axiom-Dataset"), b}
		got = append(got, s)
		handle(w, s)
	}))
	t.Cleanup(srv.Close)
	return srv, &got
}

func TestQueryTabular(t *testing.T) {
	srv, got := server(t, func(w http.ResponseWriter, _ seen) {
		_, _ = w.Write([]byte(`{"format":"tabular","tables":[{"name":"0","fields":[{"name":"_time","type":"datetime"},{"name":"n"},{"name":"obj"}],` +
			`"columns":[["a","b"],[1,null],[{"z":1,"y":[2]},"x"]]}]}`))
	})
	c := New()
	rows, err := c.Query(context.Background(), app.AxiomTarget{Domain: srv.URL + "/", Token: "tok"}, app.AxiomQuery{APL: "['x'] | limit 1", StartTime: "s", EndTime: "e"})
	if err != nil {
		t.Fatal(err)
	}
	s := (*got)[0]
	if s.method != "POST" || s.path != "/v1/datasets/_apl?format=tabular" || s.auth != "Bearer tok" || s.ctype != "application/json" || s.orgID != "" {
		t.Fatalf("request %+v", s)
	}
	var body map[string]string
	_ = json.Unmarshal(s.body, &body)
	if !reflect.DeepEqual(body, map[string]string{"apl": "['x'] | limit 1", "startTime": "s", "endTime": "e"}) {
		t.Fatalf("body %s", s.body)
	}
	want := []app.AxiomRow{
		{"_time": json.RawMessage(`"a"`), "n": json.RawMessage(`1`), "obj": json.RawMessage(`{"z":1,"y":[2]}`)},
		{"_time": json.RawMessage(`"b"`), "n": json.RawMessage(`null`), "obj": json.RawMessage(`"x"`)},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows %q", rows)
	}
}

func TestQueryTabularShape(t *testing.T) {
	var body string
	srv, _ := server(t, func(w http.ResponseWriter, _ seen) { _, _ = w.Write([]byte(body)) })
	c := New()
	tgt := app.AxiomTarget{Domain: srv.URL, Token: "t"}
	for _, empty := range []string{`{"tables":[{"fields":[{"name":"_time"}],"columns":[]}]}`, `{"tables":[{"fields":[{"name":"_time"}],"columns":[[]]}]}`} {
		body = empty
		if rows, err := c.Query(context.Background(), tgt, app.AxiomQuery{}); err != nil || len(rows) != 0 {
			t.Errorf("%s: %v %v", empty, rows, err)
		}
	}
	for _, bad := range []string{
		`{"tables":[{"fields":[{"name":"a"},{"name":"b"}],"columns":[[1,2],[3]]}]}`,
		`{"tables":[{"fields":[{"name":"a"}],"columns":[[1],[2]]}]}`,
		`{"tables":[{"fields":[{"name":5}],"columns":[[1]]}]}`,
	} {
		body = bad
		if _, err := c.Query(context.Background(), tgt, app.AxiomQuery{}); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestQueryNoTablesAndErrors(t *testing.T) {
	status, body := 200, `{}`
	srv, _ := server(t, func(w http.ResponseWriter, _ seen) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
	c := New()
	tgt := app.AxiomTarget{Domain: srv.URL, Token: "t"}
	rows, err := c.Query(context.Background(), tgt, app.AxiomQuery{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("no tables: %v %v", rows, err)
	}
	status, body = 400, "{\n  \"message\": \"invalid field\"\n}"
	_, err = c.Query(context.Background(), tgt, app.AxiomQuery{})
	var ae *app.AxiomError
	if !errors.As(err, &ae) || err.Error() != `Axiom 400: { "message": "invalid field" }` {
		t.Fatalf("error %v", err)
	}
	status, body = 503, ""
	if _, err = c.Query(context.Background(), tgt, app.AxiomQuery{}); err == nil || err.Error() != "Axiom 503" {
		t.Fatalf("error %v", err)
	}
	status, body = 200, "not json"
	if _, err = c.Query(context.Background(), tgt, app.AxiomQuery{}); err == nil || errors.As(err, &ae) {
		t.Fatalf("bad json: %v", err)
	}
}

func TestDatasetsTokensOrgs(t *testing.T) {
	srv, got := server(t, func(w http.ResponseWriter, s seen) {
		switch {
		case s.method == "GET" && s.path == "/v2/datasets":
			_, _ = w.Write([]byte(`[{"name":"keel-logs"},{"name":"otel-demo","sharedByOrg":"axiom-playground"},{"name":"x","sharedByOrg":""}]`))
		case s.method == "POST" && s.path == "/v2/datasets":
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`dataset exists`))
		case s.path == "/v2/tokens":
			_, _ = w.Write([]byte(`{"id":"t1","token":"xaat-minted"}`))
		case s.path == "/v2/orgs":
			_, _ = w.Write([]byte(`[{"id":"o1","name":"One","defaultEdgeDeployment":"cloud.eu-central-1.aws","region":"us-east-1","license":{"maxDatasets":3}},` +
				`{"id":"o2","name":"Two","region":"us-east-1","license":null},{"id":"o3","name":"Three","defaultEdgeDeployment":"","region":"eu-west-1"}]`))
		}
	})
	c := New()
	ctx := context.Background()
	tgt := app.AxiomTarget{Domain: srv.URL, Token: "personal"}

	ds, err := c.Datasets(ctx, tgt, "o1")
	if err != nil || !reflect.DeepEqual(ds, []app.AxiomDataset{{Name: "keel-logs"}, {Name: "otel-demo", Shared: true}, {Name: "x"}}) {
		t.Fatalf("datasets %+v %v", ds, err)
	}
	err = c.CreateDataset(ctx, tgt, "o1", "keel-traces", "Keel OpenTelemetry traces")
	if err == nil || err.Error() != "Axiom 409: dataset exists" {
		t.Fatalf("create %v", err)
	}
	tok, err := c.MintToken(ctx, tgt, "o1", app.AxiomTokenRequest{Name: "keel-acme", Description: "d", Datasets: []string{"keel-logs", "keel-traces"}})
	if err != nil || tok != "xaat-minted" {
		t.Fatalf("mint %q %v", tok, err)
	}
	orgs, err := c.Orgs(ctx, tgt)
	wantOrgs := []app.AxiomOrgInfo{
		{ID: "o1", Name: "One", Edge: "cloud.eu-central-1.aws", MaxDatasets: 3},
		{ID: "o2", Name: "Two", Edge: "us-east-1"},
		{ID: "o3", Name: "Three", Edge: "eu-west-1"},
	}
	if err != nil || !reflect.DeepEqual(orgs, wantOrgs) {
		t.Fatalf("orgs %+v %v", orgs, err)
	}
	calls := *got
	for i, want := range []seen{
		{method: "GET", path: "/v2/datasets", auth: "Bearer personal", orgID: "o1"},
		{method: "POST", path: "/v2/datasets", auth: "Bearer personal", orgID: "o1"},
		{method: "POST", path: "/v2/tokens", auth: "Bearer personal", orgID: "o1"},
		{method: "GET", path: "/v2/orgs", auth: "Bearer personal", orgID: ""},
	} {
		c := calls[i]
		if c.method != want.method || c.path != want.path || c.auth != want.auth || c.orgID != want.orgID || c.ctype != "application/json" {
			t.Errorf("call %d: %+v", i, c)
		}
	}
	if string(calls[1].body) != `{"name":"keel-traces","description":"Keel OpenTelemetry traces"}` {
		t.Errorf("create body %s", calls[1].body)
	}
	want := `{"name":"keel-acme","description":"d","datasetCapabilities":{"keel-logs":{"ingest":["create"],"query":["read"]},"keel-traces":{"ingest":["create"],"query":["read"]}},"orgCapabilities":{}}`
	if string(calls[2].body) != want {
		t.Errorf("token body %s", calls[2].body)
	}
}

func TestDatasetsAndTokenRejectForeignShapes(t *testing.T) {
	srv, _ := server(t, func(w http.ResponseWriter, s seen) {
		switch s.path {
		case "/v2/datasets":
			_, _ = w.Write([]byte(`[{"name":"x","sharedByOrg":true}]`))
		case "/v2/tokens":
			_, _ = w.Write([]byte(`{"token":5}`))
		}
	})
	c := New()
	ctx := context.Background()
	tgt := app.AxiomTarget{Domain: srv.URL, Token: "personal"}
	if _, err := c.Datasets(ctx, tgt, "o1"); err == nil || !strings.HasPrefix(err.Error(), "Axiom datasets: ") {
		t.Errorf("datasets: %v", err)
	}
	if _, err := c.MintToken(ctx, tgt, "o1", app.AxiomTokenRequest{}); err == nil || !strings.HasPrefix(err.Error(), "Axiom token: ") {
		t.Errorf("token: %v", err)
	}
}

func TestOAuth(t *testing.T) {
	var reply struct {
		status int
		body   string
	}
	srv, got := server(t, func(w http.ResponseWriter, _ seen) {
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	})
	c := New()
	ctx := context.Background()

	reply.status, reply.body = 201, `{"client_id":"cid","client_name":"Keel"}`
	id, err := c.RegisterClient(ctx, srv.URL, "https://k.example/axiom/callback")
	if err != nil || id != "cid" {
		t.Fatalf("register %q %v", id, err)
	}
	reg := (*got)[0]
	var body map[string]any
	_ = json.Unmarshal(reg.body, &body)
	wantBody := map[string]any{"client_name": "Keel", "redirect_uris": []any{"https://k.example/axiom/callback"},
		"grant_types": []any{"authorization_code"}, "response_types": []any{"code"}, "token_endpoint_auth_method": "none"}
	if reg.path != "/oauth2/register" || reg.ctype != "application/json" || reg.auth != "" || !reflect.DeepEqual(body, wantBody) {
		t.Fatalf("register request %+v %v", reg, body)
	}
	for _, c2 := range []struct {
		status int
		body   string
		msg    string
	}{
		{400, `{"error":"invalid_redirect_uri","error_description":"plain http"}`, "plain http"},
		{400, `{"error":"invalid_redirect_uri"}`, "invalid_redirect_uri"},
		{400, `{"error":5,"error_description":"plain http"}`, "plain http"},
		{400, `{"error":"invalid_redirect_uri","error_description":7}`, "invalid_redirect_uri"},
		{502, `<html>`, "HTTP 502"},
		{200, `{}`, "HTTP 200"},
	} {
		reply.status, reply.body = c2.status, c2.body
		_, err := c.RegisterClient(ctx, srv.URL, "x")
		var oe *app.OAuthError
		if !errors.As(err, &oe) || oe.Error() != c2.msg {
			t.Errorf("register %d %s: %v", c2.status, c2.body, err)
		}
	}

	reply.status, reply.body = 200, `{"access_token":"at","token_type":"Bearer"}`
	tok, err := c.ExchangeCode(ctx, srv.URL, app.AxiomCodeExchange{ClientID: "cid", Code: "co de", Verifier: "ver", RedirectURI: "https://k.example/axiom/callback"})
	if err != nil || tok != "at" {
		t.Fatalf("exchange %q %v", tok, err)
	}
	ex := (*got)[len(*got)-1]
	form, _ := url.ParseQuery(string(ex.body))
	if ex.path != "/oauth2/token" || ex.ctype != "application/x-www-form-urlencoded" ||
		form.Get("grant_type") != "authorization_code" || form.Get("code") != "co de" || form.Get("code_verifier") != "ver" ||
		form.Get("redirect_uri") != "https://k.example/axiom/callback" || form.Get("client_id") != "cid" {
		t.Fatalf("exchange request %+v %v", ex, form)
	}
	reply.status, reply.body = 400, `{"error":"invalid_grant"}`
	if _, err := c.ExchangeCode(ctx, srv.URL, app.AxiomCodeExchange{}); err == nil || err.Error() != "invalid_grant" {
		t.Fatalf("exchange error %v", err)
	}
}

func TestForwardTraces(t *testing.T) {
	srv, got := server(t, func(w http.ResponseWriter, s seen) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	c := New()
	body := []byte{0x1f, 0x8b, 0, 1, 2}
	res, err := c.ForwardTraces(context.Background(), app.OTLPForward{Domain: srv.URL, Token: "sink-token", Dataset: "keel-traces",
		ContentType: "application/x-protobuf", ContentEncoding: "gzip", Body: body})
	if err != nil || res.Status != 200 || res.ContentType != "application/json" || string(res.Body) != `{"ok":true}` {
		t.Fatalf("reply %+v %v", res, err)
	}
	s := (*got)[0]
	if s.method != "POST" || s.path != "/v1/traces" || s.auth != "Bearer sink-token" || s.dataset != "keel-traces" ||
		s.ctype != "application/x-protobuf" || s.encoding != "gzip" || string(s.body) != string(body) {
		t.Fatalf("forward %+v", s)
	}
	srv.Close()
	if _, err := c.ForwardTraces(context.Background(), app.OTLPForward{Domain: srv.URL}); err == nil {
		t.Fatal("no error from a closed server")
	}
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"api.axiom.co":           "https://api.axiom.co",
		"http://127.0.0.1:4318/": "http://127.0.0.1:4318",
		"https://x.example//":    "https://x.example",
	} {
		if got := app.AxiomBaseURL(in); got != want {
			t.Errorf("AxiomBaseURL(%q) = %q", in, got)
		}
	}
	if !strings.HasPrefix(app.AxiomBaseURL("api.eu.axiom.co"), "https://") {
		t.Error("scheme")
	}
}
