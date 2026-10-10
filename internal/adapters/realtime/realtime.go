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

	mu      sync.Mutex
	closed  bool
	pending map[string]map[string]bool
}

var _ app.Publisher = (*Server)(nil)

type connKey struct{}

type conn struct {
	actor domain.Actor
	err   error
}

func New(cfg Config) (*Server, error) {
	s := &Server{
		auth:    cfg.Authenticate,
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
	cookies := c.Header().Values("Set-Cookie")
	if err != nil || len(cookies) == 0 {
		return nc, brw, err
	}
	var extra []byte
	for _, v := range cookies {
		extra = append(extra, "Set-Cookie: "...)
		for _, b := range []byte(v) {
			if b < ' ' || b == 0x7f {
				b = ' '
			}
			extra = append(extra, b)
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
	c := ctx.Value(connKey{}).(conn)
	if c.err != nil {
		s.log.Error("realtime: resolve session", "err", c.err)
		return centrifuge.ConnectReply{}, centrifuge.DisconnectServerError
	}
	if !c.actor.SignedIn() {
		return centrifuge.ConnectReply{}, DisconnectSignedOut
	}
	reply := centrifuge.ConnectReply{
		Credentials: &centrifuge.Credentials{UserID: c.actor.UserID},
		Labels:      map[string]string{"session": c.actor.SessionID},
	}
	if c.actor.OrganizationID != "" {
		reply.Subscriptions = map[string]centrifuge.SubscribeOptions{Channel(c.actor.OrganizationID): {}}
	}
	return reply, nil
}

func (s *Server) Publish(organizationID string, topics []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
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
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
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

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return s.node.Shutdown(ctx)
}

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
