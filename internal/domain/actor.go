package domain

// Actor is who is calling a use case. The zero value is signed out.
type Actor struct {
	UserID         string
	Email          string
	Name           string
	OrganizationID string // "" when the user has no membership yet
	Role           string // owner | admin | member
	SessionID      string
	System         bool // internal callers: jobs, the agent, the proxy
}

// SystemActor is used by background jobs and bearer-protected internal routes.
var SystemActor = Actor{System: true}

func (a Actor) SignedIn() bool { return a.UserID != "" }

// RequireUser fails with NOT_AUTHENTICATED when signed out.
func (a Actor) RequireUser() error {
	if !a.SignedIn() && !a.System {
		return ErrNotAuthenticated
	}
	return nil
}

// RequireMember fails when signed out or without an organization.
func (a Actor) RequireMember() error {
	if err := a.RequireUser(); err != nil {
		return err
	}
	if a.OrganizationID == "" && !a.System {
		return ErrNoOrganization
	}
	return nil
}
