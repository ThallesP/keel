package app

import (
	"context"
	"errors"
	"strings"

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
	Role string
}

type Me struct {
	User         *domain.User
	Organization *MyOrganization
}

var errPasswordsMissing = errors.New("app: Passwords is not set")

func authClip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (a *App) ResolveSession(ctx context.Context, token string) (domain.Actor, error) {
	if token == "" {
		return domain.Actor{}, nil
	}
	hash := domain.HashSecret(token)
	now := a.Now()
	var (
		found  bool
		s      domain.Session
		u      domain.User
		member domain.Member
		inOrg  bool
	)
	err := a.read(ctx, func(tx Tx) error {
		var err error
		if s, err = tx.AuthSession(hash); err != nil {
			return authMissing(err)
		}
		if u, err = tx.AuthUser(s.UserID); err != nil {
			return authMissing(err)
		}
		found = true
		member, err = tx.AuthMembership(u.ID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		inOrg = err == nil
		return err
	})
	if err != nil || !found {
		return domain.Actor{}, err
	}
	if domain.SessionExpired(s.ExpiresAt, now) {
		if err := a.write(ctx, func(tx Tx, _ *Changes) error { return tx.AuthDeleteSession(s.ID) }); err != nil {
			a.Log.Warn("delete expired session", "err", err)
		} else if a.Conns != nil {
			a.Conns.DisconnectSession(s.ID)
		}
		return domain.Actor{}, nil
	}
	actor := domain.Actor{UserID: u.ID, Email: u.Email, Name: u.Name, SessionID: s.ID, SessionExpiresAt: s.ExpiresAt}
	if inOrg {
		actor.OrganizationID, actor.Role = member.OrganizationID, member.Role
	}
	if domain.SessionNeedsRenewal(s.ExpiresAt, now) {
		expires := now + domain.SessionTTL
		err := a.write(ctx, func(tx Tx, _ *Changes) error { return tx.AuthExtendSession(s.ID, expires, now) })
		if err != nil {
			a.Log.Warn("renew session", "err", err)
		} else {
			actor.SessionExpiresAt, actor.SessionRenewed = expires, true
		}
	}
	return actor, nil
}

func authMissing(err error) error {
	if errors.Is(err, ErrNoRow) {
		return nil
	}
	return err
}

func (a *App) SignUpOpen(ctx context.Context) (bool, error) {
	var someone bool
	err := a.read(ctx, func(tx Tx) (err error) { someone, err = tx.AuthAnyUser(); return })
	return !someone, err
}

func standingInvitation(tx Tx, id string, now int64) (*domain.Invitation, error) {
	if id == "" {
		return nil, nil
	}
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

func signUpRule(tx Tx, email, invitationID string, now int64) (*domain.Invitation, bool, error) {
	if _, err := tx.AuthCredentials(email); err == nil {
		return nil, false, domain.Conflict(domain.MsgUserExists)
	} else if !errors.Is(err, ErrNoRow) {
		return nil, false, err
	}
	someone, err := tx.AuthAnyUser()
	if err != nil {
		return nil, false, err
	}
	inv, err := standingInvitation(tx, invitationID, now)
	if err != nil {
		return nil, false, err
	}
	if inv != nil && !domain.SameUserEmail(inv.Email, email) {
		inv = nil
	}
	if someone && inv == nil {
		return nil, false, domain.E(domain.CodeForbidden, domain.MsgSignUpByInvitation)
	}
	return inv, !someone, nil
}

func (a *App) SignUp(ctx context.Context, in SignUpInput) (SignedIn, error) {
	email := strings.TrimSpace(in.Email)
	if !domain.ValidUserEmail(email) {
		return SignedIn{}, domain.Invalid(domain.MsgInvalidEmail)
	}
	if err := domain.ValidPassword(in.Password); err != nil {
		return SignedIn{}, err
	}
	if a.Passwords == nil {
		return SignedIn{}, errPasswordsMissing
	}
	if err := a.limited(a.limits().perIP, in.Client.IP); err != nil {
		return SignedIn{}, err
	}
	email = domain.NormalizeUserEmail(email)
	err := a.read(ctx, func(tx Tx) error {
		_, _, err := signUpRule(tx, email, in.InvitationID, a.Now())
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	hash, err := a.Passwords.Hash(in.Password)
	if err != nil {
		return SignedIn{}, err
	}
	var out SignedIn
	err = a.write(ctx, func(tx Tx, ch *Changes) error {
		now := a.Now()
		inv, first, err := signUpRule(tx, email, in.InvitationID, now)
		if err != nil {
			return err
		}
		user := domain.User{ID: domain.NewID(), Email: email, Name: in.Name, CreatedAt: now}
		if err := tx.AuthInsertUser(user, hash); err != nil {
			return err
		}
		switch {
		case inv != nil:
			if err := joinWithInvitation(tx, ch, *inv, user.ID, now); err != nil {
				return err
			}
		case first:
			org, err := foundOrganization(tx, user.ID, now)
			if err != nil {
				return err
			}
			authMembershipChanged(ch, org.ID)
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
	token := domain.NewSecret(domain.SessionTokenBytes)
	s := domain.Session{
		ID: domain.NewID(), UserID: user.ID, ExpiresAt: now + domain.SessionTTL, CreatedAt: now,
		UserAgent: authClip(client.UserAgent, 512), IP: authClip(client.IP, 64),
	}
	if err := tx.AuthInsertSession(s, domain.HashSecret(token)); err != nil {
		return SignedIn{}, err
	}
	return SignedIn{Token: token, User: user, Session: s}, nil
}

func (a *App) SignIn(ctx context.Context, email, password string, client ClientInfo) (SignedIn, error) {
	email = strings.TrimSpace(email)
	if !domain.ValidUserEmail(email) {
		return SignedIn{}, domain.Invalid(domain.MsgInvalidEmail)
	}
	if a.Passwords == nil {
		return SignedIn{}, errPasswordsMissing
	}
	email = domain.NormalizeUserEmail(email)
	if err := a.limited(a.limits().perIP, client.IP); err != nil {
		return SignedIn{}, err
	}
	key := client.IP + "\x00" + email
	limiter := a.limits().signIn
	if err := a.limited(limiter, key); err != nil {
		return SignedIn{}, err
	}
	var cred Credentials
	found := false
	err := a.read(ctx, func(tx Tx) error {
		var err error
		cred, err = tx.AuthCredentials(email)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		found = err == nil
		return err
	})
	if err != nil {
		return SignedIn{}, err
	}
	ok := a.Passwords.Verify(cred.PasswordHash, password)
	if !found || !ok {
		return SignedIn{}, domain.E(domain.CodeNotAuthenticated, domain.MsgInvalidEmailOrPassword)
	}
	var out SignedIn
	err = a.write(ctx, func(tx Tx, _ *Changes) error {
		now := a.Now()
		user, err := tx.AuthUser(cred.User.ID)
		if errors.Is(err, ErrNoRow) {
			return domain.E(domain.CodeNotAuthenticated, domain.MsgInvalidEmailOrPassword)
		}
		if err != nil {
			return err
		}
		out, err = issueSession(tx, user, now, client)
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
	err := a.read(ctx, func(tx Tx) error {
		org, err := tx.AuthOrganization(actor.OrganizationID)
		if errors.Is(err, ErrNoRow) {
			return nil
		}
		if err != nil {
			return err
		}
		me.Organization = &MyOrganization{ID: org.ID, Name: org.Name, Slug: org.Slug, Role: actor.Role}
		return nil
	})
	return me, err
}
