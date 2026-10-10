package http

import (
	"context"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/app"
	"github.com/ThallesP/keel/internal/domain"
)

var authTags = []string{"auth"}

var authPublic = []map[string][]string{}

type (
	authSignUpOpenOutput struct{ Body api.SignUpOpen }
	authSignUpInput      struct{ Body api.SignUpRequest }
	authSignInInput      struct{ Body api.SignInRequest }
	authSignedInOutput   struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
		Body      api.SignedIn
	}
	authSignOutOutput struct {
		SetCookie http.Cookie `header:"Set-Cookie"`
		Body      api.AuthSuccess
	}
	authMeOutput          struct{ Body api.Me }
	authMembersOutput     struct{ Body api.Members }
	authInvitationsOutput struct{ Body api.Invitations }
	authCreateInvInput    struct{ Body api.CreateInvitationRequest }
	authCreateInvOutput   struct{ Body api.CreatedInvitation }
	authInvitationIDInput struct {
		ID string `path:"id" doc:"Invitation id (the secret in the invite link)"`
	}
	authInvitationLookupOutput struct{ Body api.InvitationLookup }
	authAcceptedOutput         struct{ Body api.AcceptedInvitation }

	authDeviceCodeInput  struct{ Body api.DeviceCodeRequest }
	authDeviceCodeOutput struct {
		CacheControl string `header:"Cache-Control"`
		Body         api.DeviceCode
	}
	authDeviceTokenInput  struct{ Body api.DeviceTokenRequest }
	authDeviceTokenOutput struct {
		CacheControl string `header:"Cache-Control"`
		Pragma       string `header:"Pragma"`
		Body         api.DeviceToken
	}
	authDeviceLookupInput struct {
		UserCode string `query:"user_code" required:"true" doc:"The code keel login printed (dashes are ignored)"`
	}
	authDeviceStatusOutput struct{ Body api.DeviceStatus }
	authDeviceDecideInput  struct{ Body api.DeviceDecision }
	authSuccessOutput      struct{ Body api.AuthSuccess }
)

func authUserView(u domain.User) api.User { return api.User{ID: u.ID, Email: u.Email, Name: u.Name} }

func authOrganizationView(o app.MyOrganization) api.Organization {
	return api.Organization{ID: o.ID, Name: o.Name, Slug: o.Slug, Role: o.Role}
}

func authDeviceResponses(h huma.API, statuses ...string) map[string]*huma.Response {
	schema := h.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[api.DeviceError](), true, "DeviceError")
	out := map[string]*huma.Response{}
	for _, st := range statuses {
		out[st] = &huma.Response{
			Description: "RFC 8628 error: {error, error_description}",
			Content:     map[string]*huma.MediaType{"application/json": {Schema: schema}},
		}
	}
	return out
}

