package output

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestResultPutsOKFirst(t *testing.T) {
	for _, tc := range []struct {
		in   any
		want string
	}{
		{struct{}{}, `{"ok":true}` + "\n"},
		{struct {
			A int `json:"a"`
		}{1}, `{"ok":true,"a":1}` + "\n"},
		{map[string]string{"text": "<a & b>"}, `{"ok":true,"text":"<a & b>"}` + "\n"},
	} {
		var out bytes.Buffer
		p := &Printer{JSON: true, Out: &out, Err: &bytes.Buffer{}}
		p.Result(tc.in, nil)
		if out.String() != tc.want {
			t.Errorf("Result(%v) = %q, want %q", tc.in, out.String(), tc.want)
		}
	}
}

func TestFailJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &Printer{JSON: true, Out: &out, Err: &errOut}
	code := p.Fail(&Error{Code: CodeNotAuthenticated, Message: "Not logged in", Fix: "keel login x",
		Extra: map[string]any{"n": 1}})
	if code != ExitAuth {
		t.Errorf("exit code = %d, want %d", code, ExitAuth)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != false || got["code"] != CodeNotAuthenticated || got["fix"] != "keel login x" || got["n"] != 1.0 {
		t.Errorf("error object = %v", got)
	}
	if errOut.String() != "error: Not logged in\nfix:   keel login x\n" {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestFailHumanKeepsStdoutEmpty(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &Printer{Out: &out, Err: &errOut}
	if code := p.Fail(Errorf(CodeUsage, "", "bad")); code != ExitUsage {
		t.Errorf("exit code = %d, want %d", code, ExitUsage)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}
