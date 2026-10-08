package domain

import "strings"

// Roles of an organization member.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// KnownRole: one of the three roles Better Auth's organization plugin defines.
func KnownRole(role string) bool {
	return role == RoleOwner || role == RoleAdmin || role == RoleMember
}

// HasRole: role (a single role, or Better Auth's comma list) includes want.
func HasRole(role, want string) bool {
	for _, r := range strings.Split(role, ",") {
		if strings.TrimSpace(r) == want {
			return true
		}
	}
	return false
}

// CanManageInvitations: Better Auth's invitation:create / invitation:cancel permission, which
// only owners and admins have. No Keel resource looks at the role (auth-orgs.md §9.3).
func CanManageInvitations(role string) bool {
	return HasRole(role, RoleOwner) || HasRole(role, RoleAdmin)
}

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
	IP        string `json:"ip,omitempty"` // auth area: client address at creation
}

// Organization: one per install for now, founded by the first account.
type Organization struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	CreatedAt int64  `json:"createdAt"`
}

// The install's organization, as joinOrFound founds it (convex/projects.ts ORGANIZATION).
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
	InvitationRejected InvitationStatus = "rejected" // auth area: Better Auth's reject endpoint (imported rows)
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

// Standing: pending and not expired (convex/auth.ts pendingInvitation: expiresAt >= now).
func (i Invitation) Standing(now int64) bool {
	return i.Status == InvitationPending && i.ExpiresAt >= now
}

// InviteRole is the role an invitation will grant, after Better Auth's invite-member checks on
// the inviter (steps 4 and 5 of auth-orgs.md §7.2): only owners and admins invite, the role must
// be known, and only an owner may invite an owner. "" means member.
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
