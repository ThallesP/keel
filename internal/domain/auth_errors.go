package domain

const CodeRateLimited = "RATE_LIMITED"

const (
	MsgInvalidEmail       = "Invalid email"
	MsgTooManyRequests    = "Too many requests. Please try again later."
	MsgInvitationNotFound = "Invitation not found"
)

type RateLimitError struct {
	RetryAfterSeconds int64
}

func (e *RateLimitError) Error() string { return MsgTooManyRequests }

func (e *RateLimitError) Unwrap() error {
	return &Error{Code: CodeRateLimited, Message: MsgTooManyRequests}
}
