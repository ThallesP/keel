package domain

import (
	"errors"
	"fmt"
)

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

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func E(code, format string, args ...any) *Error {
	if len(args) == 0 {
		return &Error{Code: code, Message: format}
	}
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func Invalid(format string, args ...any) *Error  { return E(CodeInvalidInput, format, args...) }
func NotFound(format string, args ...any) *Error { return E(CodeNotFound, format, args...) }
func Conflict(format string, args ...any) *Error { return E(CodeConflict, format, args...) }

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

func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeServerError
}
