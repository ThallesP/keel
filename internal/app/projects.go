package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// Projects (convex/projects.ts, docs/go/spec/projects.md §9.1). A project always has its
// production environment; there is no API to rename or delete either.

// ProjectSummary is a project with its environments, production first.
type ProjectSummary struct {
	Project      domain.Project
	Environments []domain.Environment
}

// ProjectHome is a project with the environment its canvas opens on (production, else the first).
type ProjectHome struct {
	Project     domain.Project
	Environment domain.Environment
}

// canvasDefaultProject is what a fresh install's first visit gets.
var canvasDefaultProject = struct{ Name, Slug string }{"acme-support", "acme-support"}

// canvasMembership is joinOrFound plus whether it founded the organization just now (the caller's
// session then gains an organization: /api/me and the organization views change).
func canvasMembership(tx Tx, ch *Changes, actor domain.Actor, now int64) (domain.Actor, error) {
	if err := actor.RequireUser(); err != nil {
		return actor, err
	}
	m, err := canvasJoin(tx, actor, now)
	if err != nil {
		return m, err
	}
	if m.OrganizationID == "" {
		return m, errors.New("joinOrFound returned no organization")
	}
	if actor.OrganizationID == "" {
		ch.Organization(m.OrganizationID)
		ch.Add(m.OrganizationID, "/api/me")
	}
	return m, nil
}

// canvasInsertProject inserts the project and its production environment.
func canvasInsertProject(tx Tx, org, name, slug string, now int64) (ProjectSummary, error) {
	p := domain.Project{ID: domain.NewID(), OrganizationID: org, Name: name, Slug: slug, CreatedAt: now}
	if err := tx.CanvasInsertProject(p); err != nil {
		if errors.Is(err, ErrCanvasTaken) {
			return ProjectSummary{}, canvasProjectExists(slug)
		}
		return ProjectSummary{}, err
	}
	env := domain.Environment{ID: domain.NewID(), ProjectID: p.ID, Name: "production", IsProduction: true, CreatedAt: now}
	if err := tx.CanvasInsertEnvironment(env); err != nil {
		return ProjectSummary{}, err
	}
	return ProjectSummary{Project: p, Environments: []domain.Environment{env}}, nil
}

func canvasProjectExists(slug string) error {
	return domain.E(domain.CodeNameTaken, "Project \"%s\" already exists", slug)
}

// EnsureDefaultProject is the first-use bootstrap of the dashboard's home route: the install's
// organization (founded by the first account), then the organization's first project, or a new
// `acme-support` one. Returns the slug to open.
//
// Convex also adopted projects from before organizations existed (no organizationId). The SQLite
// schema makes organization_id NOT NULL, so such rows cannot exist here: the importer assigns them.
func (a *App) EnsureDefaultProject(ctx context.Context, actor domain.Actor) (string, error) {
	var slug string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		m, err := canvasMembership(tx, ch, actor, a.Now())
		if err != nil {
			return err
		}
		own, err := tx.CanvasProjects(m.OrganizationID)
		if err != nil {
			return err
		}
		if len(own) > 0 {
			slug = own[0].Slug
			return nil
		}
		if _, err := canvasInsertProject(tx, m.OrganizationID, canvasDefaultProject.Name, canvasDefaultProject.Slug, a.Now()); err != nil {
			return err
		}
		ch.Projects(m.OrganizationID)
		slug = canvasDefaultProject.Slug
		return nil
	})
	return slug, err
}

// CreateProject creates a project and its production environment, the slug derived from the name.
// A taken slug is an error, not a suffix: whoever asked (often an agent) is told. Founds the
// organization like EnsureDefaultProject, so `keel project create` works on a fresh install.
func (a *App) CreateProject(ctx context.Context, actor domain.Actor, name string) (ProjectSummary, error) {
	var out ProjectSummary
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		m, err := canvasMembership(tx, ch, actor, a.Now())
		if err != nil {
			return err
		}
		name := domain.TrimJS(name)
		if n := domain.UTF16Len(name); n == 0 || n > 60 {
			return domain.Invalid("Project name: 1–60 characters")
		}
		slug := domain.Slug(name)
		if slug == "" {
			return domain.Invalid("Project name needs a letter or digit (a-z, 0-9)")
		}
		if _, err := tx.CanvasProjectBySlug(m.OrganizationID, slug); err == nil {
			return canvasProjectExists(slug)
		} else if !errors.Is(err, ErrNoRow) {
			return err
		}
		out, err = canvasInsertProject(tx, m.OrganizationID, name, slug, a.Now())
		if err != nil {
			return err
		}
		ch.Projects(m.OrganizationID)
		return nil
	})
	return out, err
}

// ProjectBySlug is the project route's lookup: nil when signed out, without a membership, unknown
// slug, or no environment.
func (a *App) ProjectBySlug(ctx context.Context, actor domain.Actor, slug string) (*ProjectHome, error) {
	var out *ProjectHome
	err := a.read(ctx, func(tx Tx) error {
		if actor.OrganizationID == "" {
			return nil
		}
		p, err := tx.CanvasProjectBySlug(actor.OrganizationID, slug)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		envs, err := tx.CanvasEnvironments(p.ID)
		if err != nil || len(envs) == 0 {
			return err
		}
		env := envs[0]
		for _, e := range envs {
			if e.IsProduction {
				env = e
				break
			}
		}
		out = &ProjectHome{Project: p, Environment: env}
		return nil
	})
	return out, err
}

// ListProjects: every project of the actor's organization in creation order, environments
// production first. Without a membership it fails instead of returning nothing, so the CLI can tell
// "no projects" from "not in an organization": signed out → NOT_AUTHENTICATED, an organization
// exists → NO_ORGANIZATION. Before any organization exists there is nothing to be left out of: [].
func (a *App) ListProjects(ctx context.Context, actor domain.Actor) ([]ProjectSummary, error) {
	out := []ProjectSummary{}
	err := a.read(ctx, func(tx Tx) error {
		if actor.OrganizationID == "" {
			if err := actor.RequireUser(); err != nil {
				return err
			}
			exists, err := tx.CanvasOrganizationExists()
			if err != nil {
				return err
			}
			if exists {
				return domain.ErrNoOrganization
			}
			return nil
		}
		projects, err := tx.CanvasProjects(actor.OrganizationID)
		if err != nil {
			return err
		}
		for _, p := range projects {
			envs, err := tx.CanvasEnvironments(p.ID)
			if err != nil {
				return err
			}
			out = append(out, ProjectSummary{Project: p, Environments: canvasProductionFirst(envs)})
		}
		return nil
	})
	return out, err
}

// canvasProductionFirst is a stable sort putting production environments first.
func canvasProductionFirst(envs []domain.Environment) []domain.Environment {
	out := make([]domain.Environment, 0, len(envs))
	for _, e := range envs {
		if e.IsProduction {
			out = append(out, e)
		}
	}
	for _, e := range envs {
		if !e.IsProduction {
			out = append(out, e)
		}
	}
	return out
}
