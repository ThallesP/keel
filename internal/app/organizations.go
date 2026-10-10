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

func foundOrganization(tx Tx, ch *Changes, ownerID string, now int64) error {
	org := domain.Organization{ID: domain.NewID(), Name: "Default", Slug: "default", CreatedAt: now}
	if err := tx.AuthInsertOrganization(org); err != nil {
		return err
	}
	err := tx.AuthInsertMember(domain.Member{ID: domain.NewID(), OrganizationID: org.ID, UserID: ownerID, Role: domain.RoleOwner, CreatedAt: now})
	if err != nil {
		return err
	}
	authMembershipChanged(ch, org.ID)
	return nil
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

func (a *App) CreateInvitation(ctx context.Context, actor domain.Actor, email string, role domain.Role) (domain.Invitation, error) {
	if err := actor.RequireMember(); err != nil {
		return domain.Invitation{}, err
	}
	email = domain.NormalizeUserEmail(email)
	if !domain.ValidUserEmail(email) {
		return domain.Invitation{}, domain.Invalid(domain.MsgInvalidEmail)
	}
	grant, err := domain.InviteRole(actor.Role, role)
	if err != nil {
		return domain.Invitation{}, err
	}
	var inv domain.Invitation
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		now := a.Now()
		member, err := tx.AuthIsMemberByEmail(actor.OrganizationID, email)
		if err != nil {
			return err
		}
		if member {
			return domain.Conflict("User is already a member of this organization")
		}
		if err := tx.AuthCancelPendingInvitations(actor.OrganizationID, email, now); err != nil {
			return err
		}
		n, err := tx.AuthCountPendingInvitations(actor.OrganizationID, now)
		if err != nil {
			return err
		}
		if n >= domain.InvitationLimit {
			return domain.E(domain.CodeForbidden, "Invitation limit reached")
		}
		inv = domain.Invitation{
			ID:             domain.NewSecret(16),
			OrganizationID: actor.OrganizationID, Email: email, Role: grant, Status: domain.InvitationPending,
			InviterID: actor.UserID, ExpiresAt: now + domain.InvitationTTL, CreatedAt: now,
		}
		if err := tx.AuthInsertInvitation(inv); err != nil {
			return err
		}
		ch.Organization(actor.OrganizationID)
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
		if !actor.Role.CanManageInvitations() {
			return domain.E(domain.CodeForbidden, "You are not allowed to cancel this invitation")
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
		if inv.Email != actor.Email {
			return domain.E(domain.CodeForbidden, "You are not the recipient of the invitation")
		}
		_, err = tx.AuthMembership(actor.UserID)
		if err == nil {
			return domain.Conflict("You're already in an organization")
		}
		if !errors.Is(err, ErrNoRow) {
			return err
		}
		org, err := tx.AuthOrganization(inv.OrganizationID)
		if err != nil {
			return err
		}
		n, err := tx.AuthCountMembers(org.ID)
		if err != nil {
			return err
		}
		if n >= domain.MembershipLimit {
			return domain.E(domain.CodeForbidden, "Organization membership limit reached")
		}
		if err := joinWithInvitation(tx, ch, *inv, actor.UserID, now); err != nil {
			return err
		}
		out = MyOrganization{ID: org.ID, Name: org.Name, Slug: org.Slug, Role: inv.Role}
		return nil
	})
	if err == nil {
		a.Conns.DisconnectUser(actor.UserID)
	}
	return out, err
}
