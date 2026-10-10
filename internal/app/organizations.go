package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

// Organizations, members and invitations (docs/go/spec/auth-orgs.md §6–§7). One organization per
// install; every member sees the same resources; only owners and admins manage invitations.

// authMembershipChanged: someone joined (or an organization was founded). Members, invitations
// and every session's view of its organization (/api/me) change.
func authMembershipChanged(ch *Changes, org string) {
	ch.Organization(org)
	ch.Add(org, "/api/me")
}

// foundOrganization creates the install's organization with ownerID as its owner.
func foundOrganization(tx Tx, ownerID string, now int64) (domain.Organization, error) {
	org := domain.Organization{ID: domain.NewID(), Name: domain.DefaultOrganizationName, Slug: domain.DefaultOrganizationSlug, CreatedAt: now}
	if err := tx.AuthInsertOrganization(org); err != nil {
		return domain.Organization{}, err
	}
	err := tx.AuthInsertMember(domain.Member{ID: domain.NewID(), OrganizationID: org.ID, UserID: ownerID, Role: domain.RoleOwner, CreatedAt: now})
	return org, err
}

// joinOrFound is the actor's membership, founding the install's organization when there is none
// yet (convex/projects.ts joinOrFound). Returns the actor with OrganizationID/Role set.
// Errors: NOT_AUTHENTICATED; NO_ORGANIZATION when an organization exists and the actor is not in
// it. Called by: canvas (EnsureDefaultProject, CreateProject).
//
// It reads the membership inside the caller's write transaction (single writer), so two
// concurrent founders cannot make two organizations. When it founds one (the returned actor has
// an OrganizationID the given one did not), the caller should also publish
// ch.Organization(org) and ch.Add(org, "/api/me"); accounts made since the Go port are founded or
// joined at sign-up, so this only happens for accounts imported without a membership.
func joinOrFound(tx Tx, actor domain.Actor, now int64) (domain.Actor, error) {
	if !actor.SignedIn() {
		return actor, domain.ErrNotAuthenticated
	}
	m, err := tx.AuthMembership(actor.UserID)
	if err == nil {
		actor.OrganizationID, actor.Role = m.OrganizationID, m.Role
		return actor, nil
	}
	if !errors.Is(err, ErrNoRow) {
		return actor, err
	}
	exists, err := tx.AuthAnyOrganization()
	if err != nil {
		return actor, err
	}
	if exists {
		return actor, domain.ErrNoOrganization
	}
	org, err := foundOrganization(tx, actor.UserID, now)
	if err != nil {
		return actor, err
	}
	actor.OrganizationID, actor.Role = org.ID, domain.RoleOwner
	return actor, nil
}

// authFreshMember is the actor's membership as stored now. The actor's own fields were read at
// the start of the request; roles and memberships are re-read inside writes.
func authFreshMember(tx Tx, actor domain.Actor) (domain.Member, error) {
	if err := actor.RequireMember(); err != nil {
		return domain.Member{}, err
	}
	m, err := tx.AuthMembership(actor.UserID)
	if errors.Is(err, ErrNoRow) || (err == nil && m.OrganizationID != actor.OrganizationID) {
		return domain.Member{}, domain.ErrNoOrganization
	}
	return m, err
}

// ListMembers: every member of the caller's organization, in joining order.
func (a *App) ListMembers(ctx context.Context, actor domain.Actor) ([]MemberAccount, error) {
	if err := actor.RequireMember(); err != nil {
		return nil, err
	}
	var out []MemberAccount
	err := a.read(ctx, func(tx Tx) (err error) { out, err = tx.AuthMembers(actor.OrganizationID); return })
	return out, err
}

// ListInvitations: the caller's organization's standing invitations, oldest first. Any member
// may list them (Better Auth list-invitations only needs membership).
func (a *App) ListInvitations(ctx context.Context, actor domain.Actor) ([]domain.Invitation, error) {
	if err := actor.RequireMember(); err != nil {
		return nil, err
	}
	var out []domain.Invitation
	err := a.read(ctx, func(tx Tx) (err error) {
		out, err = tx.AuthPendingInvitations(actor.OrganizationID, a.Now())
		return
	})
	return out, err
}

