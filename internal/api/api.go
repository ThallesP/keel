// Package api holds the wire types of Keel's HTTP API: request and response bodies and the error
// shape. The server (transport/http) and the CLI (cli) both use them, so a field means the same
// thing on both ends. Imports only stdlib and domain (for small view constructors). JSON names are camelCase; times are unix milliseconds.
package api

// Problem is every error response: RFC 9457 problem details plus `code`, the stable code callers
// branch on (domain.Code*, the CLI's code vocabulary).
type Problem struct {
	Type   string        `json:"type,omitempty" doc:"A URI reference identifying the problem type" default:"about:blank"`
	Title  string        `json:"title,omitempty" doc:"Short summary of the problem type" example:"Not Found"`
	Status int           `json:"status,omitempty" doc:"HTTP status code" example:"404"`
	Detail string        `json:"detail,omitempty" doc:"The human sentence to show" example:"Node not found"`
	Code   string        `json:"code" doc:"Stable error code" example:"SERVICE_NOT_FOUND"`
	Errors []ErrorDetail `json:"errors,omitempty" doc:"Validation details"`
}

// ErrorDetail is one input validation failure.
type ErrorDetail struct {
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty" example:"body.name"`
	Value    any    `json:"value,omitempty"`
}

func (p *Problem) Error() string {
	if p.Detail != "" {
		return p.Detail
	}
	return p.Title
}

func (p *Problem) GetStatus() int { return p.Status }

// ContentType: problems are application/problem+json.
func (p *Problem) ContentType(string) string { return "application/problem+json" }

// Meta is GET /api/meta: what the CLI discovers an install with.
type Meta struct {
	Name    string `json:"name" example:"keel"`
	Version string `json:"version"`
	SiteURL string `json:"siteUrl" doc:"Dashboard URL as users open it"`
}
