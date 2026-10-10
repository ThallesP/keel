package app

import (
	"context"
	"errors"

	"github.com/ThallesP/keel/internal/domain"
)

type ClientInfo struct {
	IP        string
	UserAgent string
}

type SignedIn struct {
	Token   string
	User    domain.User
	Session domain.Session
}

type SignUpInput struct {
	Email        string
	Password     string
	Name         string
	InvitationID string
	Client       ClientInfo
}

type MyOrganization struct {
	ID   string
	Name string
	Slug string
	Role domain.Role
}

type Me struct {
	User         *domain.User
	Organization *MyOrganization
}

func (a *App) ResolveSession(ctx context.Context, token string) (domain.Actor, error) {
	if token == "" {
		return domain.Actor{}, nil
	}
	var actor domain.Actor
	err := a.read(ctx, func(tx Tx) error {
		s, err := tx.AuthSession(domain.HashSecret(token))
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		u, err := tx.AuthUser(s.UserID)
		if err != nil {
			return err
		}
		actor = domain.Actor{UserID: u.ID, Email: u.Email, Name: u.Name, SessionID: s.ID, SessionExpiresAt: s.ExpiresAt}
		m, err := tx.AuthMembership(u.ID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		actor.OrganizationID, actor.Role = m.OrganizationID, m.Role
		return nil
	})
	if err != nil || !actor.SignedIn() {
		return domain.Actor{}, err
	}
	now := a.Now()
	if domain.SessionExpired(actor.SessionExpiresAt, now) {
		if err := a.SignOut(ctx, actor); err != nil {
			a.Log.Warn("delete expired session", "err", err)
		}
		return domain.Actor{}, nil
	}
	if !domain.SessionNeedsRenewal(actor.SessionExpiresAt, now) {
		return actor, nil
	}
	expires := now + domain.SessionTTL
	if err := a.write(ctx, func(tx Tx, _ *Changes) error { return tx.AuthExtendSession(actor.SessionID, expires, now) }); err != nil {
		a.Log.Warn("renew session", "err", err)
		return actor, nil
	}
	actor.SessionExpiresAt, actor.SessionRenewed = expires, true
	return actor, nil
}

func (a *App) SignUpOpen(ctx context.Context) (bool, error) {
	var someone bool
	err := a.read(ctx, func(tx Tx) (err error) { someone, err = tx.AuthAnyUser(); return })
	return !someone, err
}

func standingInvitation(tx Tx, id string, now int64) (*domain.Invitation, error) {
	inv, err := tx.AuthInvitation(id)
	if errors.Is(err, ErrNoRow) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !inv.Standing(now) {
		return nil, nil
	}
	return &inv, nil
}

func signUpRule(tx Tx, email, invitationID string, now int64) (*domain.Invitation, error) {
	_, err := tx.AuthCredentials(email)
	if err == nil {
		return nil, domain.Conflict("User already exists. Use another email.")
	}
	if !errors.Is(err, ErrNoRow) {
		return nil, err
	}
	someone, err := tx.AuthAnyUser()
	if err != nil {
		return nil, err
	}
	inv, err := standingInvitation(tx, invitationID, now)
	if err != nil {
		return nil, err
	}
	if inv != nil && inv.Email != email {
		inv = nil
	}
	if someone && inv == nil {
		return nil, domain.E(domain.CodeForbidden, "Sign-up is by invitation. Ask a member for an invite link.")
	}
	return inv, nil
}

func (a *App) SignUp(ctx context.Context, in SignUpInput) (SignedIn, error) {
	email := domain.NormalizeUserEmail(in.Email)
	if !domain.ValidUserEmail(email) {
		return SignedIn{}, domain.Invalid(domain.MsgInvalidEmail)
	}
	if err := domain.ValidPassword(in.Password); err != nil {
		return SignedIn{}, err
	}
	if err := a.limited(a.limits().perIP, in.Client.IP); err != nil {
		return SignedIn{}, err
	}
	err := a.read(ctx, func(tx Tx) error {
		_, err := signUpRule(tx, email, in.InvitationID, a.Now())
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	hash := a.Passwords.Hash(in.Password)
	var out SignedIn
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		now := a.Now()
		inv, err := signUpRule(tx, email, in.InvitationID, now)
		if err != nil {
			return err
		}
		user := domain.User{ID: domain.NewID(), Email: email, Name: in.Name, CreatedAt: now}
		if err := tx.AuthInsertUser(user, hash); err != nil {
			return err
		}
		if inv == nil {
			err = foundOrganization(tx, ch, user.ID, now)
		} else {
			err = joinWithInvitation(tx, ch, *inv, user.ID, now)
		}
		if err != nil {
			return err
		}
		out, err = issueSession(tx, user, now, in.Client)
		return err
	})
	return out, err
}

func joinWithInvitation(tx Tx, ch *Changes, inv domain.Invitation, userID string, now int64) error {
	ok, err := tx.AuthSetInvitationStatus(inv.ID, domain.InvitationPending, domain.InvitationAccepted)
	if err != nil {
		return err
	}
	if !ok {
		return domain.NotFound(domain.MsgInvitationNotFound)
	}
	err = tx.AuthInsertMember(domain.Member{ID: domain.NewID(), OrganizationID: inv.OrganizationID, UserID: userID, Role: inv.Role, CreatedAt: now})
	if err != nil {
		return err
	}
	authMembershipChanged(ch, inv.OrganizationID)
	return nil
}

func issueSession(tx Tx, user domain.User, now int64, client ClientInfo) (SignedIn, error) {
	if err := tx.AuthDeleteExpiredSessions(now); err != nil {
		return SignedIn{}, err
	}
	token := domain.NewSecret(32)
	s := domain.Session{
		ID: domain.NewID(), UserID: user.ID, ExpiresAt: now + domain.SessionTTL, CreatedAt: now,
		UserAgent: client.UserAgent[:min(len(client.UserAgent), 512)], IP: client.IP,
	}
	if err := tx.AuthInsertSession(s, domain.HashSecret(token)); err != nil {
		return SignedIn{}, err
	}
	return SignedIn{Token: token, User: user, Session: s}, nil
}

func (a *App) SignIn(ctx context.Context, email, password string, client ClientInfo) (SignedIn, error) {
	email = domain.NormalizeUserEmail(email)
	if !domain.ValidUserEmail(email) {
		return SignedIn{}, domain.Invalid(domain.MsgInvalidEmail)
	}
	if err := a.limited(a.limits().perIP, client.IP); err != nil {
		return SignedIn{}, err
	}
	key := client.IP + "\x00" + email
	limiter := a.limits().signIn
	if err := a.limited(limiter, key); err != nil {
		return SignedIn{}, err
	}
	var cred Credentials
	err := a.read(ctx, func(tx Tx) (err error) {
		cred, err = tx.AuthCredentials(email)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	if !a.Passwords.Verify(cred.PasswordHash, password) {
		return SignedIn{}, domain.E(domain.CodeNotAuthenticated, "Invalid email or password")
	}
	var out SignedIn
	err = a.write(ctx, func(tx Tx, _ *Changes) (err error) {
		out, err = issueSession(tx, cred.User, a.Now(), client)
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	limiter.reset(key)
	return out, nil
}

func (a *App) SignOut(ctx context.Context, actor domain.Actor) error {
	if actor.SessionID == "" {
		return nil
	}
	err := a.write(ctx, func(tx Tx, _ *Changes) error { return tx.AuthDeleteSession(actor.SessionID) })
	if err == nil && a.Conns != nil {
		a.Conns.DisconnectSession(actor.SessionID)
	}
	return err
}

func (a *App) GetMe(ctx context.Context, actor domain.Actor) (Me, error) {
	if !actor.SignedIn() {
		return Me{}, nil
	}
	me := Me{User: &domain.User{ID: actor.UserID, Email: actor.Email, Name: actor.Name}}
	if actor.OrganizationID == "" {
		return me, nil
	}
	var org domain.Organization
	err := a.read(ctx, func(tx Tx) (err error) { org, err = tx.AuthOrganization(actor.OrganizationID); return })
	if err != nil {
		return Me{}, err
	}
	me.Organization = &MyOrganization{ID: org.ID, Name: org.Name, Slug: org.Slug, Role: actor.Role}
	return me, nil
}
