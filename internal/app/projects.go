package app

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

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

func canvasInsertProject(tx Tx, org, name, slug string, now int64) (ProjectSummary, error) {
	p := domain.Project{ID: domain.NewID(), OrganizationID: org, Name: name, Slug: slug, CreatedAt: now}
	err := tx.CanvasInsertProject(p)
	if errors.Is(err, ErrCanvasTaken) {
		return ProjectSummary{}, domain.E(domain.CodeNameTaken, "Project %q already exists", slug)
	}
	if err != nil {
		return ProjectSummary{}, err
	}
	env := domain.Environment{ID: domain.NewID(), ProjectID: p.ID, Name: "production", IsProduction: true, CreatedAt: now}
	if err := tx.CanvasInsertEnvironment(env); err != nil {
		return ProjectSummary{}, err
	}
	return ProjectSummary{Project: p, Environments: []domain.Environment{env}}, nil
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
		created, err := canvasInsertProject(tx, actor.OrganizationID, "acme-support", "acme-support", a.Now())
		if err != nil {
			return err
		}
		ch.Projects(actor.OrganizationID)
		slug = created.Project.Slug
		return nil
	})
	return slug, err
}

func (a *App) CreateProject(ctx context.Context, actor domain.Actor, name string) (ProjectSummary, error) {
	if err := actor.RequireMember(); err != nil {
		return ProjectSummary{}, err
	}
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n == 0 || n > 60 {
		return ProjectSummary{}, domain.Invalid("Project name: 1–60 characters")
	}
	slug := domain.Slug(name)
	if slug == "" {
		return ProjectSummary{}, domain.Invalid("Project name needs a letter or digit (a-z, 0-9)")
	}
	var out ProjectSummary
	err := a.write(ctx, func(tx Tx, ch *Changes) (err error) {
		out, err = canvasInsertProject(tx, actor.OrganizationID, name, slug, a.Now())
		if err != nil {
			return err
		}
		ch.Projects(actor.OrganizationID)
		return nil
	})
	return out, err
}

func (a *App) ProjectBySlug(ctx context.Context, actor domain.Actor, slug string) (*ProjectHome, error) {
	var out *ProjectHome
	err := a.read(ctx, func(tx Tx) error {
		p, err := tx.CanvasProjectBySlug(actor.OrganizationID, slug)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		envs, err := tx.CanvasEnvironments(p.ID)
		if err != nil {
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
	var out []ProjectSummary
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
