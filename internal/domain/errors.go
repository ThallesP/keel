package domain

import (
	"errors"
	"fmt"
)

// Error codes. They are the CLI's code vocabulary (docs/cli.md, "Contract"): fields and codes are
// only ever added. The transport maps each to an HTTP status (docs/go/ARCHITECTURE.md, "Errors").
const (
	CodeNotAuthenticated     = "NOT_AUTHENTICATED"
	CodeNoOrganization       = "NO_ORGANIZATION"
	CodeForbidden            = "FORBIDDEN"
	CodeNotFound             = "NOT_FOUND"
	CodeProjectNotFound      = "PROJECT_NOT_FOUND"
	CodeServiceNotFound      = "SERVICE_NOT_FOUND"
	CodeVariableNotFound     = "VARIABLE_NOT_FOUND"
	CodeDeploymentNotFound   = "DEPLOYMENT_NOT_FOUND"
	CodeNameTaken            = "NAME_TAKEN"
	CodeNothingToShip        = "NOTHING_TO_SHIP"
	CodeDeploymentRunning    = "DEPLOYMENT_RUNNING"
	CodeConflict             = "CONFLICT"
	CodeTracesOff            = "TRACES_OFF"
	CodeAuthorizationPending = "AUTHORIZATION_PENDING"
	CodeInvalidInput         = "INVALID_INPUT"
	CodeUnavailable          = "UNAVAILABLE"
	CodeServerError          = "SERVER_ERROR"
)

// Error is a failure the caller should see: a code to branch on and a sentence to show. Messages
// are kept identical to the Convex ConvexError messages they replace.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// E builds an *Error. Use the helpers below for the common codes.
func E(code, format string, args ...any) *Error {
	if len(args) == 0 {
		return &Error{Code: code, Message: format}
	}
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Invalid(format string, args ...any) *Error  { return E(CodeInvalidInput, format, args...) }
func NotFound(format string, args ...any) *Error { return E(CodeNotFound, format, args...) }
func Conflict(format string, args ...any) *Error { return E(CodeConflict, format, args...) }

// Messages the dashboard and CLI already know, from convex/access.ts.
const (
	MsgNotAuthenticated    = "Not authenticated"
	MsgNoOrganization      = "You're not in an organization yet. Ask a member for an invite link."
	MsgEnvironmentNotFound = "Environment not found"
	MsgNodeNotFound        = "Node not found"
)

var (
	ErrNotAuthenticated = &Error{Code: CodeNotAuthenticated, Message: MsgNotAuthenticated}
	ErrNoOrganization   = &Error{Code: CodeNoOrganization, Message: MsgNoOrganization}
)

// CodeOf is err's code, or SERVER_ERROR for anything that is not a *Error.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeServerError
}
