package app

import "github.com/ThallesP/keel/internal/domain"

type AuthTx interface {
	AuthAnyUser() (bool, error)
	AuthUser(id string) (domain.User, error)
	AuthCredentials(email string) (Credentials, error)
	AuthInsertUser(u domain.User, passwordHash string) error
	AuthSetPasswordHash(userID, hash string, now int64) error

	AuthSession(tokenHash string) (domain.Session, error)
	AuthInsertSession(s domain.Session, tokenHash string) error
	AuthExtendSession(id string, expiresAt, now int64) error
	AuthDeleteSession(id string) error
	AuthDeleteExpiredSessions(now int64) error

	AuthAnyOrganization() (bool, error)
	AuthOrganization(id string) (domain.Organization, error)
	AuthInsertOrganization(o domain.Organization) error

	AuthMembership(userID string) (domain.Member, error)
	AuthInsertMember(m domain.Member) error
	AuthCountMembers(organizationID string) (int, error)
	AuthMembers(organizationID string) ([]MemberAccount, error)
	AuthIsMemberByEmail(organizationID, email string) (bool, error)

	AuthInvitation(id string) (domain.Invitation, error)
	AuthInsertInvitation(inv domain.Invitation) error
	AuthSetInvitationStatus(id string, from, to domain.InvitationStatus) (bool, error)
	AuthCancelPendingInvitations(organizationID, email string, now int64) error
	AuthCountPendingInvitations(organizationID string, now int64) (int, error)
	AuthPendingInvitations(organizationID string, now int64) ([]domain.Invitation, error)

	AuthDeviceCodeByHash(hash string) (domain.DeviceCode, error)
	AuthDeviceCodeByUserCode(userCode string) (domain.DeviceCode, error)
	AuthInsertDeviceCode(dc domain.DeviceCode, deviceCodeHash string) error
	AuthSetDevicePolled(id string, at int64) error
	AuthDeleteDeviceCode(id string) error
	AuthConsumeApprovedDeviceCode(id string) (bool, error)
	AuthBindDeviceCode(id, userID string) (bool, error)
	AuthDecideDeviceCode(id string, status domain.DeviceStatus, userID string) (bool, error)
	AuthDeleteExpiredDeviceCodes(now int64) error
}

type Credentials struct {
	User         domain.User
	PasswordHash string
}

type MemberAccount struct {
	domain.Member
	Email string
	Name  string
}

type Passwords interface {
	Hash(password string) (string, error)
	Verify(hash, password string) (ok, rehash bool)
}