func (s *Server) registerAuth(h huma.API) {
	op(h, huma.Operation{
		OperationID: "getSignUpOpen", Method: http.MethodGet, Path: "/api/auth/sign-up-open", Tags: authTags,
		Summary: "Whether sign-up is open (no account exists yet)", Security: authPublic,
	}, func(ctx context.Context, _ *struct{}) (*authSignUpOpenOutput, error) {
		open, err := s.app.SignUpOpen(ctx)
		if err != nil {
			return nil, err
		}
		return &authSignUpOpenOutput{Body: api.SignUpOpen{Open: open}}, nil
	})

	authOp(h, huma.Operation{
		OperationID: "signUp", Method: http.MethodPost, Path: "/api/auth/sign-up", Tags: authTags,
		Summary:     "Create an account and sign in",
		Description: "The first account founds the install's organization. Later ones need `invitationId` from an invite link for the same email. Sets the keel_session cookie and returns the same session as `token`.",
		Security:    authPublic,
	}, func(ctx context.Context, in *authSignUpInput) (*authSignedInOutput, error) {
		out, err := s.app.SignUp(ctx, app.SignUpInput{
			Email: in.Body.Email, Password: in.Body.Password, Name: in.Body.Name,
			InvitationID: in.Body.InvitationID, Client: authClientOf(ctx),
		})
		if err != nil {
			return nil, err
		}
		return s.authSignedIn(out), nil
	})

	authOp(h, huma.Operation{
		OperationID: "signIn", Method: http.MethodPost, Path: "/api/auth/sign-in", Tags: authTags,
		Summary:     "Sign in with email and password",
		Description: "Sets the keel_session cookie and returns the same session as `token`. At most 10 tries per client and email in 5 minutes (429 RATE_LIMITED, Retry-After).",
		Security:    authPublic,
	}, func(ctx context.Context, in *authSignInInput) (*authSignedInOutput, error) {
		out, err := s.app.SignIn(ctx, in.Body.Email, in.Body.Password, authClientOf(ctx))
		if err != nil {
			return nil, err
		}
		return s.authSignedIn(out), nil
	})

	op(h, huma.Operation{
		OperationID: "signOut", Method: http.MethodPost, Path: "/api/auth/sign-out", Tags: authTags,
		Summary: "Sign out: delete the presented session and clear the cookie", Security: authPublic,
	}, func(ctx context.Context, _ *struct{}) (*authSignOutOutput, error) {
		if err := s.app.SignOut(ctx, ActorFrom(ctx)); err != nil {
			return nil, err
		}
		return &authSignOutOutput{SetCookie: s.authClearedCookie(), Body: api.AuthSuccess{Success: true}}, nil
	})

	op(h, huma.Operation{
		OperationID: "getMe", Method: http.MethodGet, Path: "/api/me", Tags: authTags,
		Summary:     "Who is signed in, and their organization",
		Description: "200 with `user: null` when signed out.",
		Security:    authPublic,
	}, func(ctx context.Context, _ *struct{}) (*authMeOutput, error) {
		me, err := s.app.GetMe(ctx, ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		var out api.Me
		if me.User != nil {
			u := authUserView(*me.User)
			out.User = &u
		}
		if me.Organization != nil {
			o := authOrganizationView(*me.Organization)
			out.Organization = &o
		}
		return &authMeOutput{Body: out}, nil
	})

	op(h, huma.Operation{
		OperationID: "listMembers", Method: http.MethodGet, Path: "/api/organization/members", Tags: authTags,
		Summary: "Members of your organization",
	}, func(ctx context.Context, _ *struct{}) (*authMembersOutput, error) {
		ms, err := s.app.ListMembers(ctx, ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		out := api.Members{Members: make([]api.Member, 0, len(ms))}
		for _, m := range ms {
			out.Members = append(out.Members, api.Member{ID: m.ID, UserID: m.UserID, Email: m.Email, Name: m.Name, Role: m.Role, CreatedAt: m.CreatedAt})
		}
		return &authMembersOutput{Body: out}, nil
	})

	op(h, huma.Operation{
		OperationID: "listInvitations", Method: http.MethodGet, Path: "/api/organization/invitations", Tags: authTags,
		Summary: "Pending invitations of your organization",
	}, func(ctx context.Context, _ *struct{}) (*authInvitationsOutput, error) {
		invs, err := s.app.ListInvitations(ctx, ActorFrom(ctx))
		if err != nil {
			return nil, err
		}
		out := api.Invitations{Invitations: make([]api.Invitation, 0, len(invs))}
		for _, i := range invs {
			out.Invitations = append(out.Invitations, api.Invitation{ID: i.ID, Email: i.Email, Role: i.Role,
				Status: string(i.Status), InviterID: i.InviterID, ExpiresAt: i.ExpiresAt, CreatedAt: i.CreatedAt})
		}
		return &authInvitationsOutput{Body: out}, nil
	})

	op(h, huma.Operation{
		OperationID: "createInvitation", Method: http.MethodPost, Path: "/api/organization/invitations", Tags: authTags,
		Summary:     "Invite someone by email (owners and admins)",
		Description: "The invite link is `<dashboard origin>/invite/<id>`; it works once, for that email, for a week. Inviting the same email again cancels the previous link. Nothing is mailed.",
	}, func(ctx context.Context, in *authCreateInvInput) (*authCreateInvOutput, error) {
		inv, err := s.app.CreateInvitation(ctx, ActorFrom(ctx), in.Body.Email, in.Body.Role)
		if err != nil {
			return nil, err
		}
		return &authCreateInvOutput{Body: api.CreatedInvitation{ID: inv.ID, Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt}}, nil
	})

	op(h, huma.Operation{
		OperationID: "cancelInvitation", Method: http.MethodDelete, Path: "/api/organization/invitations/{id}", Tags: authTags,
		Summary: "Cancel a pending invitation (owners and admins)", DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *authInvitationIDInput) (*struct{}, error) {
		if err := s.app.CancelInvitation(ctx, ActorFrom(ctx), in.ID); err != nil {
			return nil, err
		}
		return nil, nil
	})

	op(h, huma.Operation{
		OperationID: "getInvitation", Method: http.MethodGet, Path: "/api/invitations/{id}", Tags: authTags,
		Summary:     "What an invite link is for",
		Description: "Public: the id is the secret in the link. `invitation` is null when the link is unknown, used or expired.",
		Security:    authPublic,
	}, func(ctx context.Context, in *authInvitationIDInput) (*authInvitationLookupOutput, error) {
		inv, err := s.app.GetInvitation(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		var out api.InvitationLookup
		if inv != nil {
			out.Invitation = &api.PublicInvitation{Email: inv.Email, Organization: inv.Organization}
		}
		return &authInvitationLookupOutput{Body: out}, nil
	})

	op(h, huma.Operation{
		OperationID: "acceptInvitation", Method: http.MethodPost, Path: "/api/invitations/{id}/accept", Tags: authTags,
		Summary: "Join the invitation's organization as the signed-in account (same email)",
	}, func(ctx context.Context, in *authInvitationIDInput) (*authAcceptedOutput, error) {
		org, err := s.app.AcceptInvitation(ctx, ActorFrom(ctx), in.ID)
		if err != nil {
			return nil, err
		}
		return &authAcceptedOutput{Body: api.AcceptedInvitation{Organization: authOrganizationView(org)}}, nil
	})

	authOp(h, huma.Operation{
		OperationID: "createDeviceCode", Method: http.MethodPost, Path: "/api/auth/device/code", Tags: authTags,
		Summary: "Start a keel login: a code and the link a person approves", Security: authPublic,
		Responses: authDeviceResponses(h, "400"),
	}, func(ctx context.Context, in *authDeviceCodeInput) (*authDeviceCodeOutput, error) {
		d, err := s.app.StartDeviceLogin(ctx, in.Body.ClientID, authClientOf(ctx))
		if err != nil {
			return nil, err
		}
		return &authDeviceCodeOutput{CacheControl: "no-store", Body: api.DeviceCode{
			DeviceCode: d.DeviceCode, UserCode: d.UserCode, VerificationURI: d.VerificationURI,
			VerificationURIComplete: d.VerificationURIComplete, ExpiresIn: d.ExpiresIn, Interval: d.Interval,
		}}, nil
	})

	authOp(h, huma.Operation{
		OperationID: "pollDeviceToken", Method: http.MethodPost, Path: "/api/auth/device/token", Tags: authTags,
		Summary:     "Poll a keel login; once approved, the session token (handed out once)",
		Description: "Errors are RFC 8628 bodies: authorization_pending, slow_down (poll at most every `interval` seconds), expired_token, access_denied, invalid_grant.",
		Security:    authPublic,
		Responses:   authDeviceResponses(h, "400", "500"),
	}, func(ctx context.Context, in *authDeviceTokenInput) (*authDeviceTokenOutput, error) {
		t, err := s.app.PollDeviceLogin(ctx, in.Body.GrantType, in.Body.DeviceCode, in.Body.ClientID, authClientOf(ctx))
		if err != nil {
			return nil, err
		}
		return &authDeviceTokenOutput{CacheControl: "no-store", Pragma: "no-cache",
			Body: api.DeviceToken{AccessToken: t.AccessToken, TokenType: "Bearer", ExpiresIn: t.ExpiresIn}}, nil
	})

	authOp(h, huma.Operation{
		OperationID: "claimDeviceCode", Method: http.MethodGet, Path: "/api/auth/device", Tags: authTags,
		Summary:     "Look a keel login code up; signed in, also claim it",
		Description: "While signed in, an unclaimed pending code is bound to you: only you can then approve or deny it.",
		Security:    authPublic,
		Responses:   authDeviceResponses(h, "400"),
	}, func(ctx context.Context, in *authDeviceLookupInput) (*authDeviceStatusOutput, error) {
		v, err := s.app.ClaimDeviceCode(ctx, ActorFrom(ctx), in.UserCode)
		if err != nil {
			return nil, err
		}
		return &authDeviceStatusOutput{Body: api.DeviceStatus{UserCode: v.UserCode, Status: string(v.Status)}}, nil
	})

	for _, d := range []struct {
		id, path, summary string
		approve           bool
	}{
		{"approveDevice", "/api/auth/device/approve", "Approve a keel login you claimed", true},
		{"denyDevice", "/api/auth/device/deny", "Deny a keel login you claimed", false},
	} {
		authOp(h, huma.Operation{
			OperationID: d.id, Method: http.MethodPost, Path: d.path, Tags: authTags, Summary: d.summary,
			Responses: authDeviceResponses(h, "400", "401", "403"),
		}, func(ctx context.Context, in *authDeviceDecideInput) (*authSuccessOutput, error) {
			if err := s.app.DecideDeviceLogin(ctx, ActorFrom(ctx), in.Body.UserCode, d.approve); err != nil {
				return nil, err
			}
			return &authSuccessOutput{Body: api.AuthSuccess{Success: true}}, nil
		})
	}

	authNullable[api.Me](h, "user", "organization")
	authNullable[api.InvitationLookup](h, "invitation")
}

func authNullable[T any](h huma.API, fields ...string) {
	reg := h.OpenAPI().Components.Schemas
	s := reg.SchemaFromRef(reg.Schema(reflect.TypeFor[T](), true, "").Ref)
	for _, f := range fields {
		p := s.Properties[f]
		s.Properties[f] = &huma.Schema{Description: p.Description, OneOf: []*huma.Schema{p, {Type: "null"}}}
		p.Description = ""
	}
}

func (s *Server) authSignedIn(out app.SignedIn) *authSignedInOutput {
	return &authSignedInOutput{
		SetCookie: s.authSessionCookie(out.Token, out.Session.ExpiresAt),
		Body:      api.SignedIn{Token: out.Token, User: authUserView(out.User)},
	}
}
