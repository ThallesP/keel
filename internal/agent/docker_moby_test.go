package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/client"
)

type dockerAPI struct {
	mu   sync.Mutex
	reqs []string
}

var versionPrefix = regexp.MustCompile(`^/v1\.\d+`)

func (a *dockerAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	a.mu.Lock()
	a.reqs = append(a.reqs, r.Method+" "+path+"?"+r.URL.RawQuery)
	a.mu.Unlock()
	w.Header().Set("Api-Version", "1.47")
	switch {
	case path == "/_ping":
		io.WriteString(w, "OK")
	case r.Method != http.MethodGet:
		w.WriteHeader(http.StatusInternalServerError)
	case path == "/info":
		io.WriteString(w, `{"Name":"box","Swarm":{"NodeID":"swarm-node-1"}}`)
	case path == "/containers/json":
		io.WriteString(w, `[{"Id":"c1","Labels":{"com.docker.swarm.service.name":"svc-n1"},"State":"exited"}]`)
	case path == "/containers/c1/logs":
		w.Write(frame(1, "2024-01-01T00:00:01Z hi\n"))
	case path == "/events":
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, `{"Type":"container","Action":"start","Actor":{"ID":"c1","Attributes":{"com.docker.swarm.service.name":"svc-n1"}},"scope":"local","time":1704067200,"timeNano":1704067200000000001}`+"\n")
		io.WriteString(w, `{"Type":"node","Action":"update","Actor":{"ID":"nd","Attributes":{"name":"box"}},"scope":"swarm","time":1704067201,"timeNano":1704067201000000000}`+"\n")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (a *dockerAPI) requests() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.reqs)
}

func (a *dockerAPI) find(prefix string) url.Values {
	for _, r := range a.requests() {
		if strings.HasPrefix(r, prefix+"?") {
			q, _ := url.ParseQuery(strings.SplitN(r, "?", 2)[1])
			return q
		}
	}
	return nil
}

