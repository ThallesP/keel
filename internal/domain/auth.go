package domain

import "strings"

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

func KnownRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}

func HasRole(role, want string) bool {
	for _, r := range strings.Split(role, ",") {
		if strings.TrimSpace(r) == want {
			return true
		}
	}
	return false
}

func CanManageInvitations(role string) bool {
	return HasRole(role, RoleOwner) || HasRole(role, RoleAdmin)
}

type User struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt"`
}

type Session struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	ExpiresAt int64  `json:"expiresAt"`
	CreatedAt int64  `json:"createdAt"`
	UserAgent string `json:"userAgent,omitempty"`
	IP        string `json:"ip,omitempty"`
}

type Organization struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt int64  `json:"createdAt"`
}

const (
	DefaultOrganizationName = "Default"
	DefaultOrganizationSlug = "default"
)

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
	InvitationRejected InvitationStatus = "rejected"
)

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

func (i Invitation) Standing(now int64) bool {
	return i.Status == InvitationPending && i.ExpiresAt >= now
}

func InviteRole(inviterRole, role string) (string, error) {
	if !CanManageInvitations(inviterRole) {
		return "", E(CodeForbidden, MsgNotAllowedToInvite)
	}
	if role == "" {
		role = RoleMember
	}
	if !KnownRole(role) {
		return "", Invalid("%s: %s", MsgRoleNotFound, role)
	}
	if role == RoleOwner && !HasRole(inviterRole, RoleOwner) {
		return "", E(CodeForbidden, MsgNotAllowedToInviteWithRole)
	}
	return role, nil
}
