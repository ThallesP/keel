package domain

import (
	"crypto/rand"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Canvas node rules: defaults per type, database engines, generated credentials, naming and
// placement (docs/go/spec/projects.md §3). Owner: the canvas area.

// NodeDefault is what a node of a type starts with. Image is "" for types without a runtime
// (volume, group): they get no Desired.
type NodeDefault struct {
	Name  string
	Image string
	Port  int
}

// NodeDefaults (convex/nodeHelpers.ts DEFAULTS).
var NodeDefaults = map[NodeType]NodeDefault{
	NodeService:  {Name: "service", Image: "nginx:alpine", Port: 80},
	NodeDatabase: {Name: "postgres", Image: "postgres:16", Port: 5432},
	NodeCache:    {Name: "redis", Image: "redis:7", Port: 6379},
	NodeVolume:   {Name: "data"},
	NodeGroup:    {Name: "group"},
}

// DefaultConfig is a new node's config: volumes 10 GB, groups 300x180, others nothing.
func DefaultConfig(t NodeType) NodeConfig {
	f := func(v float64) *float64 { return &v }
	switch t {
	case NodeVolume:
		return NodeConfig{SizeGb: f(10)}
	case NodeGroup:
		return NodeConfig{Width: f(300), Height: f(180)}
	}
	return NodeConfig{}
}

// Engine is a database or cache the Add dialog offers. Keys match the image repo, so EngineOf can
// recover it from an image later.
type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
	EngineMongo    Engine = "mongo"
	EngineRedis    Engine = "redis"
)

// EngineSpec: the node type, image and port an engine runs as.
type EngineSpec struct {
	Type  NodeType
	Image string
	Port  int
}

// Engines (convex/nodeHelpers.ts ENGINES).
var Engines = map[Engine]EngineSpec{
	EnginePostgres: {Type: NodeDatabase, Image: "postgres:16", Port: 5432},
	EngineMySQL:    {Type: NodeDatabase, Image: "mysql:8", Port: 3306},
	EngineMongo:    {Type: NodeDatabase, Image: "mongo:7", Port: 27017},
	EngineRedis:    {Type: NodeCache, Image: "redis:7", Port: 6379},
}

// imageRepo: `ghcr.io/acme/api:1.2@sha256:…` → `api` (the JS split("@")[0].split("/").pop().split(":")[0]).
func imageRepo(image string) string {
	s, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s, _, _ = strings.Cut(s, ":")
	return s
}

// EngineOf: `postgres:16` → postgres, `docker.io/library/mysql:8.4` → mysql, `nginx` → "". It is
// image based, so a service running `redis:7` is Redis too (expose guard, --requirepass).
func EngineOf(image string) Engine {
	if image == "" {
		return ""
	}
	e := Engine(imageRepo(image))
	if _, ok := Engines[e]; ok {
		return e
	}
	return ""
}

// secretAlphabet has no l, o, I, O, 0 or 1.
const secretAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RandomSecret is n characters drawn uniformly from secretAlphabet with crypto/rand.
func RandomSecret(n int) string {
	max := big.NewInt(int64(len(secretAlphabet)))
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand never fails on supported platforms
		}
		b[i] = secretAlphabet[k.Int64()]
	}
	return string(b)
}

// SeedVariables are the credentials an engine's official image reads on first boot, in insertion
// order. Redis reads none from its env: apply passes REDIS_PASSWORD to `redis-server --requirepass`.
func SeedVariables(e Engine) []Variable {
	row := func(key, value string, secret bool) Variable { return Variable{Key: key, Value: value, Secret: secret} }
	switch e {
	case EnginePostgres:
		return []Variable{row("POSTGRES_USER", "app", false), row("POSTGRES_PASSWORD", RandomSecret(20), true), row("POSTGRES_DB", "app", false)}
	case EngineMySQL:
		return []Variable{row("MYSQL_ROOT_PASSWORD", RandomSecret(20), true), row("MYSQL_USER", "app", false),
			row("MYSQL_PASSWORD", RandomSecret(20), true), row("MYSQL_DATABASE", "app", false)}
	case EngineMongo:
		return []Variable{row("MONGO_INITDB_ROOT_USERNAME", "app", false), row("MONGO_INITDB_ROOT_PASSWORD", RandomSecret(20), true)}
	case EngineRedis:
		return []Variable{row("REDIS_PASSWORD", RandomSecret(20), true)}
	}
	return nil
}

