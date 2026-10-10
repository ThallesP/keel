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

const (
	CodeUsage                = "USAGE"
	CodeNotAuthenticated     = "NOT_AUTHENTICATED"
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
	CodeNameTaken            = "NAME_TAKEN"
	CodeTracesOff            = "TRACES_OFF"
	CodeInvalidInput         = "INVALID_INPUT"
	CodeDiscoveryFailed      = "DISCOVERY_FAILED"
	CodeNetwork              = "NETWORK_ERROR"
	CodeServer               = "SERVER_ERROR"
	CodeConfig               = "CONFIG_ERROR"
	CodeTimeout              = "TIMEOUT"
	CodeCancelled            = "CANCELLED"
	CodeConflict             = "CONFLICT"
	CodeUnavailable          = "UNAVAILABLE"
	CodeForbidden            = "FORBIDDEN"
	CodeNotFound             = "NOT_FOUND"
	CodeRateLimited          = "RATE_LIMITED"
)

const (
	ExitError     = 1
	ExitUsage     = 2
	ExitAuth      = 4
	ExitCancelled = 130
)

type Error struct {
	Code    string
	Message string
	Fix     string
	Extra   map[string]any
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

func (p *Printer) Result(v any, human func(w io.Writer)) {
	if p.JSON {
		p.Out.Write(withOK(true, encode(v)))
		return
	}
	human(p.Out)
}

func (p *Printer) Event(v any, human string) {
	if p.JSON {
		p.Out.Write(encode(v))
		return
	}
	fmt.Fprintln(p.Out, human)
}

func (p *Printer) Progress(format string, args ...any) {
	fmt.Fprintf(p.Err, format+"\n", args...)
}

func (p *Printer) Warn(format string, args ...any) {
	fmt.Fprintf(p.Err, "warning: "+format+"\n", args...)
}

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

func IsTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func encode(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(fmt.Sprintf("output: encode %T: %v", v, err))
	}
	return buf.Bytes()
}

func withOK(ok bool, obj []byte) []byte {
	head := []byte(fmt.Sprintf(`{"ok":%t`, ok))
	rest := bytes.TrimSpace(obj)[1:]
	if rest[0] != '}' {
		head = append(head, ',')
	}
	return append(append(head, rest...), '\n')
}
