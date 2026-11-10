# Image registry

Every install runs one image registry on its control plane. It is internal: Keel pushes the images it builds there and every server pulls from it. It is not a dashboard feature. You only touch it to push an image by hand.

Builds do not exist yet (`docs/workers.md`, "Builds and registry"), so today the only writer is you, through `keel registry login`.

## Shape

| What        | Where                                                                                                   |
| ----------- | ------------------------------------------------------------------------------------------------------- |
| Server      | [zot](https://zotregistry.dev) (`ghcr.io/project-zot/zot-minimal`, ~33 MB), service `registry` in `deploy/compose.yml` |
| Address     | `<tailnet-ip>:5000`, bound to the tailnet IP only, like the dashboard                                   |
| Images      | volume `keel_registry-data`                                                                             |
| Config      | `/opt/keel/registry/` on the control plane: `ca.crt`, `ca.key`, `tls.crt`, `tls.key`, `htpasswd`, `config.json` |
| Account     | one user, `keel`; the password is `KEEL_REGISTRY_PASSWORD` in `/opt/keel/.env`, generated once          |
| Formats     | OCI, and Docker's own manifests (`compat: docker2s2`), which `docker push` from Docker's classic image store sends |
| Retention   | the 10 most recently pushed tags of each repository; untagged manifests go after 24 h, then GC          |
| Image names | `<tailnet-ip>:5000/<project>/<service>:<tag>` by convention; Keel deploys by digest once it builds       |

Why it is on the control plane and not a hosted registry: the servers pulling from it are one tailnet hop away, usually in the same region or LAN, and only changed layers move. A central registry far from the servers is what makes Railway's pushes and pulls take tens of seconds.

## Who writes what

- `keel serve` writes everything in `/opt/keel/registry/` on start (`internal/registry`): a CA (once, kept forever), a certificate for the registry address signed by it (reissued when the address changes or less than a year is left of its five), the `htpasswd` line for `keel` (bcrypt at minimum cost: the password is 256 random bits), and zot's `config.json`. zot reads that directory read-only and reloads the certificate when it changes; `install.sh` restarts it too when a run reissued it.
- `install.sh` generates `KEEL_REGISTRY_PASSWORD`, creates the directory, and copies `ca.crt` to `/etc/docker/certs.d/<tailnet-ip>:5000/ca.crt` so the control plane's Docker trusts the registry. Docker reads that directory on every request: no `daemon.json`, no restart.
- `keel serve` passes the credentials to Docker on every pull and Swarm service create/update for an image under the registry's address (`X-Registry-Auth`, `internal/adapters/swarm`). Swarm hands them to whichever server runs the task. No server runs `docker login`.

## Why Keel's own CA and not Tailscale HTTPS

Docker only talks plain HTTP to `localhost`; any other registry needs a certificate it trusts, or `insecure-registries` plus a daemon restart on every server. Tailscale HTTPS certificates would need a person to turn HTTPS on for the tailnet first (the dashboard asks for that, but Keel must not depend on it to run images). A CA Keel generates needs nothing from anyone, and the tailnet already encrypts the traffic.

## Other servers

A server that joins the Swarm needs the same CA file, or its tasks fail to pull with `x509: certificate signed by unknown authority`. There is no join flow yet (`docs/workers.md`, "Node join flow"); until there is, on the new server:

```bash
sudo mkdir -p /etc/docker/certs.d/<tailnet-ip>:5000
sudo tee /etc/docker/certs.d/<tailnet-ip>:5000/ca.crt   # paste the control plane's /opt/keel/registry/ca.crt, then Ctrl-D
```

The join flow will write it.

## From your machine

```bash
keel registry login                                    # trusts the CA, runs docker login
docker tag api:dev <tailnet-ip>:5000/acme/api:dev
docker push <tailnet-ip>:5000/acme/api:dev
keel service create api --image <tailnet-ip>:5000/acme/api:dev && keel ship
```

`keel registry login` (`GET /api/registry`, any member of the organization) writes the CA to `/etc/docker/certs.d/<registry>/ca.crt`, or `~/.docker/certs.d/` under Docker Desktop. When it cannot write there, it saves the CA next to its config and fails with `DOCKER_ERROR` and the `sudo install …` command as the fix. The machine has to be on the tailnet.

## Limits

- One account for everything, readable by every member through the API. Scoped push and pull tokens come with builds.
- Losing `keel_registry-data` does not stop running services (each server keeps its copy of the images it runs), but rescheduling a task onto another server, or a rollback to an image the server no longer has, fails until the image is pushed again.
- A rollback further back than the 10 kept tags fails the same way.
- The address is the control plane's tailnet IP. If it changes, image references in services' specs point at the old address.
