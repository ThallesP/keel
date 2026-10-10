package domain

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var canvasRefRE = regexp.MustCompile(`\$\{\{\s*(?:([a-z0-9-]{1,40})\.)?([A-Z_][A-Z0-9_]{0,63})\s*\}\}`)

type RefMatch struct {
	Start, End int
	Name, Key  string
}

func FindRefs(value string) []RefMatch {
	idx := canvasRefRE.FindAllStringSubmatchIndex(value, -1)
	out := make([]RefMatch, 0, len(idx))
	for _, m := range idx {
		r := RefMatch{Start: m[0], End: m[1], Key: value[m[4]:m[5]]}
		if m[2] >= 0 {
			r.Name = value[m[2]:m[3]]
		}
		out = append(out, r)
	}
	return out
}

func RewriteRefs(value, rowNodeID string, node Node, to func(oldKey string) (name, key string)) string {
	var b strings.Builder
	last := 0
	for _, m := range FindRefs(value) {
		b.WriteString(value[last:m.Start])
		last = m.End
		if !(m.Name == node.Name || m.Name == "" && rowNodeID == node.ID) {
			b.WriteString(value[m.Start:m.End])
			continue
		}
		name, key := to(m.Key)
		if m.Name == "" {
			b.WriteString("${{ " + key + " }}")
			continue
		}
		b.WriteString(RefText(name, key))
	}
	b.WriteString(value[last:])
	return b.String()
}

func Referrers(nodes []Node, vars []Variable, startID string) []string {
	byName := make(map[string]string, len(nodes))
	for _, n := range nodes {
		byName[n.Name] = n.ID
	}
	type edge struct{ to, from string }
	refers := map[edge]bool{}
	for _, v := range vars {
		for _, m := range FindRefs(v.Value) {
			target := v.NodeID
			if m.Name != "" {
				target = byName[m.Name]
			}
			if target != "" && target != v.NodeID {
				refers[edge{target, v.NodeID}] = true
			}
		}
	}
	seen := map[string]bool{startID: true}
	queue := []string{startID}
	for at := 0; at < len(queue); at++ {
		for _, n := range nodes {
			if refers[edge{queue[at], n.ID}] && !seen[n.ID] {
				seen[n.ID] = true
				queue = append(queue, n.ID)
			}
		}
	}
	return queue[1:]
}

type ProvidedKey struct {
	Key    string
	Value  string
	Secret bool
}

func ProvidedKeys(node Node, get func(key, fallback string) string) []ProvidedKey {
	d := node.Desired
	if d == nil {
		return nil
	}
	host := node.ServiceName()
	hostPort := func(def int) string {
		if d.Port != nil {
			def = *d.Port
		}
		return host + ":" + strconv.Itoa(def)
	}
	var out []ProvidedKey
	switch node.Type {
	case NodeDatabase:
		var u url.URL
		switch EngineOf(d.Image) {
		case EngineMySQL:
			u = url.URL{Scheme: "mysql", User: url.UserPassword(get("MYSQL_USER", "app"), get("MYSQL_PASSWORD", "")), Host: hostPort(3306), Path: "/" + get("MYSQL_DATABASE", "app")}
		case EngineMongo:
			u = url.URL{Scheme: "mongodb", User: url.UserPassword(get("MONGO_INITDB_ROOT_USERNAME", "app"), get("MONGO_INITDB_ROOT_PASSWORD", "")), Host: hostPort(27017)}
		default:
			u = url.URL{Scheme: "postgres", User: url.UserPassword(get("POSTGRES_USER", "app"), get("POSTGRES_PASSWORD", "")), Host: hostPort(5432), Path: "/" + get("POSTGRES_DB", "app")}
		}
		out = append(out, ProvidedKey{"DATABASE_URL", u.String(), true})
	case NodeCache:
		u := url.URL{Scheme: "redis", Host: hostPort(6379)}
		pass := get("REDIS_PASSWORD", "")
		if pass != "" {
			u.User = url.UserPassword("default", pass)
		}
		out = append(out, ProvidedKey{"REDIS_URL", u.String(), pass != ""})
	case NodeService:
		if d.Port != nil {
			out = append(out, ProvidedKey{"URL", "http://" + hostPort(0), false})
		}
	}
	out = append(out, ProvidedKey{"HOST", host, false})
	if d.Port != nil {
		out = append(out, ProvidedKey{"PORT", strconv.Itoa(*d.Port), false})
	}
	return out
}

func SuggestedKey(node, key string) string {
	switch key {
	case "URL", "HOST", "PORT":
		return strings.ToUpper(strings.ReplaceAll(node, "-", "_")) + "_" + key
	}
	return key
}

func RefText(node, key string) string { return "${{ " + node + "." + key + " }}" }

type RefPart struct {
	Text string
	Ref  *Ref
}

type Ref struct {
	Node    string
	NodeID  string
	Key     string
	Missing bool
}

type Expansion struct {
	Resolved string
	Secret   bool
	Parts    []RefPart
}

type Resolver struct {
	byName map[string]Node
	vars   map[string][]Variable
}

func NewResolver(nodes []Node, vars []Variable) *Resolver {
	r := &Resolver{byName: make(map[string]Node, len(nodes)), vars: map[string][]Variable{}}
	for _, n := range nodes {
		r.byName[n.Name] = n
	}
	for _, v := range vars {
		r.vars[v.NodeID] = append(r.vars[v.NodeID], v)
	}
	return r
}

func (r *Resolver) Own(nodeID string) []Variable { return r.vars[nodeID] }

func (r *Resolver) row(nodeID, key string) (Variable, bool) {
	for _, v := range r.vars[nodeID] {
		if v.Key == key {
			return v, true
		}
	}
	return Variable{}, false
}

func (r *Resolver) Expand(node Node, value string) Expansion { return r.expand(node, value, 0) }

func (r *Resolver) expand(node Node, value string, depth int) Expansion {
	var out Expansion
	var b strings.Builder
	last := 0
	for _, m := range FindRefs(value) {
		if m.Start > last {
			out.Parts = append(out.Parts, RefPart{Text: value[last:m.Start]})
		}
		b.WriteString(value[last:m.Start])
		last = m.End
		target, found := node, true
		if m.Name != "" {
			target, found = r.byName[m.Name]
		}
		hit, ok := ProvidedKey{}, false
		if found && depth < 5 {
			hit, ok = r.lookup(target, m.Key, depth)
		}
		out.Parts = append(out.Parts, RefPart{Ref: &Ref{Node: m.Name, NodeID: target.ID, Key: m.Key, Missing: !ok}})
		b.WriteString(hit.Value)
		out.Secret = out.Secret || hit.Secret
	}
	if last < len(value) {
		out.Parts = append(out.Parts, RefPart{Text: value[last:]})
	}
	b.WriteString(value[last:])
	out.Resolved = b.String()
	return out
}

func (r *Resolver) lookup(target Node, key string, depth int) (ProvidedKey, bool) {
	if row, ok := r.row(target.ID, key); ok {
		inner := r.expand(target, row.Value, depth+1)
		return ProvidedKey{Key: key, Value: inner.Resolved, Secret: row.Secret || inner.Secret}, true
	}
	get := func(k, fallback string) string {
		if row, ok := r.row(target.ID, k); ok {
			return r.expand(target, row.Value, depth+1).Resolved
		}
		return fallback
	}
	for _, p := range ProvidedKeys(target, get) {
		if p.Key == key {
			return p, true
		}
	}
	return ProvidedKey{}, false
}
