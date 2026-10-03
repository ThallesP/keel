// Package output is the CLI's one contract with whoever reads it, a person or an agent.
//
// stdout carries results only: text or a table for people, exactly one JSON object with --json
// (or KEEL_JSON=1, the same switch install.sh reads). Streams (logs --follow) are one JSON object
// per line. Progress and warnings always go to stderr. Errors are {"ok":false,"code","error","fix"}
// on stdout in JSON mode, `error:` / `fix:` lines on stderr otherwise, and set the exit code.
// Fields are only ever added, never renamed or removed.
package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/term"
)

// Error codes. Stable: agents branch on them.
const (
	CodeUsage            = "USAGE"
	CodeNotAuthenticated = "NOT_AUTHENTICATED"
	// keel login is waiting for someone to approve its link in the dashboard.
	CodeAuthorizationPending = "AUTHORIZATION_PENDING"
	CodeNoOrganization       = "NO_ORGANIZATION"
	CodeNoProjects           = "NO_PROJECTS"
	CodeProjectRequired      = "PROJECT_REQUIRED"
	CodeProjectNotFound      = "PROJECT_NOT_FOUND"
	CodeServiceNotFound      = "SERVICE_NOT_FOUND"
	CodeVariableNotFound     = "VARIABLE_NOT_FOUND"
	CodeDeploymentNotFound   = "DEPLOYMENT_NOT_FOUND"
	CodeDeploymentRunning    = "DEPLOYMENT_RUNNING"
	CodeDeploymentFailed     = "DEPLOYMENT_FAILED"
	CodeNothingToShip        = "NOTHING_TO_SHIP"
	CodeNameTaken            = "NAME_TAKEN" // a project slug or service name already in use
	CodeInvalidInput         = "INVALID_INPUT"
	CodeDiscoveryFailed      = "DISCOVERY_FAILED"
	CodeNetwork              = "NETWORK_ERROR"
	CodeServer               = "SERVER_ERROR"
	CodeConfig               = "CONFIG_ERROR"
	CodeTimeout              = "TIMEOUT"
	CodeCancelled            = "CANCELLED"
)

// Exit codes: 0 ok, 1 error, 2 usage, 4 needs login (as gh), 130 interrupted.
const (
	ExitError     = 1
	ExitUsage     = 2
	ExitAuth      = 4
	ExitCancelled = 130
)

// Error is every failure the CLI reports. Message says what happened, Fix the next command to run.
type Error struct {
	Code    string
	Message string
	Fix     string
	// Extra fields for the JSON error object, e.g. the failed deployment.
	Extra map[string]any
}

func (e *Error) Error() string { return e.Message }

func (e *Error) ExitCode() int {
	switch e.Code {
	case CodeUsage:
		return ExitUsage
	case CodeNotAuthenticated, CodeAuthorizationPending:
		return ExitAuth
	case CodeCancelled:
		return ExitCancelled
	}
	return ExitError
}

// CodeOf is the code of err if it is (or wraps) an *Error, "" otherwise.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func Errorf(code, fix, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...), Fix: fix}
}

type Printer struct {
	JSON bool
	Out  io.Writer
	Err  io.Writer
}

func New(json bool) *Printer {
	return &Printer{JSON: json, Out: os.Stdout, Err: os.Stderr}
}

// Result prints a command's result: v as {"ok":true,...} in JSON mode (v must encode to an
// object), human(stdout) otherwise.
func (p *Printer) Result(v any, human func(w io.Writer)) {
	if p.JSON {
		p.Out.Write(withOK(true, encode(v)))
		return
	}
	human(p.Out)
}

// Event prints one item of a stream: a JSON line, or the human line as is.
func (p *Printer) Event(v any, human string) {
	if p.JSON {
		p.Out.Write(encode(v))
		return
	}
	fmt.Fprintln(p.Out, human)
}

// Progress is for people watching; it never reaches stdout.
func (p *Printer) Progress(format string, args ...any) {
	fmt.Fprintf(p.Err, format+"\n", args...)
}

func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.Err, "warning: "+format+"\n", args...)
}

// Fail reports err and returns the exit code.
func (p *Printer) Fail(e *Error) int {
	fmt.Fprintf(p.Err, "error: %s\n", e.Message)
	if e.Fix != "" {
		fmt.Fprintf(p.Err, "fix:   %s\n", e.Fix)
	}
	if p.JSON {
		body := map[string]any{"code": e.Code, "error": e.Message, "fix": e.Fix}
		for k, v := range e.Extra {
			body[k] = v
		}
		p.Out.Write(withOK(false, encode(body)))
	}
	return e.ExitCode()
}

// IsTerminal reports whether f is an interactive terminal. Nothing prompts unless stdin is one.
func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// encode is json.Marshal plus a newline, without HTML escaping: log lines keep their `<` and `&`.
func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("output: encode %T: %v", v, err))
	}
	return buf.Bytes()
}

// withOK puts "ok" first in an encoded object, so results stay plain structs.
func withOK(ok bool, obj []byte) []byte {
	head := []byte(fmt.Sprintf(`{"ok":%t`, ok))
	rest := bytes.TrimSpace(obj)[1:] // drop "{"
	if rest[0] != '}' {
		head = append(head, ',')
	}
	return append(append(head, rest...), '\n')
}
