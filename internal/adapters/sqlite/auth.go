package sqlite

// Implements app.AuthTx.

import (
	"github.com/ThallesP/keel/internal/adapters/sqlite/db"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

func authUserOf(u db.User) domain.User {
	return domain.User{ID: u.ID, Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt}
}

func authSessionOf(s db.Session) domain.Session {
	return domain.Session{ID: s.ID, UserID: s.UserID, ExpiresAt: s.ExpiresAt, CreatedAt: s.CreatedAt,
		UserAgent: s.UserAgent, IP: s.Ip}
}

func authMemberOf(m db.Member) domain.Member {
	return domain.Member{ID: m.ID, OrganizationID: m.OrganizationID, UserID: m.UserID, Role: m.Role, CreatedAt: m.CreatedAt}
}

func authInvitationOf(i db.Invitation) domain.Invitation {
	return domain.Invitation{ID: i.ID, OrganizationID: i.OrganizationID, Email: i.Email, Role: i.Role,
		Status: domain.InvitationStatus(i.Status), InviterID: i.InviterID, ExpiresAt: i.ExpiresAt, CreatedAt: i.CreatedAt}
}

func authDeviceCodeOf(d db.DeviceCode) domain.DeviceCode {
	return domain.DeviceCode{ID: d.ID, UserCode: d.UserCode, ClientID: d.ClientID, Status: domain.DeviceStatus(d.Status),
		UserID: str(d.UserID), IntervalS: int(d.IntervalS), LastPolledAt: d.LastPolledAt, ExpiresAt: d.ExpiresAt,
		CreatedAt: d.CreatedAt}
}

func (t *tx) AuthAnyUser() (bool, error) { return t.q.AuthAnyUser(t.ctx) }

func (t *tx) AuthUser(id string) (domain.User, error) {
	u, err := t.q.AuthGetUser(t.ctx, id)
	if err != nil {
		return domain.User{}, noRow(err)
	}
	return authUserOf(u), nil
}

func (t *tx) AuthCredentials(email string) (app.Credentials, error) {
	u, err := t.q.AuthGetUserByEmail(t.ctx, email)
	if err != nil {
		return app.Credentials{}, noRow(err)
	}
	return app.Credentials{User: authUserOf(u), PasswordHash: u.PasswordHash}, nil
}

func (t *tx) AuthInsertUser(u domain.User, passwordHash string) error {
	return t.q.AuthInsertUser(t.ctx, db.AuthInsertUserParams{ID: u.ID, Email: u.Email, Name: u.Name,
		PasswordHash: passwordHash, CreatedAt: u.CreatedAt, UpdatedAt: u.CreatedAt})
}

func (t *tx) AuthSetPasswordHash(userID, hash string, now int64) error {
	return t.q.AuthSetPasswordHash(t.ctx, db.AuthSetPasswordHashParams{PasswordHash: hash, UpdatedAt: now, ID: userID})
}

func (t *tx) AuthSession(tokenHash string) (domain.Session, error) {
	s, err := t.q.AuthGetSessionByTokenHash(t.ctx, tokenHash)
	if err != nil {
		return domain.Session{}, noRow(err)
	}
	return authSessionOf(s), nil
}

func (t *tx) AuthInsertSession(s domain.Session, tokenHash string) error {
	return t.q.AuthInsertSession(t.ctx, db.AuthInsertSessionParams{ID: s.ID, TokenHash: tokenHash, UserID: s.UserID,
		ExpiresAt: s.ExpiresAt, CreatedAt: s.CreatedAt, UpdatedAt: s.CreatedAt, UserAgent: s.UserAgent, Ip: s.IP})
}

func (t *tx) AuthExtendSession(id string, expiresAt, now int64) error {
	return t.q.AuthExtendSession(t.ctx, db.AuthExtendSessionParams{ExpiresAt: expiresAt, UpdatedAt: now, ID: id})
}

func (t *tx) AuthDeleteSession(id string) error { return t.q.AuthDeleteSession(t.ctx, id) }

func (t *tx) AuthDeleteExpiredSessions(now int64) error {
	return t.q.AuthDeleteExpiredSessions(t.ctx, now)
}

func (t *tx) AuthAnyOrganization() (bool, error) { return t.q.AuthAnyOrganization(t.ctx) }

func (t *tx) AuthOrganization(id string) (domain.Organization, error) {
	o, err := t.q.AuthGetOrganization(t.ctx, id)
	if err != nil {
		return domain.Organization{}, noRow(err)
	}
	return domain.Organization{ID: o.ID, Name: o.Name, Slug: o.Slug, CreatedAt: o.CreatedAt}, nil
}

func (t *tx) AuthInsertOrganization(o domain.Organization) error {
	return t.q.AuthInsertOrganization(t.ctx, db.AuthInsertOrganizationParams{ID: o.ID, Name: o.Name, Slug: o.Slug, CreatedAt: o.CreatedAt})
}

func (t *tx) AuthMembership(userID string) (domain.Member, error) {
	m, err := t.q.AuthGetMembershipOfUser(t.ctx, userID)
	if err != nil {
		return domain.Member{}, noRow(err)
	}
	return authMemberOf(m), nil
}

func (t *tx) AuthInsertMember(m domain.Member) error {
	return t.q.AuthInsertMember(t.ctx, db.AuthInsertMemberParams{ID: m.ID, OrganizationID: m.OrganizationID,
		UserID: m.UserID, Role: m.Role, CreatedAt: m.CreatedAt})
}

func (t *tx) AuthCountMembers(organizationID string) (int, error) {
	n, err := t.q.AuthCountMembers(t.ctx, organizationID)
	return int(n), err
}

func (t *tx) AuthMembers(organizationID string) ([]app.MemberAccount, error) {
	rows, err := t.q.AuthListMembers(t.ctx, organizationID)
	if err != nil {
		return nil, err
	}
	out := make([]app.MemberAccount, 0, len(rows))
	for _, r := range rows {
		out = append(out, app.MemberAccount{
			Member: domain.Member{ID: r.ID, OrganizationID: r.OrganizationID, UserID: r.UserID, Role: r.Role, CreatedAt: r.CreatedAt},
			Email:  r.Email, Name: r.Name,
		})
	}
	return out, nil
}

func (t *tx) AuthIsMemberByEmail(organizationID, email string) (bool, error) {
	return t.q.AuthIsMemberByEmail(t.ctx, db.AuthIsMemberByEmailParams{OrganizationID: organizationID, Email: email})
}

func (t *tx) AuthInvitation(id string) (domain.Invitation, error) {
	i, err := t.q.AuthGetInvitation(t.ctx, id)
	if err != nil {
		return domain.Invitation{}, noRow(err)
	}
	return authInvitationOf(i), nil
}

func (t *tx) AuthInsertInvitation(inv domain.Invitation) error {
	return t.q.AuthInsertInvitation(t.ctx, db.AuthInsertInvitationParams{ID: inv.ID, OrganizationID: inv.OrganizationID,
		Email: inv.Email, Role: inv.Role, Status: string(inv.Status), InviterID: inv.InviterID,
		ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt})
}

func (t *tx) AuthSetInvitationStatus(id string, from, to domain.InvitationStatus) (bool, error) {
	n, err := t.q.AuthSetInvitationStatus(t.ctx, db.AuthSetInvitationStatusParams{Status: string(to), ID: id, FromStatus: string(from)})
	return n > 0, err
}

func (t *tx) AuthCancelPendingInvitations(organizationID, email string, now int64) error {
	return t.q.AuthCancelPendingInvitations(t.ctx, db.AuthCancelPendingInvitationsParams{OrganizationID: organizationID,
		Email: email, ExpiresAt: now})
}

func (t *tx) AuthCountPendingInvitations(organizationID string, now int64) (int, error) {
	n, err := t.q.AuthCountPendingInvitations(t.ctx, db.AuthCountPendingInvitationsParams{OrganizationID: organizationID, ExpiresAt: now})
	return int(n), err
}

func (t *tx) AuthPendingInvitations(organizationID string, now int64) ([]domain.Invitation, error) {
	rows, err := t.q.AuthListPendingInvitations(t.ctx, db.AuthListPendingInvitationsParams{OrganizationID: organizationID, ExpiresAt: now})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Invitation, 0, len(rows))
	for _, r := range rows {
		out = append(out, authInvitationOf(r))
	}
	return out, nil
}

