package cli

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
)

type session struct {
	cfg  *config.Config
	name string
	inst *config.Instance
	api  *client.Client
	user *api.User
	org  *api.Organization
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, output.Errorf(output.CodeConfig, "Fix or delete the file, then keel login again",
			"Can't read the config file: %v", err)
	}
	return cfg, nil
}

func saveConfig(cfg *config.Config) error {
	if err := cfg.Save(); err != nil {
		return output.Errorf(output.CodeConfig, "Check the permissions of "+cfg.Path,
			"Can't write the config file: %v", err)
	}
	return nil
}

func (a *app) target(cfg *config.Config) (string, *config.Instance, error) {
	token := os.Getenv("KEEL_TOKEN")
	if raw := os.Getenv("KEEL_URL"); raw != "" {
		webURL, err := normalizeURL(raw)
		if err != nil {
			return "", nil, output.Errorf(output.CodeUsage, "KEEL_URL=https://<dashboard-host>", "KEEL_URL: %v", err)
		}
		return hostOf(webURL), &config.Instance{URL: webURL, Token: token}, nil
	}

	var linked, only string
	if _, l := cfg.LinkFor(cwd()); l != nil {
		linked = l.Instance
	}
	if len(cfg.Instances) == 1 {
		only = slices.Collect(maps.Keys(cfg.Instances))[0]
	}
	name := cmp.Or(a.instanceFlag, os.Getenv("KEEL_INSTANCE"), linked, cfg.Current, only)
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
		withToken := *inst
		withToken.Token = token
		inst = &withToken
	}
	return name, inst, nil
}

func (a *app) connect(ctx context.Context) (*session, error) {
	cfg, err := loadConfig()
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

func dial(ctx context.Context, cfg *config.Config, name string, inst *config.Instance) (*session, error) {
	c := client.New(inst.URL, inst.Token)
	user, org, err := c.Me(ctx)
	if err != nil {
		return nil, err
	}
	return &session{cfg: cfg, name: name, inst: inst, api: c, user: user, org: org}, nil
}

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
		if f.Token != "" {
			*inst = *f
			return nil
		}
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

func (a *app) projectSlug(s *session) string {
	var linked string
	if _, l := s.cfg.LinkFor(cwd()); l != nil && l.Instance == s.name {
		linked = l.Project
	}
	return cmp.Or(a.projectFlag, os.Getenv("KEEL_PROJECT"), linked)
}

func (a *app) project(ctx context.Context, s *session) (*api.ProjectSummary, *api.ProjectEnvironment, error) {
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

func pickProject(projects []api.ProjectSummary, slug string) (*api.ProjectSummary, error) {
	if len(projects) == 0 {
		return nil, output.Errorf(output.CodeNoProjects, "keel project create <name> --link", "No projects yet")
	}
	if slug == "" && len(projects) == 1 {
		return &projects[0], nil
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
		return nil, output.Errorf(output.CodeProjectRequired, "Pass --project <slug> or run keel link <slug> (projects: "+list+")",
			"%d projects and none picked", len(projects))
	}
	return nil, output.Errorf(output.CodeProjectNotFound, "Projects: "+list, "No project %q", slug)
}

func (a *app) connectProject(ctx context.Context) (*session, *api.ProjectSummary, *api.ProjectEnvironment, error) {
	s, err := a.connect(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	p, env, err := a.project(ctx, s)
	return s, p, env, err
}

func (a *app) connectService(ctx context.Context, name string) (*session, *client.Service, error) {
	s, _, env, err := a.connectProject(ctx)
	if err != nil {
		return nil, nil, err
	}
	svc, err := s.service(ctx, env.ID, name)
	if err != nil {
		return nil, nil, err
	}
	return s, svc, nil
}

func (s *session) service(ctx context.Context, environmentID, name string) (*client.Service, error) {
	services, err := s.api.Services(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	return findService(services, name)
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
