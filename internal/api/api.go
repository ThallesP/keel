package api

import "cmp"

type Problem struct {
	Type   string        `json:"type,omitempty" doc:"A URI reference identifying the problem type" default:"about:blank"`
	Title  string        `json:"title,omitempty" doc:"Short summary of the problem type" example:"Not Found"`
	Status int           `json:"status,omitempty" doc:"HTTP status code" example:"404"`
	Detail string        `json:"detail,omitempty" doc:"The human sentence to show" example:"Node not found"`
	Code   string        `json:"code" doc:"Stable error code" example:"SERVICE_NOT_FOUND"`
	Errors []ErrorDetail `json:"errors,omitempty" doc:"Validation details"`
}

type ErrorDetail struct {
	Message  string `json:"message,omitempty"`
	Location string `json:"location,omitempty" example:"body.name"`
	Value    any    `json:"value,omitempty"`
}

func (p *Problem) Error() string { return cmp.Or(p.Detail, p.Title) }

func (p *Problem) GetStatus() int { return p.Status }

func (p *Problem) ContentType(string) string { return "application/problem+json" }

type Meta struct {
	Name    string `json:"name" example:"keel"`
	Version string `json:"version"`
	SiteURL string `json:"siteUrl" doc:"Dashboard URL as users open it"`
}
