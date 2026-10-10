package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

func authMembershipChanged(ch *Changes, org string) {
	ch.Organization(org)
	ch.Add(org, "/api/me")
}

func foundOrganization(tx Tx, ownerID string, now int64) (domain.Organization, error) {
	org := domain.Organization{ID: domain.NewID(), Name: domain.DefaultOrganizationName, Slug: domain.DefaultOrganizationSlug, CreatedAt: now}
	if err := tx.AuthInsertOrganization(org); err != nil {
		return domain.Organization{}, err
	}
	err := tx.AuthInsertMember(domain.Member{ID: domain.NewID(), OrganizationID: org.ID, UserID: ownerID, Role: domain.RoleOwner, CreatedAt: now})
	return org, err
}

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

func (a *App) ListMembers(ctx context.Context, actor domain.Actor) ([]MemberAccount, error) {
	if err := actor.RequireMember(); err != nil {
		return nil, err
	}
	var out []MemberAccount
	err := a.read(ctx, func(tx Tx) (err error) { out, err = tx.AuthMembers(actor.OrganizationID); return })
	return out, err
}

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

type PublicInvitation struct {
	Email        string
	Organization string
}

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
		a.Conns.DisconnectUser(actor.UserID)
	}
	return out, err
}
