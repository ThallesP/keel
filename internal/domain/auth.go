package domain

import (
	"cmp"
	"slices"
)

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

func CanManageInvitations(role string) bool { return role == RoleOwner || role == RoleAdmin }

type User struct {
	ID        string
	Email     string
	Name      string
	CreatedAt int64
}

type Session struct {
	ID        string
	UserID    string
	ExpiresAt int64
	CreatedAt int64
	UserAgent string
	IP        string
}

type Organization struct {
	ID        string
	Name      string
	Slug      string
	CreatedAt int64
}

const (
	DefaultOrganizationName = "Default"
	DefaultOrganizationSlug = "default"
)

type Member struct {
	ID             string
	OrganizationID string
	UserID         string
	Role           string
	CreatedAt      int64
}

type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationAccepted InvitationStatus = "accepted"
	InvitationCanceled InvitationStatus = "canceled"
)

type Invitation struct {
	ID             string
	OrganizationID string
	Email          string
	Role           string
	Status         InvitationStatus
	InviterID      string
	ExpiresAt      int64
	CreatedAt      int64
}

func (i Invitation) Standing(now int64) bool {
	return i.Status == InvitationPending && i.ExpiresAt >= now
}

func InviteRole(inviterRole, role string) (string, error) {
	if !CanManageInvitations(inviterRole) {
		return "", E(CodeForbidden, "You are not allowed to invite users to this organization")
	}
	role = cmp.Or(role, RoleMember)
	if !slices.Contains([]string{RoleOwner, RoleAdmin, RoleMember}, role) {
		return "", Invalid("Role not found: %s", role)
	}
	if role == RoleOwner && inviterRole != RoleOwner {
		return "", E(CodeForbidden, "You are not allowed to invite a user with this role")
	}
	return role, nil
}
