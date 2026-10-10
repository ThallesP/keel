package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

type ProjectSummary struct {
	Project      domain.Project
	Environments []domain.Environment
}

type ProjectHome struct {
	Project     domain.Project
	Environment domain.Environment
}

var canvasDefaultProject = struct{ Name, Slug string }{"acme-support", "acme-support"}

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

func (a *App) EnsureDefaultProject(ctx context.Context, actor domain.Actor) (string, error) {
	if err := actor.RequireMember(); err != nil {
		return "", err
	}
	var slug string
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		own, err := tx.CanvasProjects(actor.OrganizationID)
		if err != nil {
			return err
		}
		if len(own) > 0 {
			slug = own[0].Slug
			return nil
		}
		if _, err := canvasInsertProject(tx, actor.OrganizationID, canvasDefaultProject.Name, canvasDefaultProject.Slug, a.Now()); err != nil {
			return err
		}
		ch.Projects(actor.OrganizationID)
		slug = canvasDefaultProject.Slug
		return nil
	})
	return slug, err
}

func (a *App) CreateProject(ctx context.Context, actor domain.Actor, name string) (ProjectSummary, error) {
	if err := actor.RequireMember(); err != nil {
		return ProjectSummary{}, err
	}
	var out ProjectSummary
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		name := domain.TrimJS(name)
		if n := domain.UTF16Len(name); n == 0 || n > 60 {
			return domain.Invalid("Project name: 1–60 characters")
		}
		slug := domain.Slug(name)
		if slug == "" {
			return domain.Invalid("Project name needs a letter or digit (a-z, 0-9)")
		}
		if _, err := tx.CanvasProjectBySlug(actor.OrganizationID, slug); err == nil {
			return canvasProjectExists(slug)
		} else if !errors.Is(err, ErrNoRow) {
			return err
		}
		created, err := canvasInsertProject(tx, actor.OrganizationID, name, slug, a.Now())
		if err != nil {
			return err
		}
		out = created
		ch.Projects(actor.OrganizationID)
		return nil
	})
	return out, err
}

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
		out = &ProjectHome{Project: p, Environment: canvasProductionFirst(envs)[0]}
		return nil
	})
	return out, err
}

func (a *App) ListProjects(ctx context.Context, actor domain.Actor) ([]ProjectSummary, error) {
	if err := actor.RequireMember(); err != nil {
		return nil, err
	}
	out := []ProjectSummary{}
	err := a.read(ctx, func(tx Tx) error {
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
