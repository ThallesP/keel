package http

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

type Options struct {
	Web fs.FS
	WS  http.Handler
}

type Server struct {
	app  *app.App
	opts Options
}

const SessionCookie = "keel_session"

func init() {
	huma.DefaultArrayNullable = false
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		p := &api.Problem{Status: status, Title: http.StatusText(status), Detail: msg, Code: codeForStatus(status)}
		for _, e := range errs {
			if d, ok := errors.AsType[*huma.ErrorDetail](e); ok {
				p.Errors = append(p.Errors, api.ErrorDetail{Message: d.Message, Location: d.Location, Value: d.Value})
			} else if e != nil && p.Detail == "" {
				p.Detail = e.Error()
			}
		}
		return p
	}
	huma.NewErrorWithContext = func(_ huma.Context, status int, msg string, errs ...error) huma.StatusError {
		return huma.NewError(status, msg, errs...)
	}
}

func Config(version string) huma.Config {
	c := huma.DefaultConfig("Keel", version)
	c.Info.Description = "Keel control plane API. Errors are application/problem+json with a stable `code`."
	c.OpenAPIPath = "/api/openapi"
	c.DocsPath = "/api/docs"
	c.SchemasPath = "/api/schemas"
	c.CreateHooks = nil
	c.Formats = map[string]huma.Format{"application/json": jsonFormat, "json": jsonFormat}
	c.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"session": {Type: "apiKey", In: "cookie", Name: SessionCookie},
		"bearer":  {Type: "http", Scheme: "bearer"},
	}
	c.Security = []map[string][]string{{"session": {}}, {"bearer": {}}}
	return c
}

func New(a *app.App, opts Options) http.Handler {
	s := &Server{app: a, opts: opts}
	mux := http.NewServeMux()
	s.Register(humago.New(mux, Config(a.Config.Version)))
	s.registerRaw(mux)
	return s.withActor(withInvalidations(mux))
}

func (s *Server) Register(h huma.API) {
	s.registerMeta(h)
	s.registerAuth(h)
	s.registerCanvas(h)
	s.registerDeploy(h)
	s.registerObservability(h)
	s.registerIngress(h)
}

func OpenAPI(version string) *huma.OpenAPI {
	mux := http.NewServeMux()
	h := humago.New(mux, Config(version))
	(&Server{app: &app.App{Config: app.Config{Version: version}}}).Register(h)
	return h.OpenAPI()
}

func (s *Server) registerRaw(mux *http.ServeMux) {
	s.registerDeployRaw(mux)
	s.registerObservabilityRaw(mux)
	s.registerIngressRaw(mux)
	if s.opts.WS != nil {
		mux.Handle("GET /api/ws", s.opts.WS)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, &api.Problem{Status: 404, Title: "Not Found", Detail: "No such API route", Code: domain.CodeNotFound})
	})
	if s.opts.Web != nil {
		mux.Handle("/", spa(s.opts.Web))
	}
}

type actorKey struct{}

func (s *Server) withActor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if machineRoute(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		token := SessionToken(r)
		bearer := authViaBearer(r)
		viaCookie := !bearer && token != ""
		browserWrite := !authSafeMethod(r.Method) && !bearer &&
			(viaCookie || r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "")
		if browserWrite {
			if msg := s.authCSRFRefusal(r); msg != "" {
				writeProblem(w, &api.Problem{Status: http.StatusForbidden, Title: http.StatusText(http.StatusForbidden), Detail: msg, Code: domain.CodeForbidden})
				return
			}
		}
		actor, err := s.app.ResolveSession(r.Context(), token)
		if err != nil {
			s.app.Log.Error("resolve session", "err", err)
			writeProblem(w, &api.Problem{Status: http.StatusServiceUnavailable, Title: http.StatusText(http.StatusServiceUnavailable),
				Detail: "Could not check the session; try again", Code: domain.CodeUnavailable})
			return
		}
		if viaCookie && actor.SessionRenewed {
			http.SetCookie(w, new(s.authSessionCookie(token, actor.SessionExpiresAt)))
		}
		ctx := authWithClient(context.WithValue(r.Context(), actorKey{}, actor), r)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func SessionToken(r *http.Request) string {
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		return strings.TrimSpace(token)
	}
	if c, err := r.Cookie(SessionCookie); err == nil && c.Value != "" {
		return c.Value
	}
	return ""
}

func ActorFrom(ctx context.Context) domain.Actor {
	a, _ := ctx.Value(actorKey{}).(domain.Actor)
	return a
}

func op[I, O any](h huma.API, o huma.Operation, handler func(ctx context.Context, in *I) (*O, error)) {
	huma.Register(h, o, func(ctx context.Context, in *I) (*O, error) {
		out, err := handler(ctx, in)
		if err != nil {
			return nil, problemOf(err)
		}
		return out, nil
	})
}

func problemOf(err error) error {
	if p, ok := errors.AsType[*api.Problem](err); ok {
		return p
	}
	if r, ok := errors.AsType[*domain.DeviceRefusal](err); ok {
		return &authDeviceError{status: r.Status, body: api.DeviceError{Error: r.Code, ErrorDescription: r.Description}}
	}
	if limited, ok := errors.AsType[*domain.RateLimitError](err); ok {
		p := &api.Problem{Status: http.StatusTooManyRequests, Title: http.StatusText(http.StatusTooManyRequests),
			Detail: domain.MsgTooManyRequests, Code: domain.CodeRateLimited}
		return huma.ErrorWithHeaders(p, http.Header{"Retry-After": {strconv.FormatInt(limited.RetryAfterSeconds, 10)}})
	}
	if de, ok := errors.AsType[*domain.Error](err); ok {
		status := StatusOf(de.Code)
		return &api.Problem{Status: status, Title: http.StatusText(status), Detail: de.Message, Code: de.Code}
	}
	if errors.Is(err, context.Canceled) {
		return &api.Problem{Status: 499, Title: "Client Closed Request", Detail: "Cancelled", Code: domain.CodeServerError}
	}
	slog.Error("unexpected error", "err", err)
	return &api.Problem{Status: 500, Title: "Internal Server Error", Detail: "Something went wrong on the server", Code: domain.CodeServerError}
}

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

func writeProblem(w http.ResponseWriter, p *api.Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = jsonEncode(w, p)
}

func machineRoute(path string) bool {
	return slices.ContainsFunc([]string{"/worker/", "/proxy/", "/otlp/"}, func(p string) bool { return strings.HasPrefix(path, p) })
}
