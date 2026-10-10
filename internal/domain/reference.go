package domain

import (
	"regexp"
	"strconv"
	"strings"
)

const canvasJSSpace = `[\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]*`

var canvasRefRE = regexp.MustCompile(`\$\{\{` + canvasJSSpace + `(?:([a-z0-9-]{1,40})\.)?([A-Z_][A-Z0-9_]{0,63})` + canvasJSSpace + `\}\}`)

const MaxRefDepth = 5

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

func canvasPointsAt(name, rowNodeID string, node Node) bool {
	if name == "" {
		return rowNodeID == node.ID
	}
	return name == node.Name
}

func RewriteRefs(value, rowNodeID string, node Node, to func(oldKey string) (name, key string)) string {
	refs := FindRefs(value)
	if len(refs) == 0 {
		return value
	}
	var b strings.Builder
	last := 0
	for _, m := range refs {
		b.WriteString(value[last:m.Start])
		last = m.End
		if !canvasPointsAt(m.Name, rowNodeID, node) {
			b.WriteString(value[m.Start:m.End])
			continue
		}
		name, key := to(m.Key)
		if m.Name == "" {
			b.WriteString("${{ " + key + " }}")
		} else {
			b.WriteString("${{ " + name + "." + key + " }}")
		}
	}
	b.WriteString(value[last:])
	return b.String()
}

func Referrers(nodes []Node, vars []Variable, startID string) []string {
	byName := make(map[string]string, len(nodes))
	for _, n := range nodes {
		byName[n.Name] = n.ID
	}
	referrers := map[string]map[string]bool{}
	for _, v := range vars {
		for _, m := range FindRefs(v.Value) {
			target := v.NodeID
			if m.Name != "" {
				target = byName[m.Name]
			}
			if target == "" || target == v.NodeID {
				continue
			}
			if referrers[target] == nil {
				referrers[target] = map[string]bool{}
			}
			referrers[target][v.NodeID] = true
		}
	}
	seen := map[string]bool{startID: true}
	queue := []string{startID}
	var out []string
	for at := 0; at < len(queue); at++ {
		for _, n := range nodes {
			if !referrers[queue[at]][n.ID] || seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			queue = append(queue, n.ID)
			out = append(out, n.ID)
		}
	}
	return out
}

func EncodeURIComponent(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '!', c == '~', c == '*', c == '\'', c == '(', c == ')':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
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
	port := func(def int) string {
		if d.Port != nil {
			return strconv.Itoa(*d.Port)
		}
		return strconv.Itoa(def)
	}
	enc := EncodeURIComponent
	var out []ProvidedKey
	switch node.Type {
	case NodeDatabase:
		switch EngineOf(d.Image) {
		case EngineMySQL:
			user, pass, db := get("MYSQL_USER", "app"), get("MYSQL_PASSWORD", ""), get("MYSQL_DATABASE", "app")
			out = append(out, ProvidedKey{"DATABASE_URL", "mysql://" + enc(user) + ":" + enc(pass) + "@" + host + ":" + port(3306) + "/" + enc(db), true})
		case EngineMongo:
			user, pass := get("MONGO_INITDB_ROOT_USERNAME", "app"), get("MONGO_INITDB_ROOT_PASSWORD", "")
			out = append(out, ProvidedKey{"DATABASE_URL", "mongodb://" + enc(user) + ":" + enc(pass) + "@" + host + ":" + port(27017), true})
		default:
			user, pass, db := get("POSTGRES_USER", "app"), get("POSTGRES_PASSWORD", ""), get("POSTGRES_DB", "app")
			out = append(out, ProvidedKey{"DATABASE_URL", "postgres://" + enc(user) + ":" + enc(pass) + "@" + host + ":" + port(5432) + "/" + enc(db), true})
		}
	case NodeCache:
		pass := get("REDIS_PASSWORD", "")
		auth := ""
		if pass != "" {
			auth = "default:" + enc(pass) + "@"
		}
		out = append(out, ProvidedKey{"REDIS_URL", "redis://" + auth + host + ":" + port(6379), pass != ""})
	case NodeService:
		if d.Port != nil {
			out = append(out, ProvidedKey{"URL", "http://" + host + ":" + port(0), false})
		}
	}
	out = append(out, ProvidedKey{"HOST", host, false})
	if d.Port != nil {
		out = append(out, ProvidedKey{"PORT", port(0), false})
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
		var hit *ProvidedKey
		if found && depth < MaxRefDepth {
			hit = r.lookup(target, m.Key, depth)
		}
		ref := &Ref{Node: m.Name, Key: m.Key, Missing: hit == nil}
		if found {
			ref.NodeID = target.ID
		}
		out.Parts = append(out.Parts, RefPart{Ref: ref})
		if hit != nil {
			b.WriteString(hit.Value)
			out.Secret = out.Secret || hit.Secret
		}
	}
	if last < len(value) {
		out.Parts = append(out.Parts, RefPart{Text: value[last:]})
	}
	b.WriteString(value[last:])
	out.Resolved = b.String()
	return out
}

func (r *Resolver) lookup(target Node, key string, depth int) *ProvidedKey {
	if row, ok := r.row(target.ID, key); ok {
		inner := r.expand(target, row.Value, depth+1)
		return &ProvidedKey{Key: key, Value: inner.Resolved, Secret: row.Secret || inner.Secret}
	}
	get := func(k, fallback string) string {
		if row, ok := r.row(target.ID, k); ok {
			return r.expand(target, row.Value, depth+1).Resolved
		}
		return fallback
	}
	for _, p := range ProvidedKeys(target, get) {
		if p.Key == key {
			return &p
		}
	}
	return nil
}

func (r *Resolver) Env(node Node) []string {
	rows := r.vars[node.ID]
	env := make([]string, 0, len(rows))
	for _, v := range rows {
		env = append(env, v.Key+"="+r.Expand(node, v.Value).Resolved)
	}
	return env
}
