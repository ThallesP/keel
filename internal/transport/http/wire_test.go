package http

import (
	"bytes"
	"strings"
	"testing"
)

func TestWireJSON(t *testing.T) {
	var body struct {
		Items  []string          `json:"items"`
		Counts map[string]int    `json:"counts,omitempty"`
		Port   int               `json:"port,omitempty"`
		On     bool              `json:"on,omitempty"`
		Note   string            `json:"note"`
		Nested struct{ X []int } `json:"nested"`
	}
	body.Note = "<a & b>"
	var buf bytes.Buffer
	if err := Config("test").Formats["application/json"].Marshal(&buf, body); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(buf.String())
	want := `{"items":[],"note":"<a & b>","nested":{"X":[]}}`
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}
