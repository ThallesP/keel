package keel

import (
	"encoding/json"
	"testing"

	"github.com/ThallesP/keel/apps/cli/internal/convex"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		msg, code, fix string
	}{
		{`Project "acme-api" already exists`, output.CodeNameTaken, "Pick another name, or use it: keel link acme-api"},
		{`"api" is already taken`, output.CodeNameTaken, "Pick another name; keel service list shows the taken ones"},
		{"Node not found", output.CodeServiceNotFound, "keel service list"},
		{"Not authenticated", output.CodeNotAuthenticated, "keel login https://keel.test"},
		{"Image must look like repo/name:tag", output.CodeInvalidInput, ""},
		{"Replicas must be 0–20", output.CodeInvalidInput, ""},
	} {
		data, _ := json.Marshal(tc.msg)
		err := translate(&convex.FunctionError{Message: "Uncaught ConvexError", Data: data}, "https://keel.test")
		oe, ok := err.(*output.Error)
		if !ok || oe.Code != tc.code || oe.Fix != tc.fix {
			t.Errorf("%q: got %#v, want %s with fix %q", tc.msg, err, tc.code, tc.fix)
		}
	}
}
