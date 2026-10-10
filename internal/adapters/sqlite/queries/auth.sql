-- Auth area: users, sessions, organizations, members, invitations, device codes
-- (docs/go/spec/auth-orgs.md). ASCII only.

-- name: AuthAnyUser :one
SELECT EXISTS (SELECT 1 FROM users) AS found;

-- name: AuthGetUser :one
SELECT * FROM users WHERE id = ?;

-- name: AuthGetUserByEmail :one
SELECT * FROM users WHERE email = ? COLLATE NOCASE;

-- name: AuthInsertUser :exec
INSERT INTO users (id, email, name, password_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: AuthGetSessionByTokenHash :one
SELECT * FROM sessions WHERE token_hash = ?;

-- name: AuthInsertSession :exec
INSERT INTO sessions (id, token_hash, user_id, expires_at, created_at, updated_at, user_agent, ip)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: AuthExtendSession :exec
UPDATE sessions SET expires_at = ?, updated_at = ? WHERE id = ?;

-- name: AuthDeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: AuthDeleteExpiredSessions :exec
DELETE FROM sessions WHERE expires_at <= ?;

-- name: AuthGetOrganization :one
SELECT * FROM organizations WHERE id = ?;

-- name: AuthInsertOrganization :exec
INSERT INTO organizations (id, name, slug, created_at) VALUES (?, ?, ?, ?);

-- name: AuthGetMembershipOfUser :one
SELECT * FROM members WHERE user_id = ? ORDER BY created_at, id LIMIT 1;

-- name: AuthInsertMember :exec
INSERT INTO members (id, organization_id, user_id, role, created_at) VALUES (?, ?, ?, ?, ?);

-- name: AuthCountMembers :one
SELECT COUNT(*) FROM members WHERE organization_id = ?;

-- name: AuthListMembers :many
SELECT m.id, m.organization_id, m.user_id, m.role, m.created_at, u.email, u.name
FROM members m JOIN users u ON u.id = m.user_id
WHERE m.organization_id = ?
ORDER BY m.created_at, m.id;

-- name: AuthIsMemberByEmail :one
SELECT EXISTS (
  SELECT 1 FROM members m JOIN users u ON u.id = m.user_id
  WHERE m.organization_id = ? AND u.email = ? COLLATE NOCASE
) AS member;

-- name: AuthGetInvitation :one
SELECT * FROM invitations WHERE id = ?;

-- name: AuthInsertInvitation :exec
INSERT INTO invitations (id, organization_id, email, role, status, inviter_id, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: AuthSetInvitationStatus :execrows
UPDATE invitations SET status = sqlc.arg(status)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);

-- name: AuthCancelPendingInvitations :exec
UPDATE invitations SET status = 'canceled'
WHERE organization_id = ? AND email = ? COLLATE NOCASE AND status = 'pending' AND expires_at >= ?;

-- name: AuthCountPendingInvitations :one
SELECT COUNT(*) FROM invitations
WHERE organization_id = ? AND status = 'pending' AND expires_at >= ?;

-- name: AuthListPendingInvitations :many
SELECT * FROM invitations
WHERE organization_id = ? AND status = 'pending' AND expires_at >= ?
ORDER BY created_at, id;

-- name: AuthGetDeviceCodeByHash :one
SELECT * FROM device_codes WHERE device_code_hash = ?;

-- name: AuthGetDeviceCodeByUserCode :one
SELECT * FROM device_codes WHERE user_code = ?;

-- name: AuthInsertDeviceCode :exec
INSERT INTO device_codes (id, device_code_hash, user_code, client_id, status, user_id, interval_s,
  last_polled_at, expires_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: AuthSetDevicePolled :exec
UPDATE device_codes SET last_polled_at = ? WHERE id = ?;

-- name: AuthDeleteDeviceCode :exec
DELETE FROM device_codes WHERE id = ?;

-- name: AuthConsumeApprovedDeviceCode :execrows
DELETE FROM device_codes WHERE id = ? AND status = 'approved';

-- name: AuthBindDeviceCode :execrows
UPDATE device_codes SET user_id = ?
WHERE id = ? AND status = 'pending' AND user_id IS NULL;

-- name: AuthDecideDeviceCode :execrows
UPDATE device_codes SET status = ?, user_id = ?
WHERE id = ? AND status = 'pending';

-- name: AuthDeleteExpiredDeviceCodes :exec
DELETE FROM device_codes WHERE expires_at < ?;
