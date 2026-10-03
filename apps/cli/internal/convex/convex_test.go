package convex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCall(t *testing.T) {
	var body map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		switch r.URL.Path {
		case "/api/query":
			w.Write([]byte(`{"status":"success","value":{"replicas":1.0}}`))
		case "/api/mutation":
			w.WriteHeader(400)
			w.Write([]byte(`{"status":"error","errorMessage":"[Request ID: abc] Server Error\nUncaught ConvexError: Nothing to ship\n    at handler (x.ts:1)","errorData":"Nothing to ship"}`))
		case "/api/action":
			w.WriteHeader(401)
			w.Write([]byte(`{"code":"Unauthenticated","message":"Token expired"}`))
		}
	}))
	defer srv.Close()
	c := &Client{URL: srv.URL, HTTP: srv.Client()}

	var out struct {
		Replicas float64 `json:"replicas"`
	}
	if err := c.Query(context.Background(), "nodes:list", nil, &out); err != nil || out.Replicas != 1 {
		t.Fatalf("query: %v, %+v", err, out)
	}
	if string(body["args"]) != "{}" || string(body["path"]) != `"nodes:list"` {
		t.Errorf("nil args sent as %s", body["args"])
	}

	err := c.Mutation(context.Background(), "deployments:start", map[string]any(nil), nil)
	var fe *FunctionError
	if !errors.As(err, &fe) {
		t.Fatalf("mutation error = %v", err)
	}
	if fe.DataString() != "Nothing to ship" || fe.Message != "ConvexError: Nothing to ship" {
		t.Errorf("FunctionError = %q / %q", fe.DataString(), fe.Message)
	}

	if err := c.Action(context.Background(), "logs:tail", nil, nil); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("401 without an envelope = %v, want ErrUnauthenticated", err)
	}
}