func (t *tx) AuthDeviceCodeByHash(hash string) (domain.DeviceCode, error) {
	d, err := t.q.AuthGetDeviceCodeByHash(t.ctx, hash)
	if err != nil {
		return domain.DeviceCode{}, noRow(err)
	}
	return authDeviceCodeOf(d), nil
}

func (t *tx) AuthDeviceCodeByUserCode(userCode string) (domain.DeviceCode, error) {
	d, err := t.q.AuthGetDeviceCodeByUserCode(t.ctx, userCode)
	if err != nil {
		return domain.DeviceCode{}, noRow(err)
	}
	return authDeviceCodeOf(d), nil
}

func (t *tx) AuthInsertDeviceCode(dc domain.DeviceCode, deviceCodeHash string) error {
	return t.q.AuthInsertDeviceCode(t.ctx, db.AuthInsertDeviceCodeParams{ID: dc.ID, DeviceCodeHash: deviceCodeHash,
		UserCode: dc.UserCode, ClientID: dc.ClientID, Status: string(dc.Status), UserID: nullStr(dc.UserID),
		IntervalS: int64(dc.IntervalS), LastPolledAt: dc.LastPolledAt, ExpiresAt: dc.ExpiresAt, CreatedAt: dc.CreatedAt})
}

func (t *tx) AuthSetDevicePolled(id string, at int64) error {
	return t.q.AuthSetDevicePolled(t.ctx, db.AuthSetDevicePolledParams{LastPolledAt: &at, ID: id})
}

func (t *tx) AuthDeleteDeviceCode(id string) error { return t.q.AuthDeleteDeviceCode(t.ctx, id) }

func (t *tx) AuthConsumeApprovedDeviceCode(id string) (bool, error) {
	n, err := t.q.AuthConsumeApprovedDeviceCode(t.ctx, id)
	return n > 0, err
}

func (t *tx) AuthBindDeviceCode(id, userID string) (bool, error) {
	n, err := t.q.AuthBindDeviceCode(t.ctx, db.AuthBindDeviceCodeParams{UserID: &userID, ID: id})
	return n > 0, err
}

func (t *tx) AuthDecideDeviceCode(id string, status domain.DeviceStatus, userID string) (bool, error) {
	n, err := t.q.AuthDecideDeviceCode(t.ctx, db.AuthDecideDeviceCodeParams{Status: string(status), UserID: &userID, ID: id})
	return n > 0, err
}

func (t *tx) AuthDeleteExpiredDeviceCodes(now int64) error {
	return t.q.AuthDeleteExpiredDeviceCodes(t.ctx, now)
}