// CreateInvitation invites email into the caller's organization (Better Auth
// organization/invite-member, auth-orgs.md §7.2). Inviting again cancels the previous standing
// invitation for that email. Nothing is mailed: the inviter copies the link.
func (a *App) CreateInvitation(ctx context.Context, actor domain.Actor, email, role string) (domain.Invitation, error) {
	if err := actor.RequireMember(); err != nil {
		return domain.Invitation{}, err
	}
	email = domain.NormalizeUserEmail(email)
	if !domain.ValidUserEmail(email) {
		return domain.Invitation{}, domain.Invalid(domain.MsgInvalidEmail)
	}
	var inv domain.Invitation
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		now := a.Now()
		m, err := authFreshMember(tx, actor)
		if err != nil {
			return err
		}
		grant, err := domain.InviteRole(m.Role, role)
		if err != nil {
			return err
		}
		member, err := tx.AuthIsMemberByEmail(m.OrganizationID, email)
		if err != nil {
			return err
		}
		if member {
			return domain.Conflict(domain.MsgAlreadyMember)
		}
		if err := tx.AuthCancelPendingInvitations(m.OrganizationID, email, now); err != nil {
			return err
		}
		n, err := tx.AuthCountPendingInvitations(m.OrganizationID, now)
		if err != nil {
			return err
		}
		if n >= domain.InvitationLimit {
			return domain.E(domain.CodeForbidden, domain.MsgInvitationLimit)
		}
		inv = domain.Invitation{
			// The id is the secret in the invite link: 128 bits, more than domain.NewID's 100.
			ID:             domain.NewSecret(16),
			OrganizationID: m.OrganizationID, Email: email, Role: grant, Status: domain.InvitationPending,
			InviterID: actor.UserID, ExpiresAt: now + domain.InvitationTTL, CreatedAt: now,
		}
		if err := tx.AuthInsertInvitation(inv); err != nil {
			return err
		}
		ch.Organization(m.OrganizationID)
		return nil
	})
	return inv, err
}

// CancelInvitation cancels a standing invitation of the caller's organization (owners and admins).
// Unknown and foreign ids are "Invitation not found"; an invitation that no longer stands
// (accepted, canceled, expired) is left as it is.
func (a *App) CancelInvitation(ctx context.Context, actor domain.Actor, id string) error {
	if err := actor.RequireMember(); err != nil {
		return err
	}
	return a.write(ctx, func(tx Tx, ch *Changes) error {
		inv, err := tx.AuthInvitation(id)
		if errors.Is(err, ErrNoRow) || (err == nil && inv.OrganizationID != actor.OrganizationID) {
			return domain.NotFound(domain.MsgInvitationNotFound)
		}
		if err != nil {
			return err
		}
		m, err := authFreshMember(tx, actor)
		if err != nil {
			return err
		}
		if !domain.CanManageInvitations(m.Role) {
			return domain.E(domain.CodeForbidden, domain.MsgNotAllowedToCancel)
		}
		if !inv.Standing(a.Now()) {
			return nil
		}
		if _, err := tx.AuthSetInvitationStatus(inv.ID, domain.InvitationPending, domain.InvitationCanceled); err != nil {
			return err
		}
		ch.Organization(inv.OrganizationID)
		return nil
	})
}

// PublicInvitation is what an invite link shows before sign-up.
type PublicInvitation struct {
	Email        string
	Organization string // the organization's name
}

// GetInvitation is the invite link's page data, or nil when the link is unknown, spent or expired
// (convex organizations.invitation). Public: the id is the secret in the link.
func (a *App) GetInvitation(ctx context.Context, id string) (*PublicInvitation, error) {
	var out *PublicInvitation
	err := a.read(ctx, func(tx Tx) error {
		inv, err := standingInvitation(tx, id, a.Now())
		if err != nil || inv == nil {
			return err
		}
		org, err := tx.AuthOrganization(inv.OrganizationID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		out = &PublicInvitation{Email: inv.Email, Organization: org.Name}
		return nil
	})
	return out, err
}

// AcceptInvitation joins the signed-in account to the invitation's organization (the intended
// behaviour of Better Auth's accept-invitation, which always failed in Keel on its email
// verification gate, auth-orgs.md §7.4). The unguessable id is the proof of possession.
func (a *App) AcceptInvitation(ctx context.Context, actor domain.Actor, id string) (MyOrganization, error) {
	if err := actor.RequireUser(); err != nil {
		return MyOrganization{}, err
	}
	var out MyOrganization
	err := a.write(ctx, func(tx Tx, ch *Changes) error {
		now := a.Now()
		inv, err := standingInvitation(tx, id, now)
		if err != nil {
			return err
		}
		if inv == nil {
			return domain.NotFound(domain.MsgInvitationNotFound)
		}
		if !domain.SameUserEmail(inv.Email, actor.Email) {
			return domain.E(domain.CodeForbidden, domain.MsgNotRecipient)
		}
		if _, err := tx.AuthMembership(actor.UserID); err == nil {
			return domain.Conflict(domain.MsgAlreadyInOrganization)
		} else if !errors.Is(err, ErrNoRow) {
			return err
		}
		org, err := tx.AuthOrganization(inv.OrganizationID)
		if errors.Is(err, ErrNoRow) {
			return domain.NotFound(domain.MsgInvitationNotFound)
		}
		if err != nil {
			return err
		}
		n, err := tx.AuthCountMembers(org.ID)
		if err != nil {
			return err
		}
		if n >= domain.MembershipLimit {
			return domain.E(domain.CodeForbidden, domain.MsgMembershipLimit)
		}
		if err := joinWithInvitation(tx, ch, *inv, actor.UserID, now); err != nil {
			return err
		}
		role := inv.Role
		if role == "" {
			role = domain.RoleMember
		}
		out = MyOrganization{ID: org.ID, Name: org.Name, Slug: org.Slug, Role: role}
		return nil
	})
	if err == nil && a.Conns != nil {
		a.Conns.DisconnectUser(actor.UserID) // reconnect onto the new organization's channel
	}
	return out, err
}
