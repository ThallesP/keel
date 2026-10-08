package api

// Wire types of the auth area: accounts, sessions, the organization, invitations and the
// device login (docs/go/spec/auth-orgs.md, docs/go/ARCHITECTURE.md "Resolved API decisions").

// User is an account as clients see it.
type User struct {
	ID    string `json:"id"`
	Email string `json:"email" example:"ci@example.com"`
	Name  string `json:"name"`
}

// Organization is the caller's organization and their role in it.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name" example:"Default"`
	Slug string `json:"slug" example:"default"`
	Role string `json:"role" enum:"owner,admin,member"`
}

// Me is GET /api/me. Both fields are null when signed out (still 200).
type Me struct {
	User         *User         `json:"user"`
	Organization *Organization `json:"organization" doc:"null until the account is in the install's organization"`
}

// SignUpOpen is GET /api/auth/sign-up-open.
type SignUpOpen struct {
	Open bool `json:"open" doc:"True until the first account exists; later sign-ups need an invitation"`
}

// SignUpRequest is POST /api/auth/sign-up.
type SignUpRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password" doc:"8 to 128 characters"`
	Name         string `json:"name"`
	InvitationID string `json:"invitationId,omitempty" doc:"The id from an invite link; required once the first account exists"`
}

// SignInRequest is POST /api/auth/sign-in.
type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignedIn answers sign-up and sign-in. The session is also set as the keel_session cookie;
// token is the same session for CLIs and CI (Authorization: Bearer <token>).
type SignedIn struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

// AuthSuccess is {"success": true} (sign-out, device approve and deny).
type AuthSuccess struct {
	Success bool `json:"success"`
}

// Member is one member of the organization.
type Member struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	Role      string `json:"role" enum:"owner,admin,member"`
	CreatedAt int64  `json:"createdAt" doc:"When they joined (unix ms)"`
}

// Members is GET /api/organization/members.
type Members struct {
	Members []Member `json:"members"`
}

// CreateInvitationRequest is POST /api/organization/invitations.
type CreateInvitationRequest struct {
	Email string `json:"email"`
	Role  string `json:"role,omitempty" doc:"owner, admin or member (default)"`
}

// CreatedInvitation answers POST /api/organization/invitations. The invite link is
// <dashboard origin>/invite/<id>; the id is its secret.
type CreatedInvitation struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	ExpiresAt int64  `json:"expiresAt" doc:"unix ms"`
}

// Invitation is a standing invitation of the organization.
type Invitation struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status" enum:"pending,accepted,canceled,rejected"`
	InviterID string `json:"inviterId"`
	ExpiresAt int64  `json:"expiresAt"`
	CreatedAt int64  `json:"createdAt"`
}

// Invitations is GET /api/organization/invitations: the pending, unexpired ones.
type Invitations struct {
	Invitations []Invitation `json:"invitations"`
}

// PublicInvitation is what an invite link shows before sign-up.
type PublicInvitation struct {
	Email        string `json:"email"`
	Organization string `json:"organization" doc:"The organization's name"`
}

// InvitationLookup is GET /api/invitations/{id}: null when unknown, spent or expired.
type InvitationLookup struct {
	Invitation *PublicInvitation `json:"invitation"`
}

// AcceptedInvitation answers POST /api/invitations/{id}/accept.
type AcceptedInvitation struct {
	Organization Organization `json:"organization"`
}

// Device authorization (RFC 8628) bodies use the RFC's snake_case names.

// DeviceCodeRequest is POST /api/auth/device/code.
type DeviceCodeRequest struct {
	ClientID string `json:"client_id" example:"keel-cli"`
	Scope    string `json:"scope,omitempty" doc:"Unused"`
}

// DeviceCode answers POST /api/auth/device/code.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code" example:"ABCDEFGH"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in" doc:"Seconds" example:"1800"`
	Interval                int    `json:"interval" doc:"Minimum seconds between polls" example:"5"`
}

// DeviceTokenRequest is POST /api/auth/device/token.
type DeviceTokenRequest struct {
	GrantType  string `json:"grant_type" example:"urn:ietf:params:oauth:grant-type:device_code"`
	DeviceCode string `json:"device_code"`
	ClientID   string `json:"client_id" example:"keel-cli"`
}

// DeviceToken is an approved login: a session token for the approving account, handed out once.
type DeviceToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type" example:"Bearer"`
	ExpiresIn   int64  `json:"expires_in" doc:"Seconds until the session expires"`
}

// DeviceError is every device endpoint's error body (RFC 8628 / RFC 6749), not a problem.
type DeviceError struct {
	Error            string `json:"error" example:"authorization_pending" doc:"authorization_pending, slow_down, expired_token, access_denied, invalid_grant, invalid_client, invalid_request, unauthorized, unsupported_grant_type, server_error"`
	ErrorDescription string `json:"error_description" example:"Authorization pending"`
}

// DeviceStatus answers GET /api/auth/device?user_code=.
type DeviceStatus struct {
	UserCode string `json:"user_code" doc:"As given"`
	Status   string `json:"status" enum:"pending,approved,denied"`
}

// DeviceDecision is POST /api/auth/device/approve and /deny.
type DeviceDecision struct {
	UserCode string `json:"userCode" example:"ABCD-EFGH"`
}
