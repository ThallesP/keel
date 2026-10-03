package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/apps/cli/internal/config"
	"github.com/ThallesP/keel/apps/cli/internal/keel"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

// session is a signed-in command's target: which install, as whom, and the API to it.
type session struct {
	cfg  *config.Config
	name string
	inst *config.Instance
	api  *keel.API
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
func (a *app) target(ctx context.Context, cfg *config.Config) (string, *config.Instance, error) {
	token := os.Getenv("KEEL_TOKEN")
	if raw := os.Getenv("KEEL_URL"); raw != "" {
		webURL, err := normalizeURL(raw)
		if err != nil {
			return "", nil, output.Errorf(output.CodeUsage, "KEEL_URL=https://<dashboard-host>", "KEEL_URL: %v", err)
		}
		inst := &config.Instance{
			URL:           webURL,
			ConvexURL:     os.Getenv("KEEL_CONVEX_URL"),
			ConvexSiteURL: os.Getenv("KEEL_CONVEX_SITE_URL"),
			Token:         token,
		}
		for _, known := range cfg.Instances {
			if known.URL == webURL && inst.ConvexURL == "" {
				inst.ConvexURL, inst.ConvexSiteURL = known.ConvexURL, known.ConvexSiteURL
			}
		}
		if inst.ConvexURL == "" || inst.ConvexSiteURL == "" {
			if inst.ConvexURL, inst.ConvexSiteURL, err = keel.Discover(ctx, webURL); err != nil {
				return "", nil, err
			}
		}
		return hostOf(webURL), inst, nil
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
				"Not logged in to an instance named %q (known: %s)", name, strings.Join(instanceNames(cfg), ", "))
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

func (a *app) connect(ctx context.Context) (*session, error) {
	cfg, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	name, inst, err := a.target(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := a.finishLogin(ctx, cfg, name, inst); err != nil {
		return nil, err
	}
	api, err := keel.Connect(ctx, inst)
	if err != nil {
		return nil, err
	}
	return &session{cfg: cfg, name: name, inst: inst, api: api}, nil
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
	token, slowDown, err := keel.PollLogin(ctx, inst)
	if slowDown {
		// An earlier run polled moments ago; wait out the interval for a real answer.
		select {
		case <-ctx.Done():
			return output.Errorf(output.CodeCancelled, "", "Cancelled")
		case <-time.After(time.Duration(p.Interval) * time.Second):
		}
		token, _, err = keel.PollLogin(ctx, inst)
	}
	if err != nil {
		// A keel run alongside this one may have taken the token: it is handed out only once.
		if fresh, ferr := config.Load(); ferr == nil {
			if f := fresh.Instances[name]; f != nil && f.URL == inst.URL && f.Token != "" {
				*inst = *f
				return nil
			}
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
func (a *app) project(ctx context.Context, s *session) (*keel.Project, *keel.Environment, error) {
	projects, err := s.api.Projects(ctx)
	if err != nil {
		return nil, nil, err
	}
	p, err := pickProject(projects, a.projectSlug(s), s.inst.URL)
	if err != nil {
		return nil, nil, err
	}
	if len(p.Environments) == 0 {
		return nil, nil, output.Errorf(output.CodeProjectNotFound, "Open "+s.inst.URL+" and check the project",
			"Project %s has no environment", p.Slug)
	}
	return p, &p.Environments[0], nil
}

func pickProject(projects []keel.Project, slug, webURL string) (*keel.Project, error) {
	if len(projects) == 0 {
		return nil, output.Errorf(output.CodeNoProjects, "Open "+webURL+" to create one", "No projects yet")
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
func (s *session) service(ctx context.Context, environmentID, name string) (*keel.Service, []keel.Service, error) {
	services, err := s.api.Services(ctx, environmentID)
	if err != nil {
		return nil, nil, err
	}
	found, err := findService(services, name)
	return found, services, err
}

func findService(services []keel.Service, name string) (*keel.Service, error) {
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

// normalizeURL keeps scheme and host: the dashboard origin, which is what better-auth trusts.
func normalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not a dashboard URL (http:// or https:// and a host)", raw)
	}
	return u.Scheme + "://" + u.Host, nil
}

// errCode is the output code of err, "" when it has none.
func errCode(err error) string {
	var oe *output.Error
	if errors.As(err, &oe) {
		return oe.Code
	}
	return ""
}

func hostOf(webURL string) string {
	u, _ := url.Parse(webURL)
	return u.Host
}

func instanceNames(cfg *config.Config) []string {
	return slices.Sorted(maps.Keys(cfg.Instances))
}

func cwd() string {
	dir, _ := os.Getwd()
	return dir
}
