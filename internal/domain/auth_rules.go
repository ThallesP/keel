package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

const (
	authDay = int64(24 * 60 * 60 * 1000)

	SessionTTL       = 7 * authDay
	SessionUpdateAge = 1 * authDay

	InvitationTTL   = 7 * authDay
	InvitationLimit = 100
	MembershipLimit = 100

	PasswordMinLength = 8
	PasswordMaxLength = 128

	SessionTokenBytes = 32
)

var userEmailRE = regexp.MustCompile(`^(?:[A-Za-z0-9_'+\-]+\.)*[A-Za-z0-9_'+\-]*[A-Za-z0-9_+-]@(?:[A-Za-z0-9][A-Za-z0-9\-]*\.)+[A-Za-z]{2,}$`)

func ValidUserEmail(email string) bool {
	return len(email) <= 254 && userEmailRE.MatchString(email)
}

func NormalizeUserEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func SameUserEmail(a, b string) bool { return NormalizeUserEmail(a) == NormalizeUserEmail(b) }

func ValidPassword(password string) error {
	n := UTF16Len(password)
	if n < PasswordMinLength {
		return Invalid(MsgPasswordTooShort)
	}
	if n > PasswordMaxLength {
		return Invalid(MsgPasswordTooLong)
	}
	return nil
}

func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func SessionNeedsRenewal(expiresAt, now int64) bool {
	return expiresAt-SessionTTL+SessionUpdateAge <= now
}

func SessionExpired(expiresAt, now int64) bool { return expiresAt <= now }
