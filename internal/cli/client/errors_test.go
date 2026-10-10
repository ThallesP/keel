package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThallesP/keel/internal/cli/output"
)

func TestProblemCodes(t *testing.T) {
	const url = "https://keel.test"
	for _, tc := range []struct {
		status        int
		body          string
		code, msg     string
		fix           string
		header, value string
	}{
		{404, `{"status":404,"detail":"Node not found","code":"SERVICE_NOT_FOUND"}`,
			output.CodeServiceNotFound, "Node not found", "keel service list", "", ""},
		{401, `{"status":401,"detail":"Not authenticated","code":"NOT_AUTHENTICATED"}`,
			output.CodeNotAuthenticated, "Session expired or signed out", "keel login " + url, "", ""},
		{403, `{"status":403,"detail":"You're not in an organization yet. Ask a member for an invite link.","code":"NO_ORGANIZATION"}`,
			output.CodeNoOrganization, "You're not in an organization yet. Ask a member for an invite link.",
			"Ask a member for an invite link (account menu → Invite people), then keel login " + url, "", ""},
		{409, `{"status":409,"detail":"A deployment is already running","code":"DEPLOYMENT_RUNNING"}`,
			output.CodeDeploymentRunning, "A deployment is already running", "keel deployment get --wait", "", ""},
		{409, `{"status":409,"detail":"Nothing to ship","code":"NOTHING_TO_SHIP"}`,
			output.CodeNothingToShip, "Nothing to ship", "Stage a change first (keel var set …), or redeploy: keel redeploy <service>", "", ""},
		{409, `{"status":409,"detail":"Connect Axiom to see traces","code":"TRACES_OFF"}`,
			output.CodeTracesOff, "Connect Axiom to see traces", "Open Observability in the dashboard (" + url + ") and Sign in with Axiom", "", ""},
		{409, `{"status":409,"detail":"Sign in with Axiom again to turn on traces","code":"TRACES_OFF"}`,
			output.CodeTracesOff, "Sign in with Axiom again to turn on traces", "Open Observability in the dashboard (" + url + ") and Sign in with Axiom", "", ""},
		{404, `{"status":404,"detail":"Environment not found","code":"PROJECT_NOT_FOUND"}`,
			output.CodeProjectNotFound, "Environment not found", "keel project list", "", ""},
		{409, `{"status":409,"detail":"Project \"acme-api\" already exists","code":"NAME_TAKEN"}`,
			output.CodeNameTaken, `Project "acme-api" already exists`, "Pick another name, or use it: keel link acme-api", "", ""},
		{409, `{"status":409,"detail":"Project \"acme-api\" already exists","code":"NAME_TAKEN","slug":"acme"}`,
			output.CodeNameTaken, `Project "acme-api" already exists`, "Pick another name, or use it: keel link acme", "", ""},
		{409, `{"status":409,"detail":"\"api\" is already taken","code":"NAME_TAKEN"}`,
			output.CodeNameTaken, `"api" is already taken`, "Pick another name; keel service list shows the taken ones", "", ""},
		{422, `{"status":422,"detail":"Image must look like repo/name:tag","code":"INVALID_INPUT"}`,
			output.CodeInvalidInput, "Image must look like repo/name:tag", "", "", ""},
		{422, `{"status":422,"title":"Unprocessable Entity","detail":"validation failed","code":"INVALID_INPUT","errors":[{"message":"expected number <= 65535","location":"body.port"}]}`,
			output.CodeInvalidInput, "validation failed: body.port expected number <= 65535", "", "", ""},
		{409, `{"status":409,"detail":"api.example.com is already used by web","code":"CONFLICT"}`,
			output.CodeConflict, "api.example.com is already used by web", "", "", ""},
		{503, `{"status":503,"detail":"Keel does not know this server's public IP yet","code":"UNAVAILABLE"}`,
			output.CodeUnavailable, "Keel does not know this server's public IP yet", "", "", ""},
		{404, `{"status":404,"title":"Not Found","detail":"No such API route","code":"NOT_FOUND"}`,
			output.CodeNotFound, "No such API route", "", "", ""},
		{429, `{"status":429,"detail":"Too many requests. Please try again later.","code":"RATE_LIMITED"}`,
			output.CodeRateLimited, "Too many requests. Please try again later.", "Retry in 12s", "Retry-After", "12"},
		{429, `{"status":429,"detail":"Too many requests. Please try again later.","code":"RATE_LIMITED"}`,
			output.CodeRateLimited, "Too many requests. Please try again later.", "Wait a moment, then retry", "", ""},
		{500, `{"status":500,"title":"Internal Server Error","detail":"Something went wrong on the server","code":"SERVER_ERROR"}`,
			output.CodeServer, "Something went wrong on the server", "", "", ""},
		{409, `{"status":409,"detail":"Brand new","code":"SOMETHING_NEW"}`, "SOMETHING_NEW", "Brand new", "", "", ""},

		{400, `{"detail":"Node not found"}`, output.CodeServiceNotFound, "Node not found", "keel service list", "", ""},
		{400, `{"detail":"Project \"web\" already exists"}`, output.CodeNameTaken, `Project "web" already exists`, "Pick another name, or use it: keel link web", "", ""},
		{401, ``, output.CodeNotAuthenticated, "Session expired or signed out", "keel login " + url, "", ""},
		{429, `slow down`, output.CodeRateLimited, "Too Many Requests", "Wait a moment, then retry", "", ""},
		{502, `<html>Bad Gateway</html>`, output.CodeServer, "GET /api/projects: HTTP 502: <html>Bad Gateway</html>", "", "", ""},
	} {
		h := http.Header{}
		if tc.header != "" {
			h.Set(tc.header, tc.value)
		}
		c := &Client{URL: url}
		err := c.failure("GET /api/projects", &reply{status: tc.status, header: h, body: []byte(tc.body)})
		if err.Code != tc.code || err.Message != tc.msg || err.Fix != tc.fix {
			t.Errorf("%d %s:\n got %s %q fix %q\nwant %s %q fix %q", tc.status, tc.body, err.Code, err.Message, err.Fix, tc.code, tc.msg, tc.fix)
		}
	}
}

func TestTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	slow := &Client{URL: srv.URL, Token: "tok", HTTP: &http.Client{Timeout: 20 * time.Millisecond}}
	if _, err := slow.Projects(context.Background()); output.CodeOf(err) != output.CodeTimeout {
		t.Errorf("timeout: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(srv.URL, "tok").Projects(ctx); output.CodeOf(err) != output.CodeCancelled {
		t.Errorf("cancelled: %v", err)
	}
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	_, err := New(closed.URL, "tok").Projects(context.Background())
	if oe, ok := err.(*output.Error); !ok || oe.Code != output.CodeNetwork ||
		oe.Fix != "Check the URL, and that this machine is on the install's tailnet" {
		t.Errorf("refused: %#v", err)
	}
	if err := translate(fmt.Errorf("wrapped: %w", &output.Error{Code: output.CodeUsage}), ""); output.CodeOf(err) != output.CodeUsage {
		t.Errorf("an *output.Error passes through: %v", err)
	}
	if err := translate(errors.New("odd"), ""); output.CodeOf(err) != output.CodeServer {
		t.Errorf("anything else: %v", err)
	}
}
