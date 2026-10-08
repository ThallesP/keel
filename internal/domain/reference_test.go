package domain

import (
	"reflect"
	"strings"
	"testing"
)

func TestCanvasEncodeURIComponent(t *testing.T) {
	cases := map[string]string{
		"":         "",
		"app":      "app",
		"A-z_0.9!": "A-z_0.9!",
		// Expected values are node's encodeURIComponent output.
		"a b!~*'()$&+,/:;=?@#%é€😀_-.\t": "a%20b!~*'()%24%26%2B%2C%2F%3A%3B%3D%3F%40%23%25%C3%A9%E2%82%AC%F0%9F%98%80_-.%09",
		"p@ss/w#rd":                     "p%40ss%2Fw%23rd",
	}
	for in, want := range cases {
		if got := EncodeURIComponent(in); got != want {
			t.Errorf("EncodeURIComponent(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCanvasFindRefs(t *testing.T) {
	type ref struct{ text, name, key string }
	cases := []struct {
		in   string
		want []ref
	}{
		{"${{ pg.URL }}", []ref{{"${{ pg.URL }}", "pg", "URL"}}},
		{"x${{A}}y${{ b.B }}", []ref{{"${{A}}", "", "A"}, {"${{ b.B }}", "b", "B"}}},
		// ECMAScript \s: vertical tab, no-break space, BOM, ideographic space count; NEL does not.
		{"${{\vA\u00a0}}", []ref{{"${{\vA\u00a0}}", "", "A"}}},
		{"${{\ufeffA\u3000}}", []ref{{"${{\ufeffA\u3000}}", "", "A"}}},
		{"${{\u0085A}}", nil},
		{"${{ pg.url }}", nil}, // lowercase key: literal text
		{"${{ a.b.C }}", nil},
		{"${{ ${{ A }} }}", []ref{{"${{ A }}", "", "A"}}},
		{"${{  A  }}${{B}}", []ref{{"${{  A  }}", "", "A"}, {"${{B}}", "", "B"}}},
		{"$${{ A }}}", []ref{{"${{ A }}", "", "A"}}},
		{"${{ " + strings.Repeat("a", 41) + ".A }}", nil}, // names are at most 40 chars
	}
	for _, c := range cases {
		var got []ref
		for _, m := range FindRefs(c.in) {
			got = append(got, ref{c.in[m.Start:m.End], m.Name, m.Key})
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("FindRefs(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestCanvasRewriteRefs(t *testing.T) {
	pg := Node{ID: "pg1", Name: "pg"}
	to := func(key string) (string, string) { return "db", key }
	cases := []struct{ in, row, want string }{
		// Qualified references to pg are renamed and normalized; others kept byte for byte.
		{"a ${{  pg.URL}} b ${{ other.X }} c ${{ URL }}", "api", "a ${{ db.URL }} b ${{ other.X }} c ${{ URL }}"},
		// Unqualified references on pg's own rows keep their form.
		{"${{USER}}:${{ pg.PASS }}", "pg1", "${{ USER }}:${{ db.PASS }}"},
		{"nothing here", "api", "nothing here"},
	}
	for _, c := range cases {
		if got := RewriteRefs(c.in, c.row, pg, to); got != c.want {
			t.Errorf("RewriteRefs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// A key rename: only the renamed key changes, every reference to the node is normalized.
	rename := func(k string) (string, string) {
		if k == "OLD" {
			return "pg", "NEW"
		}
		return "pg", k
	}
	if got := RewriteRefs("${{pg.OLD}}/${{pg.KEEP}}", "api", pg, rename); got != "${{ pg.NEW }}/${{ pg.KEEP }}" {
		t.Errorf("key rename: %q", got)
	}
}

func TestCanvasReferrers(t *testing.T) {
	nodes := []Node{{ID: "api", Name: "api"}, {ID: "worker", Name: "worker"}, {ID: "redis", Name: "redis"}, {ID: "web", Name: "web"}, {ID: "lone", Name: "lone"}}
	vars := []Variable{
		{NodeID: "api", Key: "Q", Value: "${{ worker.QUEUE_URL }}"},
		{NodeID: "worker", Key: "QUEUE_URL", Value: "${{ redis.REDIS_URL }}"},
		{NodeID: "redis", Key: "SELF", Value: "${{ redis.REDIS_PASSWORD }} ${{ REDIS_PASSWORD }}"}, // self: never counts
		{NodeID: "web", Key: "API", Value: "${{ api.URL }} ${{ ghost.URL }}"},
		{NodeID: "redis", Key: "BACK", Value: "${{ web.X }}"}, // a cycle back to redis
	}
	if got, want := Referrers(nodes, vars, "redis"), []string{"worker", "api", "web"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Referrers(redis) = %v, want %v", got, want)
	}
	if got := Referrers(nodes, vars, "lone"); len(got) != 0 {
		t.Errorf("Referrers(lone) = %v", got)
	}
}

func canvasPort(p int) *int { return &p }

func TestCanvasResolver(t *testing.T) {
	pg := Node{ID: "pg1", Name: "pg", Type: NodeDatabase, Desired: &Desired{Image: "postgres:16", Port: canvasPort(5432)}}
	my := Node{ID: "my1", Name: "my", Type: NodeDatabase, Desired: &Desired{Image: "mysql:8"}}
	mongo := Node{ID: "mo1", Name: "mongo", Type: NodeDatabase, Desired: &Desired{Image: "mongo:7", Port: canvasPort(27018)}}
	redis := Node{ID: "rd1", Name: "redis", Type: NodeCache, Desired: &Desired{Image: "redis:7", Port: canvasPort(6379)}}
	bare := Node{ID: "rd2", Name: "bare", Type: NodeCache, Desired: &Desired{Image: "valkey:8"}}
	api := Node{ID: "api1", Name: "api", Type: NodeService, Desired: &Desired{Image: "nginx", Port: canvasPort(8080)}}
	noport := Node{ID: "np1", Name: "noport", Type: NodeService, Desired: &Desired{Image: "worker"}}
	vol := Node{ID: "vol1", Name: "data", Type: NodeVolume}
	nodes := []Node{pg, my, mongo, redis, bare, api, noport, vol}
	vars := []Variable{
		{NodeID: "pg1", Key: "POSTGRES_USER", Value: "app"},
		{NodeID: "pg1", Key: "POSTGRES_PASSWORD", Value: "p@ss/w#rd ${{ api.PORT }}", Secret: true},
		{NodeID: "my1", Key: "MYSQL_PASSWORD", Value: "x y", Secret: true},
		{NodeID: "rd1", Key: "REDIS_PASSWORD", Value: "s3cret", Secret: true},
		{NodeID: "api1", Key: "SELF", Value: "x${{ SELF }}"},
		{NodeID: "api1", Key: "LOOP", Value: "${{ LOOP }}"},
		{NodeID: "api1", Key: "HOST", Value: "override"},
		{NodeID: "api1", Key: "PLAIN", Value: "plain"},
		{NodeID: "noport", Key: "IGNORED", Value: "other node id, never read"},
	}
	r := NewResolver(nodes, vars)
	cases := []struct {
		value, resolved string
		secret          bool
	}{
		{"${{ pg.DATABASE_URL }}", "postgres://app:p%40ss%2Fw%23rd%208080@svc-pg1:5432/app", true},
		{"${{ my.DATABASE_URL }}", "mysql://app:x%20y@svc-my1:3306/app", true},
		{"${{ mongo.DATABASE_URL }}", "mongodb://app:@svc-mo1:27018", true},
		{"${{ redis.REDIS_URL }}", "redis://default:s3cret@svc-rd1:6379", true},
		{"${{ bare.REDIS_URL }}", "redis://svc-rd2:6379", false},
		{"${{ api.URL }} ${{ api.PORT }}", "http://svc-api1:8080 8080", false},
		{"${{ api.HOST }}", "override", false}, // an own row wins over a provided key
		{"${{ pg.HOST }}:${{ pg.PORT }}", "svc-pg1:5432", false},
		{"[${{ noport.URL }}][${{ noport.PORT }}][${{ noport.HOST }}]", "[][][svc-np1]", false},
		{"${{ data.HOST }}", "", false}, // volumes provide nothing
		{"${{ pg.POSTGRES_PASSWORD }}", "p@ss/w#rd 8080", true},
		{"${{ ghost.URL }}", "", false},
		// The lookup at depth 0 expands SELF at depths 1–5: five x. (The row's own value, expanded
		// from depth 0, has six: see Env below.)
		{"${{ api.SELF }}", "xxxxx", false},
		{"${{ api.LOOP }}", "", false},
	}
	for _, c := range cases {
		e := r.Expand(api, c.value)
		if e.Resolved != c.resolved || e.Secret != c.secret {
			t.Errorf("Expand(%q) = %q secret=%v, want %q secret=%v", c.value, e.Resolved, e.Secret, c.resolved, c.secret)
		}
	}

	// Parts: text only when non-empty; missing for unknown nodes, keys and exhausted depth; a
	// self cycle's top-level part is not missing (the depth-0 lookup hit).
	e := r.Expand(api, "a${{ PLAIN }}${{ ghost.X }}${{ pg.NOPE }}${{ LOOP }}b")
	want := []RefPart{
		{Text: "a"},
		{Ref: &Ref{Key: "PLAIN", NodeID: "api1"}},
		{Ref: &Ref{Node: "ghost", Key: "X", Missing: true}},
		{Ref: &Ref{Node: "pg", NodeID: "pg1", Key: "NOPE", Missing: true}},
		{Ref: &Ref{Key: "LOOP", NodeID: "api1"}},
		{Text: "b"},
	}
	if !reflect.DeepEqual(e.Parts, want) || e.Resolved != "aplainb" {
		t.Errorf("parts = %+v resolved %q", e.Parts, e.Resolved)
	}
	if got := r.Expand(api, "").Parts; got != nil {
		t.Errorf("empty value parts = %+v", got)
	}

	// Env: own rows in row order, expanded; provided keys not added.
	if got, want := r.Env(api), []string{"SELF=xxxxxx", "LOOP=", "HOST=override", "PLAIN=plain"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Env(api) = %q, want %q", got, want)
	}
	if got := r.Env(pg); !reflect.DeepEqual(got, []string{"POSTGRES_USER=app", "POSTGRES_PASSWORD=p@ss/w#rd 8080"}) {
		t.Errorf("Env(pg) = %q", got)
	}
}

func TestCanvasResolverDepth(t *testing.T) {
	// a → b → c → d → e → f → g: the chain is cut after depth 5.
	names := []string{"a", "b", "c", "d", "e", "f", "g"}
	var nodes []Node
	var vars []Variable
	for i, n := range names {
		nodes = append(nodes, Node{ID: n, Name: n, Type: NodeService})
		v := "end"
		if i+1 < len(names) {
			v = "${{ " + names[i+1] + ".V }}"
		}
		vars = append(vars, Variable{NodeID: n, Key: "V", Value: v})
	}
	r := NewResolver(nodes, vars)
	if got := r.Expand(nodes[0], "${{ f.V }}").Resolved; got != "end" {
		t.Errorf("short chain = %q", got)
	}
	if got := r.Expand(nodes[0], "${{ b.V }}").Resolved; got != "" {
		t.Errorf("chain past depth 5 = %q, want empty", got)
	}
}

func TestCanvasProvidedKeysOrder(t *testing.T) {
	get := func(_, fb string) string { return fb }
	cases := []struct {
		n    Node
		want []string
	}{
		{Node{ID: "1", Type: NodeDatabase, Desired: &Desired{Image: "postgres:16", Port: canvasPort(5432)}}, []string{"DATABASE_URL", "HOST", "PORT"}},
		{Node{ID: "2", Type: NodeCache, Desired: &Desired{Image: "redis:7"}}, []string{"REDIS_URL", "HOST"}},
		{Node{ID: "3", Type: NodeService, Desired: &Desired{Image: "nginx", Port: canvasPort(80)}}, []string{"URL", "HOST", "PORT"}},
		{Node{ID: "4", Type: NodeService, Desired: &Desired{Image: "nginx"}}, []string{"HOST"}},
		{Node{ID: "5", Type: NodeGroup}, nil},
	}
	for _, c := range cases {
		var got []string
		for _, p := range ProvidedKeys(c.n, get) {
			got = append(got, p.Key)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ProvidedKeys(%s) = %v, want %v", c.n.ID, got, c.want)
		}
	}
	// With every credential at its fallback a Redis URL carries no password: not secret.
	if p := ProvidedKeys(cases[1].n, get)[0]; p.Secret || p.Value != "redis://svc-2:6379" {
		t.Errorf("REDIS_URL at fallback = %+v", p)
	}
}
