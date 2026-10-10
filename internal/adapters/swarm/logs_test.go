package swarm

// The Docker log reader against a fake Engine API (no Docker needed).

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
)

func TestReadServiceLogs(t *testing.T) {
	var query string
	s := fakeEngine(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/services/svc-abc/logs"):
			query = r.URL.RawQuery
			_, _ = w.Write([]byte{1, 0, 0, 0, 0, 0, 0, 2, 'h', '\n'})
		case strings.HasSuffix(r.URL.Path, "/services/svc-gone/logs"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"message":"service svc-gone not found"}`))
		default:
			w.WriteHeader(500)
		}
	})
	body, found, err := s.ReadServiceLogs(context.Background(), "svc-abc", 300)
	if err != nil || !found || string(body) != "\x01\x00\x00\x00\x00\x00\x00\x02h\n" {
		t.Fatalf("logs %q %v %v", body, found, err)
	}
	for _, p := range []string{"stdout=1", "stderr=1", "tail=300", "timestamps=1", "details=1"} {
		if !strings.Contains(query, p) {
			t.Errorf("query %q lacks %s", query, p)
		}
	}
	if strings.Contains(query, "follow") {
		t.Errorf("query %q follows", query)
	}
	if _, found, err := s.ReadServiceLogs(context.Background(), "svc-gone", 10); err != nil || found {
		t.Fatalf("missing service: %v %v", found, err)
	}
	if _, _, err := s.ReadServiceLogs(context.Background(), "svc-err", 10); err == nil {
		t.Fatal("no error on a 500")
	}
}

func TestListLogReplicas(t *testing.T) {
	var filters string
	s := fakeEngine(t, func(w http.ResponseWriter, r *http.Request) {
		filters = r.URL.Query().Get("filters")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ID":"t1","Slot":2,"Status":{"State":"running"}},{"ID":"t0","Status":{}}]`))
	})
	tasks, err := s.ListLogReplicas(context.Background(), "svc-abc")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tasks, []app.LogReplica{{ID: "t1", Slot: 2, State: "running"}, {ID: "t0"}}) {
		t.Fatalf("tasks %+v", tasks)
	}
	if !strings.Contains(filters, `"service":{"svc-abc":true}`) {
		t.Errorf("filters %s", filters)
	}
}
