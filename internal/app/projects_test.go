package app_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestCanvasCreateProject(t *testing.T) {
	k := canvasSetup(t)
	a := canvasMember(canvasOrg)
	p, err := k.app.CreateProject(k.ctx, a, "  Ação API \n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Project.Name != "Ação API" || p.Project.Slug != "acao-api" || p.Project.OrganizationID != canvasOrg {
		t.Fatalf("project %+v", p.Project)
	}
	if len(p.Environments) != 1 || p.Environments[0].Name != "production" || !p.Environments[0].IsProduction {
		t.Fatalf("environments %+v", p.Environments)
	}
	if got := k.pub.take(canvasOrg); !reflect.DeepEqual(got, []string{"/api/projects"}) {
		t.Errorf("topics %v", got)
	}

	cases := []struct {
		name, code, msg string
	}{
		{"   ", domain.CodeInvalidInput, "Project name: 1–60 characters"},
		{strings.Repeat("a", 61), domain.CodeInvalidInput, "Project name: 1–60 characters"},
		{strings.Repeat("😀", 31), domain.CodeInvalidInput, "Project name: 1–60 characters"}, // 62 UTF-16 units
		{"!!!", domain.CodeInvalidInput, "Project name needs a letter or digit (a-z, 0-9)"},
		{"Ação api", domain.CodeNameTaken, `Project "acao-api" already exists`},
	}
	for _, c := range cases {
		_, err := k.app.CreateProject(k.ctx, a, c.name)
		canvasWantErr(t, err, c.code, c.msg)
	}
	// 60 UTF-16 units is fine; the slug is cut to 40.
	p, err = k.app.CreateProject(k.ctx, a, strings.Repeat("b", 60))
	if err != nil || p.Project.Slug != strings.Repeat("b", 40) {
		t.Fatalf("60 chars: %+v %v", p.Project, err)
	}
	// The same slug in another organization is fine.
	if _, err := k.app.CreateProject(k.ctx, canvasMember(canvasOther), "Ação API"); err != nil {
		t.Fatal(err)
	}
	// Signed out.
	_, err = k.app.CreateProject(k.ctx, domain.Actor{}, "x")
	canvasWantErr(t, err, domain.CodeNotAuthenticated, "Not authenticated")
}

func TestCanvasListProjects(t *testing.T) {
	k := canvasSetup(t)
	a := canvasMember(canvasOrg)
	k.project(canvasOrg, "Zeta")
	k.project(canvasOrg, "Alpha")
	k.project(canvasOther, "Other")
	// An imported project whose staging environment was created before production.
	k.exec(`INSERT INTO projects (id, organization_id, name, slug, created_at) VALUES ('imp', 'org-a', 'Imported', 'imported', 0)`)
	k.exec(`INSERT INTO environments (id, project_id, name, is_production, created_at) VALUES ('stg', 'imp', 'staging', 0, 0), ('prd', 'imp', 'production', 1, 0)`)

	list, err := k.app.ListProjects(k.ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, p := range list {
		slugs = append(slugs, p.Project.Slug)
	}
	if !reflect.DeepEqual(slugs, []string{"zeta", "alpha", "imported"}) {
		t.Fatalf("slugs %v (creation order, own organization only)", slugs)
	}
	if envs := list[2].Environments; len(envs) != 2 || envs[0].ID != "prd" || envs[1].ID != "stg" {
		t.Fatalf("environments %+v (production first)", envs)
	}
	if home, _ := k.app.ProjectBySlug(k.ctx, a, "imported"); home == nil || home.Environment.ID != "prd" {
		t.Fatalf("by slug opens production: %+v", home)
	}

	// No membership: signed out, organization exists, no organization at all.
	_, err = k.app.ListProjects(k.ctx, domain.Actor{})
	canvasWantErr(t, err, domain.CodeNotAuthenticated, "Not authenticated")
	_, err = k.app.ListProjects(k.ctx, domain.Actor{UserID: "u"})
	canvasWantErr(t, err, domain.CodeNoOrganization, "You're not in an organization yet. Ask a member for an invite link.")
	k.exec(`DELETE FROM organizations`)
	if list, err := k.app.ListProjects(k.ctx, domain.Actor{UserID: "u"}); err != nil || len(list) != 0 {
		t.Fatalf("fresh install: %v %v", list, err)
	}
}

func TestCanvasProjectBySlug(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	home, err := k.app.ProjectBySlug(k.ctx, canvasMember(canvasOrg), "acme")
	if err != nil || home == nil || home.Environment.ID != env || home.Project.Name != "Acme" {
		t.Fatalf("by slug: %+v %v", home, err)
	}
	for _, actor := range []domain.Actor{canvasMember(canvasOther), {}, {UserID: "u"}} {
		if home, err := k.app.ProjectBySlug(k.ctx, actor, "acme"); err != nil || home != nil {
			t.Errorf("%+v sees %+v %v", actor, home, err)
		}
	}
	if home, _ := k.app.ProjectBySlug(k.ctx, canvasMember(canvasOrg), "nope"); home != nil {
		t.Errorf("unknown slug: %+v", home)
	}
}

func TestCanvasEnsureDefaultProject(t *testing.T) {
	k := canvasSetup(t)
	a := canvasMember(canvasOrg)
	slug, err := k.app.EnsureDefaultProject(k.ctx, a)
	if err != nil || slug != "acme-support" {
		t.Fatalf("first: %q %v", slug, err)
	}
	if got := k.pub.take(canvasOrg); !reflect.DeepEqual(got, []string{"/api/projects"}) {
		t.Errorf("topics %v", got)
	}
	// Again: the existing project, nothing written.
	if slug, err := k.app.EnsureDefaultProject(k.ctx, a); err != nil || slug != "acme-support" {
		t.Fatalf("again: %q %v", slug, err)
	}
	if got := k.pub.take(canvasOrg); len(got) != 0 {
		t.Errorf("topics on a no-op: %v", got)
	}
	// An organization whose first project is something else opens that one.
	k.project(canvasOther, "Billing")
	k.project(canvasOther, "Later")
	if slug, _ := k.app.EnsureDefaultProject(k.ctx, canvasMember(canvasOther)); slug != "billing" {
		t.Errorf("other org: %q", slug)
	}
	// Founding: the seam gives a signed-in user without a membership the organization; the
	// session's organization views are invalidated too.
	app.StubCanvasSeams(t, app.CanvasSeams{Join: func(tx app.Tx, actor domain.Actor, now int64) (domain.Actor, error) {
		actor.OrganizationID, actor.Role = "org-new", domain.RoleOwner
		return actor, nil
	}})
	k.exec(`INSERT INTO organizations (id, name, slug, created_at) VALUES ('org-new', 'Default', 'default', 1)`)
	if slug, err := k.app.EnsureDefaultProject(k.ctx, domain.Actor{UserID: "founder"}); err != nil || slug != "acme-support" {
		t.Fatalf("founding: %q %v", slug, err)
	}
	if got := k.pub.take("org-new"); !reflect.DeepEqual(got, []string{"/api/me", "/api/organization", "/api/projects"}) {
		t.Errorf("founding topics %v", got)
	}
	// Signed out.
	_, err = k.app.EnsureDefaultProject(k.ctx, domain.Actor{})
	canvasWantErr(t, err, domain.CodeNotAuthenticated, "Not authenticated")
}
