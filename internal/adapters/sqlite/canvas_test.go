package sqlite

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestCanvasTx(t *testing.T) {
	s := OpenTest(t)
	env := seedEnvironment(t, s)
	ctx := context.Background()
	err := s.Write(ctx, func(tx app.Tx) error {
		a := domain.Node{ID: "a", EnvironmentID: env, Type: domain.NodeService, Name: "api", CreatedAt: 1}
		if err := tx.CanvasInsertNode(a); err != nil {
			return err
		}
		if err := tx.CanvasInsertNode(domain.Node{ID: "b", EnvironmentID: env, Type: domain.NodeVolume, Name: "api", CreatedAt: 2}); !errors.Is(err, app.ErrCanvasTaken) {
			t.Errorf("duplicate name: %v", err)
		}
		if err := tx.CanvasInsertNode(domain.Node{ID: "b", EnvironmentID: env, Type: domain.NodeVolume, Name: "data", CreatedAt: 2}); err != nil {
			return err
		}
		a.Name = "data"
		if err := tx.CanvasUpdateNode(a); !errors.Is(err, app.ErrCanvasTaken) {
			t.Errorf("rename onto a taken name: %v", err)
		}

		for _, k := range []string{"Z", "A", "M"} {
			if err := tx.CanvasInsertVariable(domain.Variable{ID: "v" + k, NodeID: "a", Key: k, Value: k}); err != nil {
				return err
			}
		}
		if err := tx.CanvasInsertVariable(domain.Variable{ID: "dup", NodeID: "a", Key: "A"}); !errors.Is(err, app.ErrCanvasTaken) {
			t.Errorf("duplicate key: %v", err)
		}
		if err := tx.CanvasUpdateVariable(domain.Variable{ID: "vZ", Key: "ZZ", Value: "z", Secret: true}); err != nil {
			return err
		}
		if err := tx.CanvasUpdateVariable(domain.Variable{ID: "vM", Key: "A"}); !errors.Is(err, app.ErrCanvasTaken) {
			t.Errorf("rename onto a taken key: %v", err)
		}
		if err := tx.CanvasInsertVariable(domain.Variable{ID: "vb", NodeID: "b", Key: "B", Value: "b"}); err != nil {
			return err
		}
		vs, err := tx.CanvasVariables("a")
		if err != nil {
			return err
		}
		want := []domain.Variable{
			{ID: "vZ", NodeID: "a", Key: "ZZ", Value: "z", Secret: true},
			{ID: "vA", NodeID: "a", Key: "A", Value: "A"},
			{ID: "vM", NodeID: "a", Key: "M", Value: "M"},
		}
		if !reflect.DeepEqual(vs, want) {
			t.Errorf("variables %+v", vs)
		}
		all, err := tx.CanvasEnvironmentVariables(env)
		if err != nil || len(all) != 4 {
			t.Errorf("environment variables %+v %v", all, err)
		}
		for _, wantDeleted := range []bool{true, false} {
			if deleted, err := tx.CanvasDeleteVariable("a", "M"); deleted != wantDeleted || err != nil {
				t.Errorf("delete M: %v %v, want %v", deleted, err, wantDeleted)
			}
		}
		if err := tx.CanvasMarkDirty("b"); err != nil {
			return err
		}
		if n, _ := tx.Node("b"); !n.Dirty || n.Name != "data" {
			t.Errorf("mark dirty: %+v", n)
		}
		if err := tx.CanvasDeleteNodeVariables("a"); err != nil {
			return err
		}
		if vs, _ := tx.CanvasVariables("a"); len(vs) != 0 {
			t.Errorf("left %+v", vs)
		}

		if _, err := tx.CanvasProjectBySlug("org", "nope"); !errors.Is(err, app.ErrNoRow) {
			t.Errorf("missing slug: %v", err)
		}
		if err := tx.CanvasInsertProject(domain.Project{ID: "p2", OrganizationID: "org", Name: "Acme", Slug: "acme"}); !errors.Is(err, app.ErrCanvasTaken) {
			t.Errorf("duplicate slug: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
