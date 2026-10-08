// Package http is Keel's HTTP transport: the Huma API under /api, the WebSocket, the raw routes
// agents and services call, and the dashboard. Handlers translate wire types to use-case calls and
// back; all logic is in app. See docs/go/ARCHITECTURE.md, "HTTP API".
package http

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

// Options configure the server beyond the app.
type Options struct {
	// Web is the built dashboard (apps/web/dist), served with an SPA fallback. nil in dev: Vite
	// serves it and proxies the API here.
	Web fs.FS
	// WS is the WebSocket handler mounted at /api/ws (adapters/realtime). nil disables it.
	WS http.Handler
	// ConfigJS is the body of /config.js.
	ConfigJS string
}

// Server holds what handlers need.
type Server struct {
	app  *app.App
	opts Options
	log  *slog.Logger
}

// SessionCookie is the dashboard's session cookie.
const SessionCookie = "keel_session"

func init() {
	// Every error the API writes is an api.Problem, so the OpenAPI document (and the dashboard's
	// generated types) describe exactly that shape.
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		p := &api.Problem{Status: status, Title: http.StatusText(status), Detail: msg, Code: codeForStatus(status)}
		for _, e := range errs {
			var d *huma.ErrorDetail
			if errors.As(e, &d) {
				p.Errors = append(p.Errors, api.ErrorDetail{Message: d.Message, Location: d.Location, Value: d.Value})
			} else if e != nil && p.Detail == "" {
				p.Detail = e.Error()
			}
		}
		if status == http.StatusUnprocessableEntity || status == http.StatusBadRequest {
			p.Code = domain.CodeInvalidInput
		}
		return p
	}
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return huma.NewError(status, msg, errs...)
	}
}

// Config is the Huma configuration, shared by the server and `keel openapi`.
func Config(version string) huma.Config {
	c := huma.DefaultConfig("Keel", version)
	c.Info.Description = "Keel control plane API. Errors are application/problem+json with a stable `code`."
	c.OpenAPIPath = "/api/openapi"
	c.DocsPath = "/api/docs"
	c.SchemasPath = "/api/schemas"
	// No `$schema` link in response bodies: it would leak into the dashboard's generated types.
	c.CreateHooks = nil
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"session": {Type: "apiKey", In: "cookie", Name: SessionCookie},
		"bearer":  {Type: "http", Scheme: "bearer"},
	}
	c.Security = []map[string][]string{{"session": {}}, {"bearer": {}}}
	return c
}

// New builds the whole HTTP handler.
func New(a *app.App, opts Options) http.Handler {
	s := &Server{app: a, opts: opts, log: a.Log}
	mux := http.NewServeMux()
	humaAPI := humago.New(mux, Config(a.Config.Version))
	s.Register(humaAPI)
	s.registerRaw(mux)
	return s.withActor(withInvalidations(mux))
}

// Register adds every Huma operation. `keel openapi` calls it with a nil app to print the spec.
func (s *Server) Register(h huma.API) {
	s.registerMeta(h)
	s.registerAuth(h)
	s.registerCanvas(h)
	s.registerDeploy(h)
	s.registerObservability(h)
	s.registerIngress(h)
}

// OpenAPI is the API document without a running server.
func OpenAPI(version string) *huma.OpenAPI {
	mux := http.NewServeMux()
	h := humago.New(mux, Config(version))
	(&Server{app: &app.App{Config: app.Config{Version: version}}}).Register(h)
	return h.OpenAPI()
}

// registerRaw adds the non-Huma routes: agent and proxy callbacks, OTLP, WebSocket, config.js,
// and the dashboard.
func (s *Server) registerRaw(mux *http.ServeMux) {
	s.registerDeployRaw(mux)
	s.registerObservabilityRaw(mux)
	s.registerIngressRaw(mux)
	if s.opts.WS != nil {
		mux.Handle("GET /api/ws", s.opts.WS)
	}
	mux.HandleFunc("GET /config.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(s.opts.ConfigJS))
	})
	// Plain-text version, as Convex's /version answered (cli-install.md B3): older install
	// verification steps still curl "$convexUrl/version", and convexUrl is now the dashboard URL.
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(s.app.Config.Version + "\n"))
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, &api.Problem{Status: 404, Title: "Not Found", Detail: "No such API route", Code: domain.CodeNotFound})
	})
	if s.opts.Web != nil {
		mux.Handle("/", spa(s.opts.Web))
	}
}

