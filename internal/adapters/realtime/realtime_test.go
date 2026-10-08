package realtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/ThallesP/keel/internal/domain"
)

// Actors by bearer token. "boom" makes Authenticate fail.
var actors = map[string]domain.Actor{
	"alice":   {UserID: "u-alice", OrganizationID: "org-a", SessionID: "s-alice-1"},
	"alice2":  {UserID: "u-alice", OrganizationID: "org-a", SessionID: "s-alice-2"},
	"bob":     {UserID: "u-bob", OrganizationID: "org-b", SessionID: "s-bob"},
	"newbie":  {UserID: "u-newbie", SessionID: "s-newbie"}, // signed in, no organization yet
	"expired": {},                                          // token that resolves to signed out
}

func authenticate(r *http.Request) (domain.Actor, error) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "boom" {
		return domain.Actor{}, errors.New("database is down")
	}
	return actors[tok], nil
}

type harness struct {
	t   *testing.T
	rt  *Server
	srv *httptest.Server
}

func newHarness(t *testing.T, window time.Duration) *harness {
	t.Helper()
	rt, err := New(Config{
		Authenticate: authenticate,
		SiteURL:      "https://keel.example.com",
		Window:       window,
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/ws", rt.Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Shutdown(ctx)
		srv.Close()
	})
	return &harness{t: t, rt: rt, srv: srv}
}

