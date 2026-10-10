package app

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

type invStore struct{ commitErr error }

func (s invStore) Read(_ context.Context, fn func(Tx) error) error { return fn(nil) }
func (s invStore) Write(_ context.Context, fn func(Tx) error) error {
	if err := fn(nil); err != nil {
		return err
	}
	return s.commitErr
}

type invPublisher map[string][]string

func (p invPublisher) Publish(org string, topics []string) { p[org] = append(p[org], topics...) }

func TestWriteRecordsInvalidationsForTheRequest(t *testing.T) {
	pub := invPublisher{}
	a := New(App{Store: invStore{}, Events: pub})
	ctx, rec := WithInvalidations(context.Background())

	if err := a.write(ctx, func(_ Tx, ch *Changes) error {
		ch.Projects("org-a")
		ch.Environment("org-a", "env1")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.write(ctx, func(_ Tx, ch *Changes) error {
		ch.Projects("org-a")
		ch.Organization("org-b")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	if err := a.write(ctx, func(_ Tx, ch *Changes) error {
		ch.Environment("org-a", "rolled-back")
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	failing := New(App{Store: invStore{commitErr: boom}, Events: pub})
	_ = failing.write(ctx, func(_ Tx, ch *Changes) error { ch.Environment("org-a", "not-committed"); return nil })

	if got, want := rec.Topics("org-a"), []string{"/api/environments/env1", "/api/nodes/", "/api/projects"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("org-a topics = %v, want %v", got, want)
	}
	if got, want := rec.Topics("org-b"), []string{"/api/organization"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("org-b topics = %v, want %v", got, want)
	}
	if got, want := rec.Topics(""), []string{"/api/environments/env1", "/api/nodes/", "/api/organization", "/api/projects"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("all topics = %v, want %v", got, want)
	}
	if got := rec.Topics("org-c"); len(got) != 0 {
		t.Fatalf("org-c topics = %v", got)
	}
	if len(pub["org-a"]) == 0 || len(pub["org-b"]) == 0 {
		t.Fatalf("published %v", pub)
	}

	if err := a.write(context.Background(), func(_ Tx, ch *Changes) error { ch.Projects("org-a"); return nil }); err != nil {
		t.Fatal(err)
	}
	if InvalidationsFrom(context.Background()) != nil {
		t.Fatal("recorder out of nowhere")
	}
}

func TestRecoverPartContainsPanics(t *testing.T) {
	var logs strings.Builder
	a := New(App{Log: slog.New(slog.NewTextHandler(&logs, nil))})
	var ran []string
	for _, part := range []func(context.Context){
		func(context.Context) { ran = append(ran, "deploy") },
		func(context.Context) { panic("nil map in ingress") },
		func(context.Context) { ran = append(ran, "observability") },
	} {
		a.recoverPart(context.Background(), part)
	}
	if !reflect.DeepEqual(ran, []string{"deploy", "observability"}) {
		t.Fatalf("ran %v", ran)
	}
	if !strings.Contains(logs.String(), "nil map in ingress") {
		t.Fatalf("panic not logged: %s", logs.String())
	}
	a.Recover(context.Background())
}
