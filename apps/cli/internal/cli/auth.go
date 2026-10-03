package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ThallesP/keel/apps/cli/internal/config"
	"github.com/ThallesP/keel/apps/cli/internal/keel"
	"github.com/ThallesP/keel/apps/cli/internal/output"
)

type identity struct {
	Instance     string             `json:"instance"`
	URL          string             `json:"url"`
	User         *keel.User         `json:"user"`
	Organization *keel.Organization `json:"organization"`
}

func (a *app) loginCmd() *cobra.Command {
	var email, name, convexURL, siteURL string
	var passwordStdin bool
	cmd := &cobra.Command{
		Use:   "login <dashboard-url>",
		Short: "Sign in to a Keel install with email and password",
		Long: `Sign in to a Keel install and make it the current one.

The Convex URLs are read from the dashboard's /config.js. A dev server has none there; pass
--convex-url and --convex-site-url instead. Prompts for anything missing only when stdin is
a terminal; otherwise pass --email and pipe the password into --password-stdin.

The session is saved in the config file. To use it elsewhere (CI, another machine), pass
it as KEEL_TOKEN together with KEEL_URL; keel token prints it.`,
		Example: `  keel login https://keel.example.ts.net
  printf %s "$PASSWORD" | keel login http://100.101.102.103 --email me@example.com --password-stdin --json`,
		Args: args(1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			webURL, err := normalizeURL(args[0])
			if err != nil {
				return usage(cmd, "%v", err)
			}
			inst := &config.Instance{URL: webURL, ConvexURL: convexURL, ConvexSiteURL: siteURL}
			if inst.ConvexURL == "" || inst.ConvexSiteURL == "" {
				discovered, discoveredSite, err := keel.Discover(ctx, webURL)
				if err != nil {
					return err
				}
				inst.ConvexURL = or(inst.ConvexURL, discovered)
				inst.ConvexSiteURL = or(inst.ConvexSiteURL, discoveredSite)
			}
			inst.ConvexURL = strings.TrimRight(inst.ConvexURL, "/")
			inst.ConvexSiteURL = strings.TrimRight(inst.ConvexSiteURL, "/")

			interactive := output.IsTerminal(os.Stdin)
			stdin := bufio.NewReader(os.Stdin)
			if email == "" {
				if !interactive {
					return usage(cmd, "--email is required when stdin isn't a terminal")
				}
				fmt.Fprint(os.Stderr, "Email: ")
				line, _ := stdin.ReadString('\n')
				email = strings.TrimSpace(line)
			}
			var password string
			switch {
			case passwordStdin:
				b, err := io.ReadAll(stdin)
				if err != nil {
					return usage(cmd, "reading the password from stdin: %v", err)
				}
				password = strings.TrimRight(string(b), "\r\n")
			case interactive:
				fmt.Fprint(os.Stderr, "Password: ")
				b, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(os.Stderr)
				if err != nil {
					return usage(cmd, "reading the password: %v", err)
				}
				password = string(b)
			default:
				return usage(cmd, "stdin isn't a terminal: pipe the password and pass --password-stdin")
			}
			if email == "" || password == "" {
				return usage(cmd, "email and password are both required")
			}

			if inst.Token, err = keel.SignIn(ctx, inst, email, password); err != nil {
				return err
			}
			inst.Email = email
			api, err := keel.Connect(ctx, inst)
			if err != nil {
				return err
			}
			id, err := whoami(cmd, api)
			if err != nil {
				return err
			}

			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			if name == "" {
				name = hostOf(webURL)
			}
			cfg.SetInstance(name, inst)
			cfg.Current = name
			if err := saveConfig(cfg); err != nil {
				return err
			}
			id.Instance, id.URL = name, webURL
			if id.Organization == nil {
				a.out.Warn("this account isn't in the install's organization yet; projects stay empty until a member invites it")
			}
			a.out.Result(id, func(w io.Writer) {
				fmt.Fprintf(w, "Logged in to %s as %s\n", name, id.User.Email)
			})
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&email, "email", "", "account email")
	f.BoolVar(&passwordStdin, "password-stdin", false, "read the password from stdin")
	f.StringVar(&name, "name", "", "name for this install in the config (default: its host)")
	f.StringVar(&convexURL, "convex-url", "", "Convex API URL; skips /config.js discovery")
	f.StringVar(&siteURL, "convex-site-url", "", "Convex HTTP actions URL; skips /config.js discovery")
	return cmd
}

func (a *app) logoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Sign out and forget the install",
		Args:  args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if os.Getenv("KEEL_URL") != "" {
				return usage(cmd, "KEEL_URL is set; logout only removes saved logins (unset KEEL_URL and KEEL_TOKEN instead)")
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			name, inst, err := a.target(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if err := keel.SignOut(cmd.Context(), inst); err != nil {
				a.out.Warn("couldn't revoke the session on the server (%v); forgetting it here anyway", err)
			}
			cfg.RemoveInstance(name)
			if err := saveConfig(cfg); err != nil {
				return err
			}
			a.out.Result(struct {
				Instance string `json:"instance"`
			}{name}, func(w io.Writer) {
				fmt.Fprintf(w, "Logged out of %s\n", name)
			})
			return nil
		},
	}
}

func (a *app) whoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in account, its organization and the install",
		Args:  args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd.Context())
			if err != nil {
				return err
			}
			id, err := whoami(cmd, s.api)
			if err != nil {
				return err
			}
			id.Instance, id.URL = s.name, s.inst.URL
			a.out.Result(id, func(w io.Writer) {
				t := table(w)
				fmt.Fprintf(t, "User\t%s (%s)\n", id.User.Email, id.User.Name)
				if o := id.Organization; o != nil {
					fmt.Fprintf(t, "Organization\t%s (%s)\n", o.Name, o.Role)
				} else {
					fmt.Fprintf(t, "Organization\tnone yet\n")
				}
				fmt.Fprintf(t, "Instance\t%s  %s\n", id.Instance, id.URL)
				t.Flush()
			})
			return nil
		},
	}
}

func (a *app) tokenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print the session token, for KEEL_TOKEN elsewhere",
		Long: `Print the session token of the current install. Pass it as KEEL_TOKEN (with KEEL_URL)
to run keel where you can't log in, such as CI. It is a full session: treat it as a password.`,
		Args: args(0, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			_, inst, err := a.target(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if inst.Token == "" {
				return output.Errorf(output.CodeNotAuthenticated, "keel login "+inst.URL, "Not logged in")
			}
			a.out.Result(struct {
				Token string `json:"token"`
			}{inst.Token}, func(w io.Writer) {
				fmt.Fprintln(w, inst.Token)
			})
			return nil
		},
	}
}

func whoami(cmd *cobra.Command, api *keel.API) (*identity, error) {
	user, err := api.CurrentUser(cmd.Context())
	if err != nil {
		return nil, err
	}
	org, err := api.Organization(cmd.Context())
	if err != nil {
		return nil, err
	}
	return &identity{User: user, Organization: org}, nil
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
