package app

import (
	"context"

	"github.com/ThallesP/keel/internal/domain"
)

// ResolveSession turns a session token (cookie or bearer) into the actor. A missing, unknown or
// expired token is the signed-out actor, not an error. Owner: the auth area.
func (a *App) ResolveSession(ctx context.Context, token string) (domain.Actor, error) {
	return domain.Actor{}, nil
}
