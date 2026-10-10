package domain

const CodeRateLimited = "RATE_LIMITED"

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
	MsgRoleNotFound               = "Role not found"
	MsgAlreadyMember              = "User is already a member of this organization"
	MsgInvitationLimit            = "Invitation limit reached"
	MsgInvitationNotFound         = "Invitation not found"
	MsgNotRecipient               = "You are not the recipient of the invitation"
	MsgMembershipLimit            = "Organization membership limit reached"
	MsgNotAllowedToCancel         = "You are not allowed to cancel this invitation"
	MsgAlreadyInOrganization      = "You're already in an organization"

	MsgMissingOrigin = "Missing or null Origin"
	MsgInvalidOrigin = "Invalid origin"
)

type RateLimitError struct {
	RetryAfterSeconds int64
}

func (e *RateLimitError) Error() string { return MsgTooManyRequests }

func (e *RateLimitError) Unwrap() error {
	return &Error{Code: CodeRateLimited, Message: MsgTooManyRequests}
}
