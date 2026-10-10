package realtime

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

var DisconnectSignedOut = centrifuge.Disconnect{Code: 4501, Reason: "signed out"}

type Config struct {
	Actor   func(context.Context) domain.Actor
	SiteURL string
	Window  time.Duration
	Log     *slog.Logger
}

type Invalidation struct {
	Type   string   `json:"type"`
	Topics []string `json:"topics"`
}

func Channel(organizationID string) string { return "org:" + organizationID }

type Server struct {
	node     *centrifuge.Node
	actor    func(context.Context) domain.Actor
	siteHost string
	window   time.Duration
	log      *slog.Logger

	mu      sync.Mutex
	pending map[string]map[string]bool
}

var _ app.Publisher = (*Server)(nil)

func New(cfg Config) (*Server, error) {
	s := &Server{
		actor:   cfg.Actor,
		window:  cmp.Or(cfg.Window, 100*time.Millisecond),
		log:     cfg.Log,
		pending: map[string]map[string]bool{},
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
	if err := node.Run(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) Handler() http.Handler {
	ws := centrifuge.NewWebsocketHandler(s.node, centrifuge.WebsocketConfig{CheckOrigin: s.originAllowed})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { ws.ServeHTTP(cookieCarrier{w}, r) })
}

type cookieCarrier struct{ http.ResponseWriter }

func (c cookieCarrier) Unwrap() http.ResponseWriter { return c.ResponseWriter }

func (c cookieCarrier) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	nc, brw, err := h.Hijack()
	cookies := c.Header().Values("Set-Cookie")
	if err != nil || len(cookies) == 0 {
		return nc, brw, err
	}
	var extra bytes.Buffer
	_ = http.Header{"Set-Cookie": cookies}.Write(&extra)
	return &handshakeConn{Conn: nc, extra: extra.Bytes()}, brw, nil
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
	if _, err := c.Conn.Write(slices.Concat(p[:end+2], c.extra, p[end+2:])); err != nil {
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
	return strings.EqualFold(u.Host, r.Host) || strings.EqualFold(u.Host, s.siteHost)
}

func (s *Server) onConnecting(ctx context.Context, _ centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
	actor := s.actor(ctx)
	if !actor.SignedIn() {
		return centrifuge.ConnectReply{}, DisconnectSignedOut
	}
	reply := centrifuge.ConnectReply{
		Credentials: &centrifuge.Credentials{UserID: actor.UserID},
		Labels:      map[string]string{"session": actor.SessionID},
	}
	if actor.OrganizationID != "" {
		reply.Subscriptions = map[string]centrifuge.SubscribeOptions{Channel(actor.OrganizationID): {}}
	}
	return reply, nil
}

func (s *Server) Publish(organizationID string, topics []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, ok := s.pending[organizationID]
	if !ok {
		batch = map[string]bool{}
		s.pending[organizationID] = batch
		time.AfterFunc(s.window, func() { s.flush(organizationID) })
	}
	for _, t := range topics {
		batch[t] = true
	}
}

func (s *Server) flush(organizationID string) {
	s.mu.Lock()
	batch := s.pending[organizationID]
	delete(s.pending, organizationID)
	s.mu.Unlock()
	data, _ := json.Marshal(Invalidation{Type: "invalidate", Topics: slices.Sorted(maps.Keys(batch))})
	if _, err := s.node.Publish(Channel(organizationID), data); err != nil {
		s.log.Error("realtime: publish", "org", organizationID, "err", err)
	}
}

func (s *Server) DisconnectSession(sessionID string) {
	err := s.node.Disconnect("",
		centrifuge.WithDisconnectAllUsers(true),
		centrifuge.WithDisconnectLabelFilter(&centrifuge.FilterNode{Key: "session", Cmp: "eq", Val: sessionID}),
		centrifuge.WithCustomDisconnect(DisconnectSignedOut))
	if err != nil {
		s.log.Error("realtime: disconnect session", "err", err)
	}
}

func (s *Server) DisconnectUser(userID string) {
	err := s.node.Disconnect(userID, centrifuge.WithCustomDisconnect(centrifuge.Disconnect{Code: 4001, Reason: "membership changed"}))
	if err != nil {
		s.log.Error("realtime: disconnect user", "err", err)
	}
}

func (s *Server) Shutdown(ctx context.Context) error { return s.node.Shutdown(ctx) }

func (s *Server) logEntry(e centrifuge.LogEntry) {
	level := slog.LevelWarn
	if e.Level == centrifuge.LogLevelError {
		level = slog.LevelError
	}
	args := make([]any, 0, 2*len(e.Fields))
	for _, k := range slices.Sorted(maps.Keys(e.Fields)) {
		args = append(args, k, e.Fields[k])
	}
	s.log.Log(context.Background(), level, "realtime: "+e.Message, args...)
}