// MaxNameLen is the longest node name validName accepts and a reference can address.
const MaxNameLen = 40

// UniqueName: base if free, else base-2, base-3, … . Unlike convex/nodeHelpers.ts it clamps base
// so the result stays within MaxNameLen (a 40-char base used to give an unaddressable 42-char
// name); the base itself is returned as is when free.
func UniqueName(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		b := base
		if len(b)+len(suffix) > MaxNameLen && len(suffix) < MaxNameLen {
			b = b[:MaxNameLen-len(suffix)]
		}
		if name := b + suffix; !taken[name] {
			return name
		}
	}
}

var nonName = regexp.MustCompile(`[^a-z0-9-]+`)

// NameFromImage: `ghcr.io/acme/api-server:1.2` → `api-server`; fallback when nothing is left.
// Trimmed of leading/trailing `-` before the 40-char cut, so it may end in `-` (as in TS).
func NameFromImage(image, fallback string) string {
	s := nonName.ReplaceAllString(strings.ToLower(imageRepo(image)), "-")
	s = strings.Trim(s, "-")
	if len(s) > MaxNameLen {
		s = s[:MaxNameLen]
	}
	if s == "" {
		return fallback
	}
	return s
}

// NodeWidth is a canvas node's width (node-shell.tsx); groups carry their own in config.width.
const NodeWidth = 220

// NextPosition is where a node created without a position lands (the CLI has no canvas): right of
// the rightmost top-level node, level with it; {0, 0} on an empty canvas.
func NextPosition(nodes []Node) Position {
	var at *Position
	for _, n := range nodes {
		if n.ParentID != "" {
			continue // relative to its group
		}
		w := float64(NodeWidth)
		if n.Config.Width != nil {
			w = *n.Config.Width
		}
		x := n.Position.X + w + 60
		if at == nil || x > at.X {
			at = &Position{X: x, Y: n.Position.Y}
		}
	}
	if at == nil {
		return Position{}
	}
	return *at
}

// PortNumber validates a JSON number as a port (JS Number.isInteger: 80.0 is fine, 80.5 is not).
func PortNumber(p *float64) (*int, error) {
	v, ok := intNumber(p, 1, 65535)
	if !ok {
		return nil, Invalid("Port must be 1–65535")
	}
	return v, nil
}

// ReplicasNumber validates a JSON number as a replica count.
func ReplicasNumber(r *float64) (*int, error) {
	v, ok := intNumber(r, 0, 20)
	if !ok {
		return nil, Invalid("Replicas must be 0–20")
	}
	return v, nil
}

func intNumber(p *float64, lo, hi int) (*int, bool) {
	if p == nil {
		return nil, true
	}
	f := *p
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) || f < float64(lo) || f > float64(hi) {
		return nil, false
	}
	v := int(f)
	return &v, true
}

// UTF16Len is a string's JavaScript length (UTF-16 code units): the unit every Convex length
// limit was written in.
func UTF16Len(s string) int {
	n := 0
	for _, r := range s { // ranging yields U+FFFD for invalid bytes, never a surrogate
		n += utf16.RuneLen(r)
	}
	return n
}

// IsJSSpace is the ECMAScript WhiteSpace + LineTerminator set (String.prototype.trim, `\s`).
func IsJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// TrimJS is JavaScript's String.prototype.trim.
func TrimJS(s string) string { return strings.TrimFunc(s, IsJSSpace) }