func newTestMoby(t *testing.T) (*MobyDocker, *dockerAPI) {
	t.Helper()
	api := &dockerAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	d, err := newMobyDocker(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithHTTPRequestHook(readOnly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d, api
}

func filtersOf(t *testing.T, q url.Values) map[string]map[string]bool {
	t.Helper()
	var f map[string]map[string]bool
	if err := json.Unmarshal([]byte(q.Get("filters")), &f); err != nil {
		t.Fatalf("filters %q: %v", q.Get("filters"), err)
	}
	return f
}

func TestMobyInfoAndContainers(t *testing.T) {
	d, api := newTestMoby(t)
	ctx := context.Background()
	info, err := d.Info(ctx)
	if err != nil || info != (NodeInfo{NodeID: "swarm-node-1", Name: "box"}) {
		t.Fatalf("info = %+v, %v", info, err)
	}
	cs, err := d.ListSwarmContainers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 || cs[0].ID != "c1" || cs[0].State != "exited" || cs[0].Labels[labelServiceName] != "svc-n1" {
		t.Fatalf("containers = %+v", cs)
	}
	q := api.find("GET /containers/json")
	if q.Get("all") != "1" {
		t.Errorf("all = %q", q.Get("all"))
	}
	want := map[string]map[string]bool{"label": {labelServiceName: true}, "status": {"running": true, "exited": true}}
	if got := filtersOf(t, q); !equalFilters(got, want) {
		t.Errorf("filters = %v, want %v", got, want)
	}
}

func TestMobyContainerLogsQuery(t *testing.T) {
	d, api := newTestMoby(t)
	for _, since := range []string{"", "1704067200.000000001"} {
		rc, err := d.ContainerLogs(context.Background(), "c1", since)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		rc.Close()
		var p FrameParser
		if f := p.Push(body); len(f) != 1 || f[0].Text != "2024-01-01T00:00:01Z hi\n" {
			t.Fatalf("frames = %+v", f)
		}
	}
	var logs []url.Values
	for _, r := range api.requests() {
		if strings.HasPrefix(r, "GET /containers/c1/logs?") {
			q, _ := url.ParseQuery(strings.SplitN(r, "?", 2)[1])
			logs = append(logs, q)
		}
	}
	if len(logs) != 2 {
		t.Fatalf("log requests = %v", api.requests())
	}
	for _, q := range logs {
		for _, k := range []string{"follow", "stdout", "stderr", "timestamps"} {
			if q.Get(k) != "1" {
				t.Errorf("%s = %q in %v", k, q.Get(k), q)
			}
		}
	}
	if logs[0].Get("tail") != "0" || logs[0].Has("since") {
		t.Errorf("no resume point: %v, want tail=0", logs[0])
	}
	if logs[1].Get("since") != "1704067200.000000001" || logs[1].Has("tail") {
		t.Errorf("resume point: %v, want since unchanged", logs[1])
	}
}

func TestMobyEvents(t *testing.T) {
	d, api := newTestMoby(t)
	s, err := d.Events(context.Background(), "1704067200.000000001")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e1, err := s.Next()
	if err != nil {
		t.Fatal(err)
	}
	if e1.Type != "container" || e1.Action != "start" || e1.ActorID != "c1" || e1.Attributes[labelServiceName] != "svc-n1" || e1.TimeNano != 1704067200000000001 {
		t.Fatalf("event = %+v", e1)
	}
	var raw map[string]any
	if err := json.Unmarshal(e1.Raw, &raw); err != nil {
		t.Fatal(err)
	}
	actor, _ := raw["Actor"].(map[string]any)
	attrs, _ := actor["Attributes"].(map[string]any)
	if raw["Type"] != "container" || raw["Action"] != "start" || attrs[labelServiceName] != "svc-n1" || raw["time"] != float64(1704067200) {
		t.Fatalf("raw = %s", e1.Raw)
	}
	if e2, err := s.Next(); err != nil || e2.Type != "node" {
		t.Fatalf("second event = %+v, %v", e2, err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v, want io.EOF", err)
	}
	q := api.find("GET /events")
	if q.Get("since") != "1704067200.000000001" {
		t.Errorf("since = %q", q.Get("since"))
	}
	want := map[string]map[string]bool{"type": {"container": true, "service": true, "node": true}}
	if got := filtersOf(t, q); !equalFilters(got, want) {
		t.Errorf("filters = %v", got)
	}
}

func TestMobyReadOnly(t *testing.T) {
	d, api := newTestMoby(t)
	_, err := d.cli.ContainerRemove(context.Background(), "c1", client.ContainerRemoveOptions{Force: true})
	if err == nil || !strings.Contains(err.Error(), "keel agent is read-only on Docker: refusing DELETE") {
		t.Fatalf("remove = %v, want refused", err)
	}
	_, err = d.cli.ContainerStart(context.Background(), "c1", client.ContainerStartOptions{})
	if err == nil || !strings.Contains(err.Error(), "refusing POST") {
		t.Fatalf("start = %v, want refused", err)
	}
	for _, r := range api.requests() {
		if !strings.HasPrefix(r, "GET ") && !strings.HasPrefix(r, "HEAD /_ping") {
			t.Fatalf("the daemon saw %s", r)
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if readOnly(httptest.NewRequest(m, "/containers/c1", nil)) == nil {
			t.Errorf("%s allowed", m)
		}
	}
}

func TestNewMobyDockerIsReadOnly(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix socket: %v", err)
	}
	api := &dockerAPI{}
	srv := httptest.NewUnstartedServer(api)
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	d, err := NewMobyDocker(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if info, err := d.Info(context.Background()); err != nil || info.NodeID != "swarm-node-1" {
		t.Fatalf("info over the socket = %+v, %v", info, err)
	}
	if _, err := d.cli.ContainerRemove(context.Background(), "c1", client.ContainerRemoveOptions{}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("remove = %v, want refused", err)
	}
	for _, r := range api.requests() {
		if !strings.HasPrefix(r, "GET ") && !strings.HasPrefix(r, "HEAD /_ping") {
			t.Fatalf("the daemon saw %s", r)
		}
	}
}

func equalFilters(a, b map[string]map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv := b[k]
		if len(av) != len(bv) {
			return false
		}
		for v := range av {
			if !bv[v] {
				return false
			}
		}
	}
	return true
}
