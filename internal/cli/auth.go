package cli

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/cli/client"
	"github.com/ThallesP/keel/internal/cli/config"
	"github.com/ThallesP/keel/internal/cli/output"
)

type identity struct {
	Instance     string               `json:"instance"`
	URL          string               `json:"url"`
	User         *client.User         `json:"user"`
	Organization *client.Organization `json:"organization"`
}

type loginResult struct {
	Status string `json:"status"`
	*identity
}

type pendingResult struct {
	Status      string    `json:"status"`
	Instance    string    `json:"instance"`
	URL         string    `json:"url"`
	ApprovalURL string    `json:"approvalUrl"`
	Code        string    `json:"code"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Next        string    `json:"next"`
}

func (a *app) loginCmd() *cobra.Command {
	var name string
	var wait, noWait bool
	cmd := &cobra.Command{
		Use:   "login [dashboard-url]",
		Short: "Sign in to a Keel install by approving a link in its dashboard",
		Long: `Sign in to a Keel install and make it the current one.

keel login prints a link to the install's dashboard. Whoever opens it signed in and approves
gives this CLI a session as their account. If you are an agent, send the link to your human.

With a terminal, keel login waits for the approval. Without one, or with --json or --no-wait,
it prints the link and returns: carry on, and the first keel command after the approval
finishes the login (until then commands fail with AUTHORIZATION_PENDING). --wait blocks until
approved instead. Running keel login again while the link is valid prints the same link;
when already logged in it only says so (keel logout first to switch accounts).

Without a URL it logs in again to the install commands use. The URL is the one you open the
dashboard at; the API is on the same address (keel login checks it at /api/meta).

The session is saved in the config file. To use it elsewhere (CI, another machine), pass it as
KEEL_TOKEN together with KEEL_URL; keel token prints it.`,
		Example: `  keel login https://keel.example.ts.net
  keel login https://keel.example.ts.net --json    # prints the link and returns
  keel login --wait                                # block until the link is approved`,
		Args: args(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if wait && noWait {
				return usage(cmd, "--wait and --no-wait are exclusive")
			}
			cfg, err := a.loadConfig()
			if err != nil {
				return err
			}
			name, inst, err := a.loginTarget(cmd, cfg, args, name)
			if err != nil {
				return err
			}
			cfg.SetInstance(name, inst)
			cfg.Current = name
			c := client.New(inst.URL, "")

			var token string
			if inst.Token != "" {
				s, err := dial(ctx, cfg, name, inst)
				if output.CodeOf(err) == output.CodeNotAuthenticated {
					inst.Token = ""
				} else if err != nil {
					return err
				} else {
					return a.loggedIn(s)
				}
			}
			if p := inst.Pending; p != nil && !p.Expired() {
				token, _, err = c.PollLogin(ctx, p.DeviceCode)
				if output.CodeOf(err) == output.CodeNotAuthenticated {
					inst.Pending = nil
				} else if err != nil {
					return err
				}
			}
			if token == "" && (inst.Pending == nil || inst.Pending.Expired()) {
				if inst.Pending, err = c.StartLogin(ctx); err != nil {
					return err
				}
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}

			if token == "" {
				p := inst.Pending
				if !wait && (noWait || a.out.JSON || !output.IsTerminal(os.Stdin)) {
					next := "Send approvalUrl to a person to approve, then carry on: the next keel command finishes the login (or wait for it: keel login --wait)"
					a.out.Result(pendingResult{"pending", name, inst.URL, p.URL, prettyCode(p.UserCode), p.ExpiresAt, next},
						func(w io.Writer) {
							fmt.Fprintf(w, "To log in to %s, open this link and approve (if you are an agent, send it to your human):\n\n  %s\n\n", name, p.URL)
							fmt.Fprintf(w, "Code %s, valid until %s. The next keel command after the approval finishes the login; keel login --wait waits for it.\n",
								prettyCode(p.UserCode), p.ExpiresAt.Local().Format("15:04"))
						})
					return nil
				}
				a.out.Progress("Open this link and approve to log in to %s:\n\n  %s\n\nCode %s. Waiting for the approval (Ctrl-C stops waiting; the link stays valid until %s)…",
					name, p.URL, prettyCode(p.UserCode), p.ExpiresAt.Local().Format("15:04"))
				if token, err = waitForApproval(ctx, c, p); err != nil {
					return err
				}
			}

			inst.Token, inst.Pending = token, nil
			if err := saveConfig(cfg); err != nil {
				return err
			}
			s, err := dial(ctx, cfg, name, inst)
			if err != nil {
				return err
			}
			return a.loggedIn(s)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&wait, "wait", false, "wait for the approval even without a terminal")
	f.BoolVar(&noWait, "no-wait", false, "print the link and return, even in a terminal")
	f.StringVar(&name, "name", "", "name for this install in the config (default: its host)")
	return cmd
}

func (a *app) loginTarget(cmd *cobra.Command, cfg *config.Config, args []string, name string) (string, *config.Instance, error) {
	var inst *config.Instance
	if len(args) == 0 {
		if os.Getenv("KEEL_URL") != "" || len(cfg.Instances) == 0 {
			return "", nil, usage(cmd, "pass the dashboard URL: keel login <dashboard-url>")
		}
		var err error
		if name, _, err = a.target(cfg); err != nil {
			return "", nil, err
		}
		inst = cfg.Instances[name]
	} else {
		webURL, err := normalizeURL(args[0])
		if err != nil {
			return "", nil, usage(cmd, "%v", err)
		}
		name = cmp.Or(name, hostOf(webURL))
		if inst = cfg.Instances[name]; inst == nil || inst.URL != webURL {
			inst = &config.Instance{URL: webURL}
		}
	}
	if _, err := client.Discover(cmd.Context(), inst.URL); err != nil {
		return "", nil, err
	}
	return name, inst, nil
}

func waitForApproval(ctx context.Context, c *client.Client, p *config.PendingLogin) (string, error) {
	interval := time.Duration(p.Interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return "", output.Errorf(output.CodeCancelled, "keel login --wait (the link stays valid)", "Stopped waiting")
		case <-time.After(interval):
		}
		token, slowDown, err := c.PollLogin(ctx, p.DeviceCode)
		if err != nil || token != "" {
			return token, err
		}
		if slowDown {
			interval += 5 * time.Second
		}
	}
}

func (a *app) loggedIn(s *session) error {
	s.inst.Email = s.user.Email
	if err := saveConfig(s.cfg); err != nil {
		return err
	}
	id := s.identity()
	if id.Organization == nil {
		a.out.Warn("this account isn't in the install's organization yet; projects stay empty until a member invites it")
	}
	a.out.Result(loginResult{"loggedIn", id}, func(w io.Writer) {
		fmt.Fprintf(w, "Logged in to %s as %s\n", s.name, id.User.Email)
	})
	return nil
}

func (s *session) identity() *identity {
	return &identity{Instance: s.name, URL: s.inst.URL, User: s.user, Organization: s.org}
}

func prettyCode(c string) string {
	if len(c) == 8 {
		return c[:4] + "-" + c[4:]
	}
	return c
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
			name, inst, err := a.target(cfg)
			if err != nil {
				return err
			}
			if inst.Token != "" {
				if err := client.New(inst.URL, inst.Token).SignOut(cmd.Context()); err != nil {
					a.out.Warn("couldn't revoke the session on the server (%v); forgetting it here anyway", err)
				}
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
			id := s.identity()
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
			name, inst, err := a.target(cfg)
			if err != nil {
				return err
			}
			if err := a.finishLogin(cmd.Context(), cfg, name, inst); err != nil {
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
