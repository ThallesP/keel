package cli

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
)

// session is a signed-in command's target: which install, as whom, and the API to it.
type session struct {
	cfg  *config.Config
	name string
	inst *config.Instance
	api  *client.Client
	// Who the session is, from GET /api/me when connecting.
	user *client.User
	org  *client.Organization
}

func (a *app) loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, output.Errorf(output.CodeConfig, "Fix or delete the file, then keel login again",
			"Can't read the config file: %v", err)
	}
	return cfg, nil
}

func saveConfig(cfg *config.Config) error {
	if err := cfg.Save(); err != nil {
		return output.Errorf(output.CodeConfig, "Check the permissions of "+cfg.Path(),
			"Can't write the config file: %v", err)
	}
	return nil
}

// target picks the install: KEEL_URL, else --instance / KEEL_INSTANCE, else the directory's link,
// else the current instance, else the only one. KEEL_TOKEN overrides the stored token.
// KEEL_CONVEX_URL and KEEL_CONVEX_SITE_URL, which went with KEEL_URL in the Convex era, are
// ignored: the dashboard URL is the API's.
func (a *app) target(cfg *config.Config) (string, *config.Instance, error) {
	token := os.Getenv("KEEL_TOKEN")
	if raw := os.Getenv("KEEL_URL"); raw != "" {
		webURL, err := normalizeURL(raw)
		if err != nil {
			return "", nil, output.Errorf(output.CodeUsage, "KEEL_URL=https://<dashboard-host>", "KEEL_URL: %v", err)
		}
		return hostOf(webURL), &config.Instance{URL: webURL, Token: token}, nil
	}

	name := a.instanceFlag
	if name == "" {
		name = os.Getenv("KEEL_INSTANCE")
	}
	if name == "" {
		if _, l := cfg.LinkFor(cwd()); l != nil {
			name = l.Instance
		}
	}
	if name == "" {
		name = cfg.Current
	}
	if name == "" && len(cfg.Instances) == 1 {
		for n := range cfg.Instances {
			name = n
		}
	}
	inst := cfg.Instances[name]
	if inst == nil {
		if name != "" && len(cfg.Instances) > 0 {
			return "", nil, output.Errorf(output.CodeNotAuthenticated, "keel login <dashboard-url> --name "+name,
				"Not logged in to an instance named %q (known: %s)", name, strings.Join(slices.Sorted(maps.Keys(cfg.Instances)), ", "))
		}
		return "", nil, output.Errorf(output.CodeNotAuthenticated,
			"keel login <dashboard-url>, or set KEEL_URL and KEEL_TOKEN", "Not logged in")
	}
	if token != "" {
		copy := *inst
		copy.Token = token
		inst = &copy
	}
	return name, inst, nil
}

// connect is a signed-in command's start: the install, a pending login finished, and the
// session checked (GET /api/me), so a dead one fails as NOT_AUTHENTICATED before anything else.
func (a *app) connect(ctx context.Context) (*session, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	name, inst, err := a.target(cfg)
	if err != nil {
		return nil, err
	}
	if err := a.finishLogin(ctx, cfg, name, inst); err != nil {
		return nil, err
	}
	return dial(ctx, cfg, name, inst)
}

// dial checks inst's session and returns the session to it.
func dial(ctx context.Context, cfg *config.Config, name string, inst *config.Instance) (*session, error) {
	c := client.New(inst.URL, inst.Token)
	user, org, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	return &session{cfg: cfg, name: name, inst: inst, api: c, user: user, org: org}, nil
}

