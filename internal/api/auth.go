package api

import "github.com/ThallesP/keel/internal/domain"

type User struct {
	ID    string `json:"id"`
	Email string `json:"email" example:"ci@example.com"`
	Name  string `json:"name"`
}

type Organization struct {
	ID   string      `json:"id"`
	Name string      `json:"name" example:"Default"`
	Slug string      `json:"slug" example:"default"`
	Role domain.Role `json:"role" enum:"owner,admin,member"`
}

type Me struct {
	User         *User         `json:"user"`
	Organization *Organization `json:"organization" doc:"null until the account is in the install's organization"`
}

type SignUpOpen struct {
	Open bool `json:"open" doc:"True until the first account exists; later sign-ups need an invitation"`
}

type SignUpRequest struct {
	Email        string `json:"email"`
	Password     string `json:"password" doc:"8 to 128 characters"`
	Name         string `json:"name"`
	InvitationID string `json:"invitationId,omitempty" doc:"The id from an invite link; required once the first account exists"`
}

type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type SignedIn struct {
	Token string `json:"token"`
	User  User   `json:"user"`
}

type AuthSuccess struct {
	Success bool `json:"success"`
}

type Member struct {
	ID        string      `json:"id"`
	UserID    string      `json:"userId"`
	Email     string      `json:"email"`
	Name      string      `json:"name"`
	Role      domain.Role `json:"role" enum:"owner,admin,member"`
	CreatedAt int64       `json:"createdAt" doc:"When they joined (unix ms)"`
}

type Members struct {
	Members []Member `json:"members"`
}

type CreateInvitationRequest struct {
	Email string      `json:"email"`
	Role  domain.Role `json:"role,omitempty" doc:"owner, admin or member (default)"`
}

type CreatedInvitation struct {
	ID        string      `json:"id"`
	Email     string      `json:"email"`
	Role      domain.Role `json:"role"`
	ExpiresAt int64       `json:"expiresAt" doc:"unix ms"`
}

type Invitation struct {
	ID        string                  `json:"id"`
	Email     string                  `json:"email"`
	Role      domain.Role             `json:"role"`
	Status    domain.InvitationStatus `json:"status" enum:"pending,accepted,canceled"`
	InviterID string                  `json:"inviterId"`
	ExpiresAt int64                   `json:"expiresAt"`
	CreatedAt int64                   `json:"createdAt"`
}

type Invitations struct {
	Invitations []Invitation `json:"invitations"`
}

type PublicInvitation struct {
	Email        string `json:"email"`
	Organization string `json:"organization" doc:"The organization's name"`
}

type InvitationLookup struct {
	Invitation *PublicInvitation `json:"invitation"`
}

type AcceptedInvitation struct {
	Organization Organization `json:"organization"`
}

type DeviceCodeRequest struct {
	ClientID string `json:"client_id" example:"keel-cli"`
	Scope    string `json:"scope,omitempty" doc:"Unused"`
}

type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code" example:"ABCDEFGH"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in" doc:"Seconds" example:"1800"`
	Interval                int    `json:"interval" doc:"Minimum seconds between polls" example:"5"`
}

type DeviceTokenRequest struct {
	GrantType  string `json:"grant_type" example:"urn:ietf:params:oauth:grant-type:device_code"`
	DeviceCode string `json:"device_code"`
	ClientID   string `json:"client_id" example:"keel-cli"`
}

type DeviceToken struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type" example:"Bearer"`
	ExpiresIn   int64  `json:"expires_in" doc:"Seconds until the session expires"`
}

type DeviceError struct {
	Error            string `json:"error" example:"authorization_pending" doc:"authorization_pending, slow_down, expired_token, access_denied, invalid_grant, invalid_client, invalid_request, unauthorized, unsupported_grant_type, server_error"`
	ErrorDescription string `json:"error_description" example:"Authorization pending"`
}

type DeviceStatus struct {
	UserCode string              `json:"user_code" doc:"As given"`
	Status   domain.DeviceStatus `json:"status" enum:"pending,approved,denied"`
}

type DeviceDecision struct {
	UserCode string `json:"userCode" example:"ABCD-EFGH"`
}
