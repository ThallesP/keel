package domain

// CodeRateLimited: too many attempts; retry after the response's Retry-After seconds (HTTP 429).
// Added by the auth area for the sign-in limiter: FORBIDDEN would tell a client (an agent, a CI
// script) the request can never succeed, while this one is worth retrying later. Codes are only
// ever added (apps/cli/README.md).
const CodeRateLimited = "RATE_LIMITED"

// Messages of the auth area, verbatim from better-auth 1.6.17 and convex/auth.ts (the dashboard
// shows them). docs/go/spec/auth-orgs.md §5–§7.
const (
	MsgInvalidEmail           = "Invalid email"
	MsgPasswordTooShort       = "Password too short"
	MsgPasswordTooLong        = "Password too long"
	MsgUserExists             = "User already exists. Use another email."
	MsgSignUpByInvitation     = "Sign-up is by invitation. Ask a member for an invite link."
	MsgInvalidEmailOrPassword = "Invalid email or password"
	MsgTooManyRequests        = "Too many requests. Please try again later."

	MsgNotAllowedToInvite         = "You are not allowed to invite users to this organization"
	MsgNotAllowedToInviteWithRole = "You are not allowed to invite a user with this role"
	MsgRoleNotFound               = "Role not found" // + ": <role>"
	MsgAlreadyMember              = "User is already a member of this organization"
	MsgInvitationLimit            = "Invitation limit reached"
	MsgInvitationNotFound         = "Invitation not found"
	MsgNotRecipient               = "You are not the recipient of the invitation"
	MsgMembershipLimit            = "Organization membership limit reached"
	MsgNotAllowedToCancel         = "You are not allowed to cancel this invitation"
	// New in Go: accepting an invitation while already in an organization (one membership per
	// user, auth-orgs.md §7.4).
	MsgAlreadyInOrganization = "You're already in an organization"

	// CSRF (Better Auth's origin check).
	MsgMissingOrigin = "Missing or null Origin"
	MsgInvalidOrigin = "Invalid origin"
)

// RateLimitError is a refused attempt and when to retry. It unwraps to a RATE_LIMITED *Error.
type RateLimitError struct {
	RetryAfterSeconds int64
}

func (e *RateLimitError) Error() string { return MsgTooManyRequests }

func (e *RateLimitError) Unwrap() error {
	return &Error{Code: CodeRateLimited, Message: MsgTooManyRequests}
}
