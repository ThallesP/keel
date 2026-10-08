// Package realtime is the dashboard's WebSocket (GET /api/ws) on a centrifuge server, and the
// app.Publisher that pushes invalidations through it. See docs/go/ARCHITECTURE.md, "Realtime",
// and docs/go/spec/web-data.md §9–§10.
//
// Protocol, as the dashboard's centrifuge JS client sees it:
//
//   - Connect auth is the session (cookie or bearer), resolved by Config.Authenticate on the
//     upgrade request. The client sends no token and picks no channels.
//   - Signed out: the connection is closed with code 4501 "signed out" (terminal, the client does
//     not reconnect). Same code when the session is signed out later (DisconnectSession).
//   - Signed in without an organization: connected, no subscriptions.
//   - Member: a server-side subscription to "org:<organizationId>".
//   - Publications on that channel: {"type":"invalidate","topics":["/api/environments/<id>", …]}.
//     Topics are API path prefixes; the client refetches every query whose key starts with one.
//     Publishes are coalesced per organization for Config.Window (100 ms) with topics deduped.
//   - Code 4001 "membership changed" (DisconnectUser) asks the client to reconnect: the new
//     connection gets the user's current organization channel, and the client's reconnect
//     handler invalidates everything.
package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Disconnect codes Keel sends (centrifuge: 4000–4499 reconnect, 4500–4999 terminal).
var (
	// DisconnectSignedOut: no (or no longer a) valid session. Terminal.
	DisconnectSignedOut = centrifuge.Disconnect{Code: 4501, Reason: "signed out"}
	// DisconnectMembershipChanged: reconnect to be subscribed to the user's current organization.
	DisconnectMembershipChanged = centrifuge.Disconnect{Code: 4001, Reason: "membership changed"}
)

// DefaultWindow is how long publishes to one organization are collected before one message goes
// out (web-data.md §10.4: "coalesce per channel for about 100 ms and dedupe topics").
const DefaultWindow = 100 * time.Millisecond

// Config configures the server.
type Config struct {
	// Authenticate resolves the caller of the upgrade request. The zero Actor is signed out; an
	// error is a server failure (the client is told to retry). Required. serve wires it to
	// app.ResolveSession(transport.SessionToken(r)).
	Authenticate func(r *http.Request) (domain.Actor, error)
	// SiteURL is the dashboard URL as users open it (KEEL_SITE_URL). Its host is accepted as an
	// Origin besides the request's own Host.
	SiteURL string
	// Window is the per-organization coalescing window. 0 = DefaultWindow; negative = publish
	// immediately.
	Window time.Duration
	Log    *slog.Logger
}

// Invalidation is the only message published.
type Invalidation struct {
	Type   string   `json:"type"` // "invalidate"
	Topics []string `json:"topics"`
}

// Channel is an organization's channel name.
func Channel(organizationID string) string { return "org:" + organizationID }

// Server is the centrifuge node, its WebSocket handler and the publisher.
type Server struct {
	node     *centrifuge.Node
	auth     func(r *http.Request) (domain.Actor, error)
	siteHost string
	window   time.Duration
	log      *slog.Logger

	mu       sync.Mutex
	closed   bool
	pending  map[string]*batch                           // organization → topics waiting for the window
	sessions map[string]map[*centrifuge.Client]struct{} // Keel session id → its connections
}

type batch struct {
	topics map[string]struct{}
	timer  *time.Timer
}

var _ app.Publisher = (*Server)(nil)

type connKey struct{}

// conn is what the upgrade request resolved, carried to OnConnecting in the request context.
type conn struct {
	actor domain.Actor
	err   error
}

