package app

import (
	"context"
	"errors"
	"strings"

	"github.com/ThallesP/keel/internal/domain"
)

// Accounts and sessions (docs/go/spec/auth-orgs.md §4–§6). Replaces Better Auth's email/password
// endpoints, its session table and convex/auth.ts's database hooks.

// ClientInfo is where a request came from; it is recorded on the sessions it creates.
type ClientInfo struct {
	IP        string
	UserAgent string
}

// SignedIn is a new session. Token is the secret the client keeps (cookie or bearer); it is
// shown once and only its hash is stored.
type SignedIn struct {
	Token   string
	User    domain.User
	Session domain.Session
}

// SignUpInput is POST /api/auth/sign-up.
type SignUpInput struct {
	Email        string
	Password     string
	Name         string
	InvitationID string // from an invite link; required once the first account exists
	Client       ClientInfo
}

// MyOrganization is the caller's organization and role (convex organizations.current).
type MyOrganization struct {
	ID   string
	Name string
	Slug string
	Role string
}

// Me is GET /api/me: who is signed in and in which organization. Both nil when signed out.
type Me struct {
	User         *domain.User
	Organization *MyOrganization
}

// errPasswordsMissing: serve did not set App.Passwords.
var errPasswordsMissing = errors.New("app: Passwords is not set")

// authClip bounds request metadata stored on a session.
func authClip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ResolveSession turns a session token (cookie or bearer) into the actor. A missing, unknown or
// expired token is the signed-out actor, not an error. A session used more than a day after its
// last renewal is pushed out to 7 days from now (actor.SessionRenewed tells the transport to
// re-send the cookie); an expired one is deleted.
func (a *App) ResolveSession(ctx context.Context, token string) (domain.Actor, error) {
	if token == "" {
		return domain.Actor{}, nil
	}
	hash := domain.HashSessionToken(token)
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
			a.Log.Warn("renew session", "err", err) // still valid until its old expiry
		} else {
			actor.SessionExpiresAt, actor.SessionRenewed = expires, true
		}
	}
	return actor, nil
}

// authMissing: ErrNoRow means "not signed in", anything else is a failure.
func authMissing(err error) error {
	if errors.Is(err, ErrNoRow) {
		return nil
	}
	return err
}

// SignUpOpen: true until the first account exists (convex auth.signUpOpen).
func (a *App) SignUpOpen(ctx context.Context) (bool, error) {
	var someone bool
	err := a.read(ctx, func(tx Tx) (err error) { someone, err = tx.AuthAnyUser(); return })
	return !someone, err
}

// standingInvitation is the invitation with that id if it is pending and unexpired, else nil
// (convex/auth.ts pendingInvitation; unknown and malformed ids are nil too).
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

// signUpRule is Better Auth's sign-up order plus Keel's user.create hooks: a taken email is
// refused first (so it never reveals the invite rule), then everyone but the first account needs
// a standing invitation for that email. It returns the invitation to join (nil: none) and
// whether this is the first account.
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

// SignUp creates an account and signs it in, in one transaction (user, membership, spent
// invitation, session: Better Auth did these as separate writes, auth-orgs.md §5.1).
//
// The first account founds the install's organization and owns it (Go: eagerly, at sign-up;
// Convex founded it on the first dashboard visit, which `keel login` could skip, §6.4). Later
// accounts need a standing invitation for their email and join with its role.
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
	// Refuse early, before paying for a hash; the write checks again.
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
			founded, err := tx.AuthAnyOrganization()
			if err != nil {
				return err
			}
			if !founded {
				org, err := foundOrganization(tx, user.ID, now)
				if err != nil {
					return err
				}
				authMembershipChanged(ch, org.ID)
			}
		}
		out, err = issueSession(tx, user, now, in.Client)
		return err
	})
	return out, err
}

// joinWithInvitation spends the invitation and makes userID a member with its role.
func joinWithInvitation(tx Tx, ch *Changes, inv domain.Invitation, userID string, now int64) error {
	ok, err := tx.AuthSetInvitationStatus(inv.ID, domain.InvitationPending, domain.InvitationAccepted)
	if err != nil {
		return err
	}
	if !ok {
		return domain.NotFound(domain.MsgInvitationNotFound)
	}
	role := inv.Role
	if role == "" {
		role = domain.RoleMember
	}
	err = tx.AuthInsertMember(domain.Member{ID: domain.NewID(), OrganizationID: inv.OrganizationID, UserID: userID, Role: role, CreatedAt: now})
	if err != nil {
		return err
	}
	authMembershipChanged(ch, inv.OrganizationID)
	return nil
}

// issueSession creates a session for user (7 days) and returns its token. Expired sessions are
// swept on the way (there is no cleanup job).
func issueSession(tx Tx, user domain.User, now int64, client ClientInfo) (SignedIn, error) {
	if err := tx.AuthDeleteExpiredSessions(now); err != nil {
		return SignedIn{}, err
	}
	token := domain.NewSecret(domain.SessionTokenBytes)
	s := domain.Session{
		ID: domain.NewID(), UserID: user.ID, ExpiresAt: now + domain.SessionTTL, CreatedAt: now,
		UserAgent: authClip(client.UserAgent, 512), IP: authClip(client.IP, 64),
	}
	if err := tx.AuthInsertSession(s, domain.HashSessionToken(token)); err != nil {
		return SignedIn{}, err
	}
	return SignedIn{Token: token, User: user, Session: s}, nil
}

// SignIn checks an email and password and opens a session (Better Auth /sign-in/email). Every
// failure is the same 401 so accounts cannot be enumerated; a miss still costs a hash. A Better
// Auth (scrypt) hash is replaced with argon2id on success. At most SignInAttempts tries per
// client IP and email in SignInWindow (RATE_LIMITED).
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
	ok, rehash := a.Passwords.Verify(cred.PasswordHash, password)
	if !found || !ok {
		return SignedIn{}, domain.E(domain.CodeNotAuthenticated, domain.MsgInvalidEmailOrPassword)
	}
	newHash := ""
	if rehash {
		if newHash, err = a.Passwords.Hash(password); err != nil {
			a.Log.Warn("rehash password", "err", err)
			newHash = ""
		}
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
		if newHash != "" {
			if err := tx.AuthSetPasswordHash(user.ID, newHash, now); err != nil {
				return err
			}
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

// SignOut deletes the caller's session. Signed out already is fine.
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

// GetMe is the caller and their organization (convex auth.getCurrentUser + organizations.current).
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
