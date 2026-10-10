package app_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func TestCanvasListVariables(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	pg := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api", Port: canvasPtr(8080.0)})
	k.exec(`UPDATE variables SET value = 'pw' WHERE node_id = ? AND key = 'POSTGRES_PASSWORD'`, pg)
	k.setVar(api, "DATABASE_URL", "${{ postgres.DATABASE_URL }}?sslmode=disable")
	k.setVar(api, "SELF", "port ${{ PORT }} on ${{ HOST }}")
	if err := k.app.SetVariable(k.ctx, m, api, app.SetVariableInput{Key: "TOKEN", Value: "t0k", Secret: true}); err != nil {
		t.Fatal(err)
	}
	k.setVar(api, "MISSING", "${{ ghost.X }}")

	vars, err := k.app.ListVariables(k.ctx, m, api)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		key, value, resolved   string
		secret, resolvedSecret bool
	}
	var got []row
	for _, v := range vars {
		got = append(got, row{v.Key, v.Value, v.Resolved, v.Secret, v.ResolvedSecret})
	}
	want := []row{
		{"DATABASE_URL", "${{ postgres.DATABASE_URL }}?sslmode=disable", "postgres://app:pw@svc-" + pg + ":5432/app?sslmode=disable", false, true},
		{"SELF", "port ${{ PORT }} on ${{ HOST }}", "port 8080 on svc-" + api, false, false},
		{"TOKEN", "t0k", "t0k", true, false},
		{"MISSING", "${{ ghost.X }}", "", false, false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("variables\n got %+v\nwant %+v", got, want)
	}
	parts := vars[0].Parts
	if len(parts) != 2 || parts[0].Ref == nil || parts[0].Ref.Node != "postgres" || parts[0].Ref.NodeID != pg || parts[0].Ref.Missing ||
		parts[1].Ref != nil || parts[1].Text != "?sslmode=disable" {
		t.Errorf("parts %+v", parts)
	}
	if p := vars[3].Parts; len(p) != 1 || !p[0].Ref.Missing || p[0].Ref.NodeID != "" {
		t.Errorf("missing parts %+v", p)
	}
	if v := vars[2].Parts; len(v) != 1 || v[0].Text != "t0k" {
		t.Errorf("plain parts %+v", v)
	}

	// Signed out, unknown node: nothing.
	if vars, _ := k.app.ListVariables(k.ctx, domain.Actor{}, api); len(vars) != 0 {
		t.Error("signed out lists variables")
	}
	if vars, _ := k.app.ListVariables(k.ctx, m, "nope"); len(vars) != 0 {
		t.Error("unknown node lists variables")
	}
}

func TestCanvasReferenceable(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	k.create(env, app.CreateNodeInput{Type: domain.NodeGroup})
	k.create(env, app.CreateNodeInput{Type: domain.NodeVolume})
	redis := k.create(env, app.CreateNodeInput{Type: domain.NodeCache})
	web := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "web"})
	k.setVar(web, "HOST", "override")
	k.setVar(web, "A", "1")

	ref, err := k.app.ReferenceableVariables(k.ctx, m, api)
	if err != nil {
		t.Fatal(err)
	}
	src := ref.Sources
	if len(src) != 2 || src[0].NodeID != redis || src[1].NodeID != web || src[0].Image != "redis:7" || src[0].Type != domain.NodeCache {
		t.Fatalf("sources %+v", src)
	}
	wantRedis := []app.ReferenceKey{{Key: "REDIS_URL", As: "REDIS_URL", Provided: true}, {Key: "HOST", As: "REDIS_HOST", Provided: true}, {Key: "PORT", As: "REDIS_PORT", Provided: true}, {Key: "REDIS_PASSWORD", As: "REDIS_PASSWORD", Secret: true}}
	if !reflect.DeepEqual(src[0].Keys, wantRedis) {
		t.Errorf("redis keys %+v (REDIS_URL reads as not secret: credentials are never read here)", src[0].Keys)
	}
	wantWeb := []app.ReferenceKey{{Key: "URL", As: "WEB_URL", Provided: true}, {Key: "PORT", As: "WEB_PORT", Provided: true}, {Key: "HOST", As: "HOST"}, {Key: "A", As: "A"}}
	if !reflect.DeepEqual(src[1].Keys, wantWeb) {
		t.Errorf("web keys %+v (an own HOST shadows the provided one)", src[1].Keys)
	}
	if ref, _ := k.app.ReferenceableVariables(k.ctx, canvasMember(canvasOther), api); len(ref.Sources) != 0 {
		t.Error("foreign member sees sources")
	}
}