// New starts a centrifuge node. Call Shutdown to stop it.
func New(cfg Config) (*Server, error) {
	if cfg.Authenticate == nil {
		return nil, errors.New("realtime: Config.Authenticate is required")
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	window := cfg.Window
	if window == 0 {
		window = DefaultWindow
	}
	s := &Server{
		auth:     cfg.Authenticate,
		window:   window,
		log:      log,
		pending:  map[string]*batch{},
		sessions: map[string]map[*centrifuge.Client]struct{}{},
	}
	if u, err := url.Parse(cfg.SiteURL); err == nil {
		s.siteHost = u.Host
	}

	node, err := centrifuge.New(centrifuge.Config{
		Name:       "keel",
		LogLevel:   centrifuge.LogLevelWarn,
		LogHandler: s.logEntry,
		// A private registry: the node's metrics are not exported, and tests can build several.
		Metrics: centrifuge.MetricsConfig{RegistererGatherer: prometheus.NewRegistry()},
	})
	if err != nil {
		return nil, err
	}
	s.node = node
	node.OnConnecting(s.onConnecting)
	node.OnConnect(s.onConnect)
	if err := node.Run(); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler is GET /api/ws. It rejects a foreign Origin with 403, resolves the session, then
// upgrades; the connect step decides what the connection may see.
func (s *Server) Handler() http.Handler {
	ws := centrifuge.NewWebsocketHandler(s.node, centrifuge.WebsocketConfig{
		// Checked below, before the session lookup.
		CheckOrigin: func(*http.Request) bool { return true },
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.originAllowed(r) {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
		actor, err := s.auth(r)
		ctx := context.WithValue(r.Context(), connKey{}, conn{actor: actor, err: err})
		ws.ServeHTTP(w, r.WithContext(ctx))
	})
}

// originAllowed: browsers always send Origin on a WebSocket handshake; it must name the host the
// request was sent to (same origin) or the configured dashboard host. No Origin = not a browser
// (no ambient-credential attack), allowed; the session is still required.
func (s *Server) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return s.siteHost != "" && strings.EqualFold(u.Host, s.siteHost)
}

func (s *Server) onConnecting(ctx context.Context, _ centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
	c, ok := ctx.Value(connKey{}).(conn)
	if !ok {
		return centrifuge.ConnectReply{}, centrifuge.DisconnectServerError
	}
	if c.err != nil {
		s.log.Error("realtime: resolve session", "err", c.err)
		return centrifuge.ConnectReply{}, centrifuge.DisconnectServerError
	}
	if !c.actor.SignedIn() {
		return centrifuge.ConnectReply{}, DisconnectSignedOut
	}
	reply := centrifuge.ConnectReply{Credentials: &centrifuge.Credentials{UserID: c.actor.UserID}}
	if c.actor.OrganizationID != "" {
		reply.Subscriptions = map[string]centrifuge.SubscribeOptions{Channel(c.actor.OrganizationID): {}}
	}
	return reply, nil
}

// onConnect tracks the connection under its session so DisconnectSession can find it. Client-side
// subscribe, publish, RPC, presence and history have no handlers, so centrifuge refuses them: the
// server alone decides what a connection receives.
func (s *Server) onConnect(client *centrifuge.Client) {
	c, _ := client.Context().Value(connKey{}).(conn)
	sid := c.actor.SessionID
	if sid == "" {
		return
	}
	s.mu.Lock()
	set := s.sessions[sid]
	if set == nil {
		set = map[*centrifuge.Client]struct{}{}
		s.sessions[sid] = set
	}
	set[client] = struct{}{}
	s.mu.Unlock()
	client.OnDisconnect(func(centrifuge.DisconnectEvent) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if set := s.sessions[sid]; set != nil {
			delete(set, client)
			if len(set) == 0 {
				delete(s.sessions, sid)
			}
		}
	})
}

// Publish queues topics for the organization's channel; one message per organization goes out at
// the end of the window with every topic queued meanwhile, deduped and sorted. Safe to call from
// any goroutine; a no-op after Shutdown.
func (s *Server) Publish(organizationID string, topics []string) {
	if organizationID == "" || len(topics) == 0 {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	b := s.pending[organizationID]
	fresh := b == nil
	if fresh {
		b = &batch{topics: map[string]struct{}{}}
		s.pending[organizationID] = b
	}
	for _, t := range topics {
		if t != "" {
			b.topics[t] = struct{}{}
		}
	}
	if fresh && s.window > 0 {
		b.timer = time.AfterFunc(s.window, func() { s.flush(organizationID) })
	}
	s.mu.Unlock()
	if fresh && s.window <= 0 {
		s.flush(organizationID)
	}
}

func (s *Server) flush(organizationID string) {
	s.mu.Lock()
	b := s.pending[organizationID]
	delete(s.pending, organizationID)
	closed := s.closed
	s.mu.Unlock()
	if b == nil || closed || len(b.topics) == 0 {
		return
	}
	topics := make([]string, 0, len(b.topics))
	for t := range b.topics {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	data, err := json.Marshal(Invalidation{Type: "invalidate", Topics: topics})
	if err != nil {
		s.log.Error("realtime: encode invalidation", "err", err)
		return
	}
	if _, err := s.node.Publish(Channel(organizationID), data); err != nil {
		s.log.Error("realtime: publish", "org", organizationID, "err", err)
	}
}

// DisconnectSession closes every connection opened with the session, with the terminal code
// 4501 "signed out". The auth area calls it when a session ends (sign-out, deletion); a tab that
// reconnects anyway is rejected at connect because its session no longer resolves.
func (s *Server) DisconnectSession(sessionID string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	clients := make([]*centrifuge.Client, 0, len(s.sessions[sessionID]))
	for c := range s.sessions[sessionID] {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.Disconnect(DisconnectSignedOut)
	}
}

// DisconnectUser closes every connection of the user with the reconnecting code 4001
// "membership changed". The auth area calls it when the user's organization membership changes
// (founding, joining, removal): on reconnect the server subscribes the connection to the
// organization the user is in now, and the client invalidates every query.
func (s *Server) DisconnectUser(userID string) {
	if userID == "" {
		return
	}
	if err := s.node.Disconnect(userID, centrifuge.WithCustomDisconnect(DisconnectMembershipChanged)); err != nil {
		s.log.Error("realtime: disconnect user", "err", err)
	}
}

// Shutdown drops queued publications and closes every connection (centrifuge's "shutdown" code,
// which clients reconnect after; reconnecting refetches everything, so nothing queued is lost).
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for org, b := range s.pending {
		if b.timer != nil {
			b.timer.Stop()
		}
		delete(s.pending, org)
	}
	s.mu.Unlock()
	return s.node.Shutdown(ctx)
}

func (s *Server) logEntry(e centrifuge.LogEntry) {
	level := slog.LevelInfo
	switch e.Level {
	case centrifuge.LogLevelTrace, centrifuge.LogLevelDebug:
		level = slog.LevelDebug
	case centrifuge.LogLevelWarn:
		level = slog.LevelWarn
	case centrifuge.LogLevelError:
		level = slog.LevelError
	}
	args := make([]any, 0, 2*len(e.Fields))
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, k, e.Fields[k])
	}
	s.log.Log(context.Background(), level, "realtime: "+e.Message, args...)
}
