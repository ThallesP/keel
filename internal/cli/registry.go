package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/api"
	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
)

func (a *app) registryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "registry",
		Short: "Use the install's image registry from this machine",
		Long: `Every Keel install runs an image registry on its control plane, reachable over the tailnet.
Keel pushes the images it builds there and its servers pull from it; you only need it to push
an image by hand. See docs/registry.md.`,
	}
	cmd.AddCommand(a.registryLoginCmd())
	return cmd
}

func (a *app) registryLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "login",
		Short: "Let this machine's Docker push to and pull from the registry",
		Long: `Trust the registry's certificate authority and run docker login with the install's
credentials. Docker reads the CA from /etc/docker/certs.d/<registry>/ca.crt (Docker Desktop:
~/.docker/certs.d). When this user cannot write there, the CA is saved next to the CLI's
config and the error's fix is the sudo command that installs it.`,
		Example: `  keel registry login
  docker tag api:dev 100.64.0.7:5000/acme/api:dev
  docker push 100.64.0.7:5000/acme/api:dev`,
		Args: args(0, 0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := a.connect(ctx)
			if err != nil {
				return err
			}
			reg, err := s.api.Registry(ctx)
			if err != nil {
				return err
			}
			if _, err := exec.LookPath("docker"); err != nil {
				return output.Errorf(output.CodeDocker, "install Docker: https://docs.docker.com/get-docker/", "docker is not on PATH")
			}
			caPath, err := trustRegistry(ctx, reg)
			if err != nil {
				return err
			}
			if err := dockerLogin(ctx, reg); err != nil {
				return err
			}
			a.out.Result(struct {
				Registry string `json:"registry"`
				Username string `json:"username"`
				CAPath   string `json:"caPath"`
			}{reg.Host, reg.Username, caPath}, func(w io.Writer) {
				fmt.Fprintf(w, "Logged in to %s.\nPush an image as %s/<project>/<name>:<tag>.\n", reg.Host, reg.Host)
			})
			return nil
		},
	}
}

func trustRegistry(ctx context.Context, reg api.Registry) (string, error) {
	dir, err := dockerCertsDir(ctx)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, reg.Host, "ca.crt")
	if current, err := os.ReadFile(path); err == nil && string(current) == reg.CA {
		return path, nil
	}
	err = writeCA(path, reg.CA)
	if err == nil {
		return path, nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return "", err
	}
	keelDir, err := config.Dir()
	if err != nil {
		return "", err
	}
	saved := filepath.Join(keelDir, "registry", reg.Host, "ca.crt")
	if err := writeCA(saved, reg.CA); err != nil {
		return "", err
	}
	return "", output.Errorf(output.CodeDocker,
		fmt.Sprintf("sudo install -D -m 644 %s %s, then run keel registry login again", saved, path),
		"Docker reads registry CAs from %s, which this user cannot write", dir)
}

func dockerCertsDir(ctx context.Context) (string, error) {
	var stderr bytes.Buffer
	info := exec.CommandContext(ctx, "docker", "info", "--format", "{{.OperatingSystem}}")
	info.Stderr = &stderr
	system, err := info.Output()
	if err != nil {
		return "", output.Errorf(output.CodeDocker, "start Docker, then run keel registry login again",
			"docker info failed: %s", strings.TrimSpace(stderr.String()))
	}
	if !strings.Contains(string(system), "Docker Desktop") {
		return "/etc/docker/certs.d", nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".docker", "certs.d"), nil
}

func writeCA(path, ca string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(ca), 0o644)
}

func dockerLogin(ctx context.Context, reg api.Registry) error {
	login := exec.CommandContext(ctx, "docker", "login", reg.Host, "--username", reg.Username, "--password-stdin")
	login.Stdin = strings.NewReader(reg.Password)
	out, err := login.CombinedOutput()
	if err != nil {
		return output.Errorf(output.CodeDocker, "check that this machine is on the install's tailnet and reaches "+reg.Host,
			"docker login %s failed: %s", reg.Host, strings.TrimSpace(string(out)))
	}
	return nil
}