// ── Actor ──────────────────────────────────────────────────────────────────────────────────

type actorKey struct{}

// withActor resolves the session (cookie, else bearer) once per request. Cookie-authenticated
// unsafe requests must come from this site (Origin, else Referer: CSRF, auth_session.go); bearer
// requests are exempt. A session renewed on the way gets its cookie re-sent.
func (s *Server) withActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := SessionToken(r)
		viaCookie := authViaCookie(r)
		if viaCookie && !authSafeMethod(r.Method) {
			if msg := s.authCSRFRefusal(r); msg != "" {
				authForbidden(w, msg)
				return
			}
		}
		actor := domain.Actor{}
		if token != "" && s.app != nil && s.app.Store != nil {
			a, err := s.app.ResolveSession(r.Context(), token)
			if err != nil {
				s.log.Error("resolve session", "err", err)
			} else {
				actor = a
			}
		}
		if viaCookie && actor.SessionRenewed {
			c := s.authSessionCookie(token, actor.SessionExpiresAt)
			http.SetCookie(w, &c)
		}
		ctx := authWithClient(context.WithValue(r.Context(), actorKey{}, actor), r)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// SessionToken: `Authorization: Bearer <token>` when the request has one, else the keel_session
// cookie. The bearer wins so that the credential that authenticates a request is the one its
// CSRF exemption is based on (authViaCookie): a bearer request is never acted on with the
// browser's ambient cookie.
func SessionToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

// ActorFrom is the caller of the current request.
func ActorFrom(ctx context.Context) domain.Actor {
	a, _ := ctx.Value(actorKey{}).(domain.Actor)
	return a
}

// ── Operations and errors ──────────────────────────────────────────────────────────────────

// op registers a Huma operation whose handler returns domain errors; they become problems.
func op[I, O any](h huma.API, o huma.Operation, handler func(ctx context.Context, in *I) (*O, error)) {
	huma.Register(h, o, func(ctx context.Context, in *I) (*O, error) {
		out, err := handler(ctx, in)
		if err != nil {
			return nil, problemOf(err)
		}
		return out, nil
	})
}

// problemOf maps a domain error to its problem; anything else is a logged 500.
func problemOf(err error) error {
	var p *api.Problem
	if errors.As(err, &p) {
		return p
	}
	var de *domain.Error
	if errors.As(err, &de) {
		status := StatusOf(de.Code)
		return &api.Problem{Status: status, Title: http.StatusText(status), Detail: de.Message, Code: de.Code}
	}
	if errors.Is(err, context.Canceled) {
		return &api.Problem{Status: 499, Title: "Client Closed Request", Detail: "Cancelled", Code: domain.CodeServerError}
	}
	slog.Error("unexpected error", "err", err)
	return &api.Problem{Status: 500, Title: "Internal Server Error", Detail: "Something went wrong on the server", Code: domain.CodeServerError}
}

// StatusOf is the HTTP status of a domain error code (docs/go/ARCHITECTURE.md, "Errors").
func StatusOf(code string) int {
	switch code {
	case domain.CodeNotAuthenticated:
		return 401
	case domain.CodeNoOrganization, domain.CodeForbidden:
		return 403
	case domain.CodeNotFound, domain.CodeProjectNotFound, domain.CodeServiceNotFound,
		domain.CodeVariableNotFound, domain.CodeDeploymentNotFound:
		return 404
	case domain.CodeNameTaken, domain.CodeNothingToShip, domain.CodeDeploymentRunning,
		domain.CodeConflict, domain.CodeTracesOff:
		return 409
	case domain.CodeAuthorizationPending:
		return 428
	case domain.CodeRateLimited:
		return 429
	case domain.CodeInvalidInput:
		return 422
	case domain.CodeUnavailable:
		return 503
	}
	return 500
}

func codeForStatus(status int) string {
	switch status {
	case 401:
		return domain.CodeNotAuthenticated
	case 403:
		return domain.CodeForbidden
	case 404:
		return domain.CodeNotFound
	case 409:
		return domain.CodeConflict
	case 400, 422:
		return domain.CodeInvalidInput
	case 429:
		return domain.CodeRateLimited
	case 503:
		return domain.CodeUnavailable
	}
	return domain.CodeServerError
}

// writeProblem writes a problem from a raw (non-Huma) handler.
func writeProblem(w http.ResponseWriter, p *api.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = jsonEncode(w, p)
}