// finishLogin completes the login keel login left pending, once someone approved it in the
// dashboard: the token is saved and the command runs as usual. Not approved yet is
// AUTHORIZATION_PENDING. An instance with a token (or KEEL_TOKEN) has nothing to finish.
func (a *app) finishLogin(ctx context.Context, cfg *config.Config, name string, inst *config.Instance) error {
	p := inst.Pending
	if inst.Token != "" || p == nil {
		return nil
	}
	if p.Expired() {
		return output.Errorf(output.CodeNotAuthenticated, "keel login "+inst.URL,
			"The login link expired before anyone approved it")
	}
	c := client.New(inst.URL, "")
	token, slowDown, err := c.PollLogin(ctx, p.DeviceCode)
	if slowDown {
		// An earlier run polled moments ago; wait out the interval for a real answer.
		select {
		case <-ctx.Done():
			return output.Errorf(output.CodeCancelled, "", "Cancelled")
		case <-time.After(time.Duration(p.Interval) * time.Second):
		}
		token, _, err = c.PollLogin(ctx, p.DeviceCode)
	}
	if err != nil {
		fresh, ferr := config.Load()
		if ferr != nil {
			return err
		}
		f := fresh.Instances[name]
		if f == nil || f.URL != inst.URL {
			return err
		}
		// A keel run alongside this one may have taken the token: it is handed out only once.
		if f.Token != "" {
			*inst = *f
			return nil
		}
		// Turned down, expired or used up: the install has dropped the code, so forget it and
		// later runs say "Not logged in" instead of polling a code that is gone.
		if output.CodeOf(err) == output.CodeNotAuthenticated && f.Pending != nil && f.Pending.DeviceCode == p.DeviceCode {
			f.Pending = nil
			fresh.Save()
		}
		return err
	}
	if token == "" {
		return output.Errorf(output.CodeAuthorizationPending,
			"Open "+p.URL+" and approve (agents: send it to your human), then retry; or keel login --wait",
			"Waiting for someone to approve the login")
	}
	inst.Token, inst.Pending = token, nil
	if err := saveConfig(cfg); err != nil {
		return err
	}
	a.out.Progress("Login approved; saved for %s", name)
	return nil
}

// projectSlug is the project asked for: --project, KEEL_PROJECT, then the directory's link; ""
// when nothing picks one.
func (a *app) projectSlug(s *session) string {
	if a.projectFlag != "" {
		return a.projectFlag
	}
	if p := os.Getenv("KEEL_PROJECT"); p != "" {
		return p
	}
	if _, l := s.cfg.LinkFor(cwd()); l != nil && l.Instance == s.name {
		return l.Project
	}
	return ""
}

// project resolves the target project and its environment (production for now; environments
// beyond production arrive with a flag).
func (a *app) project(ctx context.Context, s *session) (*client.Project, *client.Environment, error) {
	projects, err := s.api.Projects(ctx)
	if err != nil {
		return nil, nil, err
	}
	p, err := pickProject(projects, a.projectSlug(s))
	if err != nil {
		return nil, nil, err
	}
	if len(p.Environments) == 0 {
		return nil, nil, output.Errorf(output.CodeProjectNotFound, "Open "+s.inst.URL+" and check the project",
			"Project %s has no environment", p.Slug)
	}
	return p, &p.Environments[0], nil
}

func pickProject(projects []client.Project, slug string) (*client.Project, error) {
	if len(projects) == 0 {
		return nil, output.Errorf(output.CodeNoProjects, "keel project create <name> --link", "No projects yet")
	}
	slugs := make([]string, len(projects))
	for i := range projects {
		if projects[i].Slug == slug || projects[i].ID == slug {
			return &projects[i], nil
		}
		slugs[i] = projects[i].Slug
	}
	list := strings.Join(slugs, ", ")
	if slug == "" {
		if len(projects) == 1 {
			return &projects[0], nil
		}
		return nil, output.Errorf(output.CodeProjectRequired,
			fmt.Sprintf("Pass --project <slug> or run keel link <slug> (projects: %s)", list),
			"%d projects and none picked", len(projects))
	}
	return nil, output.Errorf(output.CodeProjectNotFound, "Projects: "+list, "No project %q", slug)
}

// service finds a service of the environment by name (or id) and returns the full list with it.
func (s *session) service(ctx context.Context, environmentID, name string) (*client.Service, []client.Service, error) {
	services, err := s.api.Services(ctx, environmentID)
	if err != nil {
		return nil, nil, err
	}
	found, err := findService(services, name)
	return found, services, err
}

func findService(services []client.Service, name string) (*client.Service, error) {
	names := make([]string, len(services))
	for i := range services {
		if services[i].Name == name || services[i].ID == name {
			return &services[i], nil
		}
		names[i] = services[i].Name
	}
	fix := "keel service list"
	if len(names) > 0 {
		fix = "Services: " + strings.Join(names, ", ")
	}
	return nil, output.Errorf(output.CodeServiceNotFound, fix, "No service %q", name)
}

// normalizeURL keeps scheme and host: the dashboard origin, which is also the API's.
func normalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not a dashboard URL (http:// or https:// and a host)", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

func hostOf(webURL string) string {
	u, _ := url.Parse(webURL)
	return u.Host
}

func cwd() string {
	dir, _ := os.Getwd()
	return dir
}
