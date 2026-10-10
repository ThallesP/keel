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
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/events"
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

func (a *dockerAPI) queries(prefix string) []url.Values {
	var out []url.Values
	for _, r := range a.requests() {
		if query, ok := strings.CutPrefix(r, prefix+"?"); ok {
			q, _ := url.ParseQuery(query)
			out = append(out, q)
		}
	}
	return out
}

func newTestMoby(t *testing.T) (*MobyDocker, *dockerAPI) {
	t.Helper()
	api := &dockerAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	cli, err := client.New(client.WithHost("tcp://"+strings.TrimPrefix(srv.URL, "http://")), client.WithHTTPRequestHook(readOnly))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return &MobyDocker{cli: cli}, api
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
	q := api.queries("GET /containers/json")[0]
	if q.Get("all") != "1" {
		t.Errorf("all = %q", q.Get("all"))
	}
	want := map[string]map[string]bool{"label": {labelServiceName: true}, "status": {"running": true, "exited": true}}
	if got := filtersOf(t, q); !reflect.DeepEqual(got, want) {
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
	logs := api.queries("GET /containers/c1/logs")
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
	s := d.Events(context.Background(), "1704067200.000000001")
	want := events.Message{Type: "container", Action: "start", Actor: events.Actor{ID: "c1", Attributes: map[string]string{labelServiceName: "svc-n1"}},
		Scope: "local", Time: 1704067200, TimeNano: 1704067200000000001}
	if e1, err := s.Next(); err != nil || !reflect.DeepEqual(e1, want) {
		t.Fatalf("event = %+v, %v", e1, err)
	}
	if e2, err := s.Next(); err != nil || e2.Type != "node" {
		t.Fatalf("second event = %+v, %v", e2, err)
	}
	if _, err := s.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("end = %v, want io.EOF", err)
	}
	q := api.queries("GET /events")[0]
	if q.Get("since") != "1704067200.000000001" {
		t.Errorf("since = %q", q.Get("since"))
	}
	if got := filtersOf(t, q); !reflect.DeepEqual(got, map[string]map[string]bool{"type": {"container": true, "service": true, "node": true}}) {
		t.Errorf("filters = %v", got)
	}
}

func TestMobyEventsDockerDown(t *testing.T) {
	d, err := NewMobyDocker(filepath.Join(t.TempDir(), "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s := d.Events(context.Background(), "")
	if _, err := s.Next(); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("first Next = %v, want the connect error", err)
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
