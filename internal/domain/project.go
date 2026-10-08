package domain

// Project belongs to one organization and always has a production environment.
type Project struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	CreatedAt      int64  `json:"createdAt"`
}

type Environment struct {
	ID           string `json:"id"`
	ProjectID    string `json:"projectId"`
	Name         string `json:"name"`
	IsProduction bool   `json:"isProduction"`
	CreatedAt    int64  `json:"createdAt"`
}
