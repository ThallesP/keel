package app

import (
	"encoding/binary"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/ThallesP/keel/internal/domain"
)

const axiomGoldenPath = "testdata/axiom_scenarios.golden.json"

var UpdateGolden = flag.Bool("update", false, "rewrite "+axiomGoldenPath+" from the live results")

type AxiomGolden struct {
	Scenarios json.RawMessage `json:"scenarios"`
	Spans     []domain.Span   `json:"spans"`
	Demux     []DemuxGolden   `json:"demux"`
}

type DemuxGolden struct {
	Name  string                  `json:"name"`
	Lines []domain.ServiceLogLine `json:"lines"`
}

func DockerFrame(stream byte, payload string) []byte {
	head := make([]byte, 8, 8+len(payload))
	head[0] = stream
	binary.BigEndian.PutUint32(head[4:], uint32(len(payload)))
	return append(head, payload...)
}

func ReadJSON[T any](t *testing.T, path string) T {
	t.Helper()
	var v T
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func ReadAxiomGolden(t *testing.T) AxiomGolden {
	t.Helper()
	return ReadJSON[AxiomGolden](t, axiomGoldenPath)
}

func RewriteAxiomGolden(t *testing.T, edit func(g *AxiomGolden)) {
	t.Helper()
	g := ReadAxiomGolden(t)
	edit(&g)
	b, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(axiomGoldenPath, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
