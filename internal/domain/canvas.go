package domain

import (
	"cmp"
	"crypto/rand"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

type NodeDefault struct {
	Name  string
	Image string
	Port  int
}

var NodeDefaults = map[NodeType]NodeDefault{
	NodeService:  {Name: "service", Image: "nginx:alpine", Port: 80},
	NodeDatabase: {Name: "postgres", Image: "postgres:16", Port: 5432},
	NodeCache:    {Name: "redis", Image: "redis:7", Port: 6379},
	NodeVolume:   {Name: "data"},
	NodeGroup:    {Name: "group"},
}

func DefaultConfig(t NodeType) NodeConfig {
	switch t {
	case NodeVolume:
		return NodeConfig{SizeGb: new(10.0)}
	case NodeGroup:
		return NodeConfig{Width: new(300.0), Height: new(180.0)}
	}
	return NodeConfig{}
}

type Engine string

const (
	EnginePostgres Engine = "postgres"
	EngineMySQL    Engine = "mysql"
	EngineMongo    Engine = "mongo"
	EngineRedis    Engine = "redis"
)

type EngineSpec struct {
	Type  NodeType
	Image string
	Port  int
}

var Engines = map[Engine]EngineSpec{
	EnginePostgres: {Type: NodeDatabase, Image: "postgres:16", Port: 5432},
	EngineMySQL:    {Type: NodeDatabase, Image: "mysql:8", Port: 3306},
	EngineMongo:    {Type: NodeDatabase, Image: "mongo:7", Port: 27017},
	EngineRedis:    {Type: NodeCache, Image: "redis:7", Port: 6379},
}

func canvasImageRepo(image string) string {
	s, _, _ := strings.Cut(image, "@")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	s, _, _ = strings.Cut(s, ":")
	return s
}

func EngineOf(image string) Engine {
	e := Engine(canvasImageRepo(image))
	if _, ok := Engines[e]; ok {
		return e
	}
	return ""
}

const canvasSecretAlphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

func RandomSecret(n int) string {
	max := big.NewInt(int64(len(canvasSecretAlphabet)))
	b := make([]byte, n)
	for i := range b {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = canvasSecretAlphabet[k.Int64()]
	}
	return string(b)
}

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

const MaxNameLen = 40

func UniqueName(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		suffix := "-" + strconv.Itoa(i)
		if name := base[:min(len(base), MaxNameLen-len(suffix))] + suffix; !taken[name] {
			return name
		}
	}
}

var canvasNonName = regexp.MustCompile(`[^a-z0-9-]+`)

func NameFromImage(image, fallback string) string {
	s := strings.Trim(canvasNonName.ReplaceAllString(strings.ToLower(canvasImageRepo(image)), "-"), "-")
	return cmp.Or(s[:min(len(s), MaxNameLen)], fallback)
}

func NextPosition(nodes []Node) Position {
	var at *Position
	for _, n := range nodes {
		if n.ParentID != "" {
			continue
		}
		w := 220.0
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

func PortNumber(p *float64) (*int, error) {
	return canvasIntNumber(p, 1, 65535, Invalid(MsgPortRange))
}

func ReplicasNumber(r *float64) (*int, error) {
	return canvasIntNumber(r, 0, 20, Invalid("Replicas must be 0–20"))
}

func canvasIntNumber(p *float64, lo, hi float64, invalid error) (*int, error) {
	if p == nil {
		return nil, nil
	}
	if f := *p; f != math.Trunc(f) || f < lo || f > hi {
		return nil, invalid
	}
	return new(int(*p)), nil
}

func UTF16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func isJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func TrimJS(s string) string { return strings.TrimFunc(s, isJSSpace) }
