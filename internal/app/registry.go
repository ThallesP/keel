package app

import "github.com/ThallesP/keel/internal/domain"

func (a *App) RegistryLogin(actor domain.Actor) (Registry, error) {
	if err := actor.RequireMember(); err != nil {
		return Registry{}, err
	}
	if a.Config.Registry.Host == "" {
		return Registry{}, domain.E(domain.CodeUnavailable, "This install has no registry. Re-run install.sh to add it.")
	}
	return a.Config.Registry, nil
}