// message is one centrifuge protocol v2 JSON message (a reply or a push).
type message struct {
	ID    uint32 `json:"id"`
	Error *struct {
		Code    uint32 `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Connect *struct {
		Client string                     `json:"client"`
		Subs   map[string]json.RawMessage `json:"subs"`
	} `json:"connect"`
	Subscribe json.RawMessage `json:"subscribe"`
	Push      *struct {
		Channel string `json:"channel"`
		Pub     *struct {
			Data json.RawMessage `json:"data"`
		} `json:"pub"`
	} `json:"push"`
}

// client is a raw WebSocket speaking centrifuge's JSON protocol, as the dashboard's JS client does.
type client struct {
	t      *testing.T
	ws     *websocket.Conn
	msgs   chan message
	closed chan error // the read error that ended the connection
	nextID uint32
}

func (h *harness) dial(token, origin string) (*client, *http.Response, error) {
	h.t.Helper()
	hdr := http.Header{}
	if token != "" {
		hdr.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http")+"/api/ws",
		&websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		return nil, resp, err
	}
	c := &client{t: h.t, ws: ws, msgs: make(chan message, 64), closed: make(chan error, 1)}
	go c.readLoop()
	h.t.Cleanup(func() { ws.CloseNow() })
	return c, resp, nil
}

func (c *client) readLoop() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.closed <- err
			close(c.msgs)
			return
		}
		// One frame may carry several newline-separated messages; "{}" is a ping.
		dec := json.NewDecoder(bytes.NewReader(data))
		for {
			var m message
			if err := dec.Decode(&m); err != nil {
				break
			}
			if m.ID == 0 && m.Push == nil {
				continue // ping
			}
			c.msgs <- m
		}
	}
}

func (c *client) send(cmd map[string]any) uint32 {
	c.t.Helper()
	c.nextID++
	cmd["id"] = c.nextID
	b, _ := json.Marshal(cmd)
	if err := c.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	return c.nextID
}

// next is the next message, failing after 2 s.
func (c *client) next() message {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatalf("connection closed: %v", <-c.closed)
		}
		return m
	case <-time.After(2 * time.Second):
		c.t.Fatal("no message within 2s")
	}
	return message{}
}

// quiet asserts nothing arrives for d.
func (c *client) quiet(d time.Duration) {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if ok {
			c.t.Fatalf("unexpected message: %+v", m)
		}
	case <-time.After(d):
	}
}

// closeStatus waits for the server to close the connection and returns the close code.
func (c *client) closeStatus() websocket.StatusCode {
	c.t.Helper()
	for {
		select {
		case _, ok := <-c.msgs:
			if !ok {
				return websocket.CloseStatus(<-c.closed)
			}
		case <-time.After(2 * time.Second):
			c.t.Fatal("connection not closed within 2s")
		}
	}
}

func (c *client) connect() message {
	c.t.Helper()
	id := c.send(map[string]any{"connect": map[string]any{}})
	m := c.next()
	if m.ID != id {
		c.t.Fatalf("reply id %d, want %d", m.ID, id)
	}
	return m
}

func (h *harness) connected(token string) *client {
	h.t.Helper()
	c, _, err := h.dial(token, "")
	if err != nil {
		h.t.Fatalf("dial %s: %v", token, err)
	}
	if m := c.connect(); m.Connect == nil {
		h.t.Fatalf("connect %s: %+v", token, m)
	}
	return c
}

func invalidation(t *testing.T, m message, channel string) Invalidation {
	t.Helper()
	if m.Push == nil || m.Push.Pub == nil {
		t.Fatalf("not a publication: %+v", m)
	}
	if m.Push.Channel != channel {
		t.Fatalf("publication on %q, want %q", m.Push.Channel, channel)
	}
	var inv Invalidation
	if err := json.Unmarshal(m.Push.Pub.Data, &inv); err != nil {
		t.Fatalf("payload %s: %v", m.Push.Pub.Data, err)
	}
	return inv
}

func TestSignedOutRejected(t *testing.T) {
	h := newHarness(t, -1)
	for _, token := range []string{"", "expired", "unknown"} {
		c, _, err := h.dial(token, "")
		if err != nil {
			t.Fatalf("dial %q: %v", token, err)
		}
		c.send(map[string]any{"connect": map[string]any{}})
		if got := c.closeStatus(); got != 4501 {
			t.Fatalf("token %q: close %d, want 4501 (signed out)", token, got)
		}
	}
}

func TestAuthenticateErrorAsksToRetry(t *testing.T) {
	h := newHarness(t, -1)
	c, _, err := h.dial("boom", "")
	if err != nil {
		t.Fatal(err)
	}
	c.send(map[string]any{"connect": map[string]any{}})
	if got := c.closeStatus(); got != 3004 { // centrifuge "internal server error": reconnect
		t.Fatalf("close %d, want 3004", got)
	}
}

func TestMemberReceivesOrganizationPublications(t *testing.T) {
	h := newHarness(t, 30*time.Millisecond)
	c, _, err := h.dial("alice", "")
	if err != nil {
		t.Fatal(err)
	}
	m := c.connect()
	if m.Connect == nil {
		t.Fatalf("connect reply: %+v", m)
	}
	if _, ok := m.Connect.Subs["org:org-a"]; !ok || len(m.Connect.Subs) != 1 {
		t.Fatalf("server-side subscriptions = %v, want exactly org:org-a", m.Connect.Subs)
	}

	// Two writes inside the window: one message, topics deduped and sorted.
	h.rt.Publish("org-a", []string{"/api/projects", "/api/environments/e1"})
	h.rt.Publish("org-a", []string{"/api/projects", "/api/nodes/n1"})
	got := invalidation(t, c.next(), "org:org-a")
	want := Invalidation{Type: "invalidate", Topics: []string{"/api/environments/e1", "/api/nodes/n1", "/api/projects"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invalidation = %+v, want %+v", got, want)
	}
	c.quiet(80 * time.Millisecond)

	// The next window is a new message.
	h.rt.Publish("org-a", []string{"/api/organization"})
	if got := invalidation(t, c.next(), "org:org-a"); !reflect.DeepEqual(got.Topics, []string{"/api/organization"}) {
		t.Fatalf("second invalidation = %+v", got)
	}
}

func TestPayloadWireShape(t *testing.T) {
	h := newHarness(t, -1)
	c := h.connected("alice")
	h.rt.Publish("org-a", []string{"/api/projects"})
	m := c.next()
	if string(m.Push.Pub.Data) != `{"type":"invalidate","topics":["/api/projects"]}` {
		t.Fatalf("payload = %s", m.Push.Pub.Data)
	}
}

func TestNoCrossOrganizationLeakage(t *testing.T) {
	h := newHarness(t, -1)
	alice := h.connected("alice")
	bob := h.connected("bob")

	h.rt.Publish("org-b", []string{"/api/environments/bob-env"})
	if got := invalidation(t, bob.next(), "org:org-b"); got.Topics[0] != "/api/environments/bob-env" {
		t.Fatalf("bob got %+v", got)
	}
	alice.quiet(50 * time.Millisecond)

	// A client cannot subscribe itself to another organization's channel (or any channel).
	id := alice.send(map[string]any{"subscribe": map[string]any{"channel": "org:org-b"}})
	reply := alice.next()
	if reply.ID != id || reply.Error == nil || reply.Subscribe != nil {
		t.Fatalf("client-side subscribe to org:org-b was not refused: %+v", reply)
	}
	h.rt.Publish("org-b", []string{"/api/projects"})
	h.rt.Publish("org-a", []string{"/api/nodes/alice-node"})
	// Alice's next publication is her own: Bob's never reached her.
	if got := invalidation(t, alice.next(), "org:org-a"); got.Topics[0] != "/api/nodes/alice-node" {
		t.Fatalf("alice got %+v", got)
	}
	if got := invalidation(t, bob.next(), "org:org-b"); got.Topics[0] != "/api/projects" {
		t.Fatalf("bob got %+v", got)
	}
	bob.quiet(50 * time.Millisecond)
}

func TestSignedInWithoutOrganizationHasNoSubscriptions(t *testing.T) {
	h := newHarness(t, -1)
	c, _, err := h.dial("newbie", "")
	if err != nil {
		t.Fatal(err)
	}
	m := c.connect()
	if m.Connect == nil || len(m.Connect.Subs) != 0 {
		t.Fatalf("connect reply = %+v, want connected without subscriptions", m)
	}
	h.rt.Publish("org-a", []string{"/api/projects"})
	h.rt.Publish("", []string{"/api/projects"})
	c.quiet(50 * time.Millisecond)
}

func TestOrigin(t *testing.T) {
	h := newHarness(t, -1)
	host := strings.TrimPrefix(h.srv.URL, "http://")
	for _, tc := range []struct {
		origin string
		ok     bool
	}{
		{"", true},                          // not a browser
		{"http://" + host, true},            // same host as the request
		{"https://keel.example.com", true},  // KEEL_SITE_URL's host
		{"https://KEEL.example.com", true},  // hosts compare case-insensitively
		{"https://evil.example.com", false}, // anything else
		{"https://keel.example.com.evil.io", false},
		{"null", false},
	} {
		c, resp, err := h.dial("alice", tc.origin)
		if tc.ok {
			if err != nil {
				t.Fatalf("origin %q refused: %v", tc.origin, err)
			}
			if m := c.connect(); m.Connect == nil {
				t.Fatalf("origin %q: connect %+v", tc.origin, m)
			}
			continue
		}
		if err == nil {
			t.Fatalf("origin %q accepted", tc.origin)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("origin %q: response %v, want 403", tc.origin, resp)
		}
	}
}

func TestDisconnectSession(t *testing.T) {
	h := newHarness(t, -1)
	tab1 := h.connected("alice")
	tab2 := h.connected("alice")
	other := h.connected("alice2") // same user, another session (e.g. the CLI's)

	h.rt.DisconnectSession("s-alice-1")
	for _, c := range []*client{tab1, tab2} {
		if got := c.closeStatus(); got != 4501 {
			t.Fatalf("close %d, want 4501", got)
		}
	}
	h.rt.Publish("org-a", []string{"/api/projects"})
	invalidation(t, other.next(), "org:org-a")

	h.rt.DisconnectSession("")        // no-op
	h.rt.DisconnectSession("missing") // no-op
	h.rt.Publish("org-a", []string{"/api/projects"})
	invalidation(t, other.next(), "org:org-a")
}

func TestDisconnectUserReconnects(t *testing.T) {
	h := newHarness(t, -1)
	a1 := h.connected("alice")
	a2 := h.connected("alice2")
	bob := h.connected("bob")

	h.rt.DisconnectUser("u-alice")
	for _, c := range []*client{a1, a2} {
		if got := c.closeStatus(); got != 4001 {
			t.Fatalf("close %d, want 4001 (membership changed, reconnect)", got)
		}
	}
	h.rt.Publish("org-b", []string{"/api/projects"})
	invalidation(t, bob.next(), "org:org-b")
}

func TestShutdown(t *testing.T) {
	h := newHarness(t, time.Hour)
	c := h.connected("alice")
	h.rt.Publish("org-a", []string{"/api/projects"}) // queued for an hour
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.rt.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got := c.closeStatus(); got != 3001 { // centrifuge "shutdown": clients reconnect
		t.Fatalf("close %d, want 3001", got)
	}
	h.rt.Publish("org-a", []string{"/api/projects"}) // no-op, no panic
}

func TestNewRequiresAuthenticate(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without Authenticate succeeded")
	}
}
