package domain

type Project struct {
	ID             string
	OrganizationID string
	Name           string
	Slug           string
	CreatedAt      int64
}

type Environment struct {
	ID           string
	ProjectID    string
	Name         string
	IsProduction bool
	CreatedAt    int64
}