func TestCanvasReferenceSuggestions(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	redis := k.create(env, app.CreateNodeInput{Type: domain.NodeCache})
	web := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "my-web"})
	db := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})

	suggestions := func(node string) []app.ReferenceSuggestion {
		ref, err := k.app.ReferenceableVariables(k.ctx, m, node)
		if err != nil {
			t.Fatal(err)
		}
		return ref.Suggestions
	}
	want := []app.ReferenceSuggestion{
		{NodeID: redis, Node: "redis", Key: "REDIS_URL", As: "REDIS_URL", Value: "${{ redis.REDIS_URL }}"},
		{NodeID: web, Node: "my-web", Key: "URL", As: "MY_WEB_URL", Value: "${{ my-web.URL }}"},
		{NodeID: db, Node: "postgres", Key: "DATABASE_URL", As: "DATABASE_URL", Value: "${{ postgres.DATABASE_URL }}"},
	}
	if got := suggestions(api); !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh service:\n got  %+v\n want %+v", got, want)
	}

	k.setVar(api, "CACHE", "${{ redis.HOST }}")
	k.setVar(api, "DATABASE_URL", "postgres://elsewhere")
	if got := suggestions(api); !reflect.DeepEqual(got, want[1:2]) {
		t.Fatalf("referenced redis and taken DATABASE_URL drop out: %+v", got)
	}
	if got := suggestions(db); len(got) != 0 {
		t.Fatalf("only services get suggestions: %+v", got)
	}
}

func TestCanvasSetVariable(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	web := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "web"})
	worker := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "worker"})
	group := k.create(env, app.CreateNodeInput{Type: domain.NodeGroup})
	k.setVar(api, "FIRST", "1")
	k.setVar(api, "OLD", "secret-ish")
	k.setVar(api, "LAST", "${{ OLD }}-${{api.OLD}}")
	k.setVar(web, "FROM_API", "${{ api.OLD }} ${{ api.FIRST }}")
	k.setVar(worker, "FROM_WEB", "${{ web.FROM_API }}")
	k.setVar(group, "ANY", "groups may have variables")
	k.clean(env)
	k.pub.take(canvasOrg)

	// Rename OLD → NEW: the row keeps its place; references follow (normalized), others stay.
	err := k.app.SetVariable(k.ctx, m, api, app.SetVariableInput{Key: "NEW", Value: "v2", Secret: true, PreviousKey: canvasPtr("OLD")})
	if err != nil {
		t.Fatal(err)
	}
	vs := k.vars(api)
	if canvasKeys(vs) != "FIRST,NEW,LAST" || vs[1].Value != "v2" || !vs[1].Secret {
		t.Fatalf("api rows %+v", vs)
	}
	if vs[2].Value != "${{ NEW }}-${{ api.NEW }}" {
		t.Errorf("self references %q", vs[2].Value)
	}
	if v := k.vars(web)[0].Value; v != "${{ api.NEW }} ${{ api.FIRST }}" {
		t.Errorf("web reference %q", v)
	}
	// The node and its referrers (transitively) are staged.
	if !k.node(api).Dirty || !k.node(web).Dirty || !k.node(worker).Dirty {
		t.Errorf("dirty api %v web %v worker %v", k.node(api).Dirty, k.node(web).Dirty, k.node(worker).Dirty)
	}
	if topics := k.pub.take(canvasOrg); !canvasHas(topics, "/api/nodes/"+worker) || !canvasHas(topics, "/api/environments/"+env) {
		t.Errorf("topics %v", topics)
	}

	// Upsert in place.
	k.setVar(api, "FIRST", "one")
	if vs := k.vars(api); canvasKeys(vs) != "FIRST,NEW,LAST" || vs[0].Value != "one" {
		t.Errorf("upsert %+v", vs)
	}
	// Unknown previousKey inserts.
	if err := k.app.SetVariable(k.ctx, m, api, app.SetVariableInput{Key: "ADDED", Value: "x", PreviousKey: canvasPtr("GHOST")}); err != nil {
		t.Fatal(err)
	}
	if canvasKeys(k.vars(api)) != "FIRST,NEW,LAST,ADDED" {
		t.Errorf("unknown previousKey: %s", canvasKeys(k.vars(api)))
	}
	// Any node type, groups included.
	if err := k.app.SetVariable(k.ctx, m, group, app.SetVariableInput{Key: "MORE", Value: "x"}); err != nil {
		t.Fatal(err)
	}

	// Limits and errors.
	if err := k.app.SetVariable(k.ctx, m, api, app.SetVariableInput{Key: "BIG", Value: strings.Repeat("a", 4096)}); err != nil {
		t.Errorf("4096 chars: %v", err)
	}
	cases := []struct {
		in        app.SetVariableInput
		code, msg string
	}{
		{app.SetVariableInput{Key: "lower", Value: "x"}, domain.CodeInvalidInput, "Key: UPPER_SNAKE_CASE only"},
		{app.SetVariableInput{Key: "1ABC", Value: "x"}, domain.CodeInvalidInput, "Key: UPPER_SNAKE_CASE only"},
		{app.SetVariableInput{Key: "A/B", Value: "x"}, domain.CodeInvalidInput, "Key: UPPER_SNAKE_CASE only"},
		{app.SetVariableInput{Key: "BIG", Value: strings.Repeat("a", 4097)}, domain.CodeInvalidInput, "Value too long"},
		{app.SetVariableInput{Key: "BIG", Value: strings.Repeat("😀", 2049)}, domain.CodeInvalidInput, "Value too long"}, // 4098 UTF-16 units
		{app.SetVariableInput{Key: "FIRST", Value: "x", PreviousKey: canvasPtr("NEW")}, domain.CodeNameTaken, "FIRST already exists"},
	}
	for _, c := range cases {
		canvasWantErr(t, k.app.SetVariable(k.ctx, m, api, c.in), c.code, c.msg)
	}
	canvasWantErr(t, k.app.SetVariable(k.ctx, m, "nope", app.SetVariableInput{Key: "A"}), domain.CodeServiceNotFound, "Node not found")
}

