package domain

// Roles of an organization member.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

type User struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
}

// Session: Token is the bearer secret (cookie keel_session or Authorization: Bearer). Only its
// hash is stored.
type Session struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	ExpiresAt int64  `json:"expiresAt"`
	CreatedAt int64  `json:"createdAt"`
	UserAgent string `json:"userAgent,omitempty"`
}

// Organization: one per install for now, founded by the first account.
type Organization struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt int64  `json:"createdAt"`
}

type Member struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organizationId"`
	UserID         string `json:"userId"`
	Role           string `json:"role"`
	CreatedAt      int64  `json:"createdAt"`
}

type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationCanceled InvitationStatus = "canceled"
)

// Invitation: the id is the secret in the invite link.
type Invitation struct {
	ID             string           `json:"id"`
	OrganizationID string           `json:"organizationId"`
	Email          string           `json:"email"`
	Role           string           `json:"role"`
	Status         InvitationStatus `json:"status"`
	InviterID      string           `json:"inviterId"`
	ExpiresAt      int64            `json:"expiresAt"`
	CreatedAt      int64            `json:"createdAt"`
}
