package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf16"
)

// Account rules ported from better-auth 1.6.17 and convex/auth.ts (docs/go/spec/auth-orgs.md).
// Times are unix milliseconds.
const (
	authDay = int64(24 * 60 * 60 * 1000)

	// Sessions last 7 days and slide: a session used more than a day after its last renewal is
	// pushed out to 7 days from now (Better Auth expiresIn / updateAge).
	SessionTTL       = 7 * authDay
	SessionUpdateAge = 1 * authDay

	// Invite links work for a week (INVITATION_TTL_S).
	InvitationTTL = 7 * authDay
	// Better Auth's organization plugin defaults: pending unexpired invitations per organization
	// (checked on invite) and members per organization (checked on accept).
	InvitationLimit = 100
	MembershipLimit = 100

	// Passwords: length in UTF-16 code units, as JavaScript's String.length counts them.
	PasswordMinLength = 8
	PasswordMaxLength = 128

	// SessionTokenBytes of randomness in a session token.
	SessionTokenBytes = 32
)

// zod 4.6.4's z.email() pattern, which Better Auth validates emails with.
var userEmailRE = regexp.MustCompile(`^(?:[A-Za-z0-9_'+\-]+\.)*[A-Za-z0-9_'+\-]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9\-]*\.)+[A-Za-z]{2,}$`)

// ValidUserEmail: the address Better Auth would accept.
// ValidUserEmail: at most 254 characters (RFC 5321's path limit), checked before the regex.
func ValidUserEmail(email string) bool {
	return len(email) <= 254 && userEmailRE.MatchString(email)
}

// NormalizeUserEmail is how emails are stored and compared: trimmed, lower-cased.
func NormalizeUserEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// SameUserEmail: convex/auth.ts sameEmail (trimmed, case-insensitive).
func SameUserEmail(a, b string) bool { return NormalizeUserEmail(a) == NormalizeUserEmail(b) }

// authJSLength is s.length in JavaScript: UTF-16 code units.
func authJSLength(s string) int { return len(utf16.Encode([]rune(s))) }

// ValidPassword: Better Auth's length rule (min 8, max 128).
func ValidPassword(password string) error {
	n := authJSLength(password)
	if n < PasswordMinLength {
		return Invalid(MsgPasswordTooShort)
	}
	if n > PasswordMaxLength {
		return Invalid(MsgPasswordTooLong)
	}
	return nil
}

// HashSessionToken is what the database stores for a session token: hex SHA-256. A legacy Better
// Auth signed value ("<token>.<signature>") is reduced to its token first, so tokens printed by
// `keel token` before the switch keep working once sessions are imported.
func HashSessionToken(token string) string {
	if i := strings.IndexByte(token, '.'); i >= 0 {
		token = token[:i]
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// HashSecret is hex SHA-256 of a secret stored only as its hash (device codes).
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// SessionNeedsRenewal: the session was last renewed more than SessionUpdateAge ago
// (Better Auth: expiresAt - expiresIn + updateAge <= now).
func SessionNeedsRenewal(expiresAt, now int64) bool {
	return expiresAt-SessionTTL+SessionUpdateAge <= now
}

// SessionExpired: Better Auth treats a session as gone once expiresAt is not in the future.
func SessionExpired(expiresAt, now int64) bool { return expiresAt <= now }