func TestCanvasRemoveVariable(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	m := canvasMember(canvasOrg)
	pg := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	k.setVar(api, "DB", "${{ postgres.DATABASE_URL }}")
	k.clean(env)
	k.pub.take(canvasOrg)

	// Missing key: nothing staged, nothing published.
	if err := k.app.RemoveVariable(k.ctx, m, pg, "NOPE"); err != nil {
		t.Fatal(err)
	}
	if k.node(pg).Dirty || len(k.pub.take(canvasOrg)) != 0 {
		t.Error("missing key staged something")
	}
	if err := k.app.RemoveVariable(k.ctx, m, pg, "POSTGRES_PASSWORD"); err != nil {
		t.Fatal(err)
	}
	if canvasKeys(k.vars(pg)) != "POSTGRES_USER,POSTGRES_DB" || !k.node(pg).Dirty || !k.node(api).Dirty {
		t.Errorf("after remove: %s dirty pg %v api %v", canvasKeys(k.vars(pg)), k.node(pg).Dirty, k.node(api).Dirty)
	}
	canvasWantErr(t, k.app.RemoveVariable(k.ctx, m, "nope", "A"), domain.CodeServiceNotFound, "Node not found")
}

func TestCanvasComputeEnvSeam(t *testing.T) {
	k := canvasSetup(t)
	env := k.project(canvasOrg, "Acme")
	pg := k.create(env, app.CreateNodeInput{Type: domain.NodeDatabase})
	api := k.create(env, app.CreateNodeInput{Type: domain.NodeService, Name: "api"})
	k.exec(`UPDATE variables SET value = 'p w' WHERE node_id = ? AND key = 'POSTGRES_PASSWORD'`, pg)
	k.setVar(api, "Z_LAST_KEY_FIRST", "${{ postgres.DATABASE_URL }}")
	k.setVar(api, "A", "${{ Z_LAST_KEY_FIRST }}!")

	envMap, err := k.app.CanvasComputeEnv(k.ctx, api)
	if err != nil {
		t.Fatal(err)
	}
	url := "postgres://app:p%20w@svc-" + pg + ":5432/app"
	if !reflect.DeepEqual(envMap, map[string]string{"Z_LAST_KEY_FIRST": url, "A": url + "!"}) {
		t.Errorf("env map %v", envMap)
	}
	// A database's container gets its own rows only, not DATABASE_URL.
	if pgEnv, _ := k.app.CanvasComputeEnv(k.ctx, pg); len(pgEnv) != 3 || pgEnv["DATABASE_URL"] != "" {
		t.Errorf("pg env %v", pgEnv)
	}

	// markReferrersDirty as other areas call it (tracing switch, migrations).
	k.clean(env)
	if err := k.app.CanvasMarkReferrersDirty(k.ctx, pg); err != nil {
		t.Fatal(err)
	}
	if !k.node(api).Dirty || k.node(pg).Dirty {
		t.Errorf("seam: api %v pg %v (the node itself is the caller's)", k.node(api).Dirty, k.node(pg).Dirty)
	}
}
