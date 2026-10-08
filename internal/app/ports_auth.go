package app

import "github.com/ThallesP/keel/internal/domain"

// AuthTx: users, sessions, organizations, members, invitations, device codes.
// Owner: the auth area (docs/go/spec/auth-orgs.md). Lookups return ErrNoRow when nothing matches.
// Emails are compared case-insensitively. Other areas may read through it too (e.g.
// AuthOrganization for the organization's slug).
type AuthTx interface {
	AuthAnyUser() (bool, error)
	AuthUser(id string) (domain.User, error)
	// AuthCredentials is the account with that email and its password hash.
	AuthCredentials(email string) (Credentials, error)
	AuthInsertUser(u domain.User, passwordHash string) error
	AuthSetPasswordHash(userID, hash string, now int64) error

	// AuthSession looks a session up by the hash of its token (domain.HashSessionToken).
	AuthSession(tokenHash string) (domain.Session, error)
	AuthInsertSession(s domain.Session, tokenHash string) error
	AuthExtendSession(id string, expiresAt, now int64) error
	AuthDeleteSession(id string) error
	AuthDeleteExpiredSessions(now int64) error

	AuthAnyOrganization() (bool, error)
	AuthOrganization(id string) (domain.Organization, error)
	AuthInsertOrganization(o domain.Organization) error

	// AuthMembership is the user's (first) membership.
	AuthMembership(userID string) (domain.Member, error)
	AuthInsertMember(m domain.Member) error
	AuthCountMembers(organizationID string) (int, error)
	// AuthMembers in joining order, with the account each one is.
	AuthMembers(organizationID string) ([]MemberAccount, error)
	AuthIsMemberByEmail(organizationID, email string) (bool, error)

	AuthInvitation(id string) (domain.Invitation, error)
	AuthInsertInvitation(inv domain.Invitation) error
	// AuthSetInvitationStatus moves the invitation from one status to another; false when it was
	// not in `from` (or does not exist).
	AuthSetInvitationStatus(id string, from, to domain.InvitationStatus) (bool, error)
	// AuthCancelPendingInvitations cancels the standing invitations for email in the organization.
	AuthCancelPendingInvitations(organizationID, email string, now int64) error
	AuthCountPendingInvitations(organizationID string, now int64) (int, error)
	// AuthPendingInvitations: the standing (pending, unexpired) ones, oldest first.
	AuthPendingInvitations(organizationID string, now int64) ([]domain.Invitation, error)

	// AuthDeviceCodeByHash looks a device code up by domain.HashSecret(device code).
	AuthDeviceCodeByHash(hash string) (domain.DeviceCode, error)
	AuthDeviceCodeByUserCode(userCode string) (domain.DeviceCode, error)
	AuthInsertDeviceCode(dc domain.DeviceCode, deviceCodeHash string) error
	AuthSetDevicePolled(id string, at int64) error
	AuthDeleteDeviceCode(id string) error
	// AuthConsumeApprovedDeviceCode deletes the row if it is (still) approved: the token is
	// handed out once.
	AuthConsumeApprovedDeviceCode(id string) (bool, error)
	// AuthBindDeviceCode claims a pending, unclaimed code for userID.
	AuthBindDeviceCode(id, userID string) (bool, error)
	// AuthDecideDeviceCode approves or denies a pending code.
	AuthDecideDeviceCode(id string, status domain.DeviceStatus, userID string) (bool, error)
	AuthDeleteExpiredDeviceCodes(now int64) error
}

// Credentials: an account and its stored password hash (never leaves app).
type Credentials struct {
	User         domain.User
	PasswordHash string
}

// MemberAccount is a member with its account's email and name.
type MemberAccount struct {
	domain.Member
	Email string
	Name  string
}

// Passwords hashes and checks account passwords (adapters/password). Pure CPU work; call it
// outside write transactions.
type Passwords interface {
	Hash(password string) (string, error)
	// Verify reports whether password matches hash and whether the hash should be replaced by a
	// fresh Hash (old format or costs). An empty hash never matches but takes as long as a real
	// check, so a missing account is not revealed by timing.
	Verify(hash, password string) (ok, rehash bool)
}
