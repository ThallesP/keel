package domain

type Actor struct {
	UserID         string
	Email          string
	Name           string
	OrganizationID string
	Role           Role
	SessionID      string
	System         bool

	SessionExpiresAt int64
	SessionRenewed   bool
}

var SystemActor = Actor{System: true}

func (a Actor) SignedIn() bool { return a.UserID != "" }

func (a Actor) RequireUser() error {
	if !a.SignedIn() && !a.System {
		return ErrNotAuthenticated
	}
	return nil
}

func (a Actor) RequireMember() error {
	if err := a.RequireUser(); err != nil {
		return err
	}
	if a.OrganizationID == "" && !a.System {
		return ErrNoOrganization
	}
	return nil
}
