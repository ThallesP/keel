package realtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

var (
	DisconnectSignedOut         = centrifuge.Disconnect{Code: 4501, Reason: "signed out"}
	DisconnectMembershipChanged = centrifuge.Disconnect{Code: 4001, Reason: "membership changed"}
)

const DefaultWindow = 100 * time.Millisecond

type Config struct {
	Authenticate func(r *http.Request) (domain.Actor, error)
	SiteURL      string
	Window       time.Duration
	Log          *slog.Logger
}

type Invalidation struct {
	Type   string   `json:"type"`
	Topics []string `json:"topics"`
}

func Channel(organizationID string) string { return "org:" + organizationID }

type Server struct {
	node     *centrifuge.Node
	auth     func(r *http.Request) (domain.Actor, error)
	siteHost string
	window   time.Duration
	log      *slog.Logger

	mu       sync.Mutex
	closed   bool
	pending  map[string]*batch
	sessions map[string]map[*centrifuge.Client]struct{}
}

type batch struct {
	topics map[string]struct{}
	timer  *time.Timer
}

var _ app.Publisher = (*Server)(nil)

type connKey struct{}

type conn struct {
	actor domain.Actor
	err   error
}

func New(cfg Config) (*Server, error) {
	if cfg.Authenticate == nil {
		return nil, errors.New("realtime: Config.Authenticate is required")
	}
	window := cfg.Window
	if window == 0 {
		window = DefaultWindow
	}
	s := &Server{
		auth:     cfg.Authenticate,
		window:   window,
		log:      cfg.Log,
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
		Metrics:    centrifuge.MetricsConfig{RegistererGatherer: prometheus.NewRegistry()},
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

func (s *Server) Handler() http.Handler {
	ws := centrifuge.NewWebsocketHandler(s.node, centrifuge.WebsocketConfig{
		CheckOrigin: func(*http.Request) bool { return true },
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.originAllowed(r) {
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}
		actor, err := s.auth(r)
		ctx := context.WithValue(r.Context(), connKey{}, conn{actor: actor, err: err})
		ws.ServeHTTP(cookieCarrier{w}, r.WithContext(ctx))
	})
}

type cookieCarrier struct{ http.ResponseWriter }

func (c cookieCarrier) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c cookieCarrier) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	nc, brw, err := h.Hijack()
	if err != nil || len(c.Header().Values("Set-Cookie")) == 0 {
		return nc, brw, err
	}
	var extra []byte
	for _, v := range c.Header().Values("Set-Cookie") {
		extra = append(extra, "Set-Cookie: "...)
		for i := 0; i < len(v); i++ {
			if b := v[i]; b > 31 && b != 127 {
				extra = append(extra, b)
			} else {
				extra = append(extra, ' ')
			}
		}
		extra = append(extra, "\r\n"...)
	}
	return &handshakeConn{Conn: nc, extra: extra}, brw, nil
}

type handshakeConn struct {
	net.Conn
	extra []byte
	done  atomic.Bool
}

func (c *handshakeConn) Write(p []byte) (int, error) {
	if c.done.Swap(true) {
		return c.Conn.Write(p)
	}
	end := bytes.Index(p, []byte("\r\n\r\n"))
	if end < 0 || !bytes.HasPrefix(p, []byte("HTTP/1.1 101 ")) {
		return c.Conn.Write(p)
	}
	out := make([]byte, 0, len(p)+len(c.extra))
	out = append(out, p[:end+2]...)
	out = append(out, c.extra...)
	out = append(out, p[end+2:]...)
	if _, err := c.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

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

func (s *Server) DisconnectUser(userID string) {
	if userID == "" {
		return
	}
	if err := s.node.Disconnect(userID, centrifuge.WithCustomDisconnect(DisconnectMembershipChanged)); err != nil {
		s.log.Error("realtime: disconnect user", "err", err)
	}
}

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
