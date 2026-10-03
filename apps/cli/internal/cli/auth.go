package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

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

// loginResult is what keel login prints once signed in.
type loginResult struct {
	Status string `json:"status"` // "loggedIn"
	*identity
}

// pendingResult is what keel login prints while its link waits for an approval.
type pendingResult struct {
	Status      string    `json:"status"` // "pending"
	Instance    string    `json:"instance"`
	URL         string    `json:"url"`
	ApprovalURL string    `json:"approvalUrl"`
	Code        string    `json:"code"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Next        string    `json:"next"`
}

func (a *app) loginCmd() *cobra.Command {
	var name, convexURL, siteURL string
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

Without a URL it logs in again to the install commands use. A new install's Convex URLs are
read from the dashboard's /config.js. A dev server has none there; pass --convex-url and
--convex-site-url instead.

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
			name, inst, err := a.loginTarget(cmd, cfg, args, name, convexURL, siteURL)
			if err != nil {
				return err
			}
			cfg.SetInstance(name, inst)
			cfg.Current = name

			var token string
			if inst.Token != "" {
				// Already logged in, unless the session is gone.
				api, err := keel.Connect(ctx, inst)
				if errCode(err) == output.CodeNotAuthenticated {
					inst.Token = ""
				} else if err != nil {
					return err
				} else {
					return a.loggedIn(cmd, cfg, name, inst, api)
				}
			}
			if p := inst.Pending; p != nil && !p.Expired() {
				// The same link again, unless it was approved or turned down meanwhile.
				token, _, err = keel.PollLogin(ctx, inst)
				if errCode(err) == output.CodeNotAuthenticated {
					inst.Pending = nil
				} else if err != nil {
					return err
				}
			}
			if token == "" && (inst.Pending == nil || inst.Pending.Expired()) {
				if inst.Pending, err = keel.StartLogin(ctx, inst); err != nil {
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
				if token, err = waitForApproval(ctx, inst); err != nil {
					return err
				}
			}

			inst.Token, inst.Pending = token, nil
			api, err := keel.Connect(ctx, inst)
			if err != nil {
				return err
			}
			return a.loggedIn(cmd, cfg, name, inst, api)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&wait, "wait", false, "wait for the approval even without a terminal")
	f.BoolVar(&noWait, "no-wait", false, "print the link and return, even in a terminal")
	f.StringVar(&name, "name", "", "name for this install in the config (default: its host)")
	f.StringVar(&convexURL, "convex-url", "", "Convex API URL; skips /config.js discovery")
	f.StringVar(&siteURL, "convex-site-url", "", "Convex HTTP actions URL; skips /config.js discovery")
	return cmd
}

// loginTarget is the install keel login signs in to: the dashboard URL given, else the one
// commands use. A known install keeps its Convex URLs and login state; a new one reads its
// URLs from /config.js.
func (a *app) loginTarget(cmd *cobra.Command, cfg *config.Config, args []string, name, convexURL, siteURL string) (string, *config.Instance, error) {
	ctx := cmd.Context()
	var inst *config.Instance
	if len(args) == 0 {
		if os.Getenv("KEEL_URL") != "" || len(cfg.Instances) == 0 {
			return "", nil, usage(cmd, "pass the dashboard URL: keel login <dashboard-url>")
		}
		var err error
		if name, inst, err = a.target(ctx, cfg); err != nil {
			return "", nil, err
		}
	} else {
		webURL, err := normalizeURL(args[0])
		if err != nil {
			return "", nil, usage(cmd, "%v", err)
		}
		name = or(name, hostOf(webURL))
		if inst = cfg.Instances[name]; inst == nil || inst.URL != webURL {
			inst = &config.Instance{URL: webURL}
		}
	}
	inst.ConvexURL = or(convexURL, inst.ConvexURL)
	inst.ConvexSiteURL = or(siteURL, inst.ConvexSiteURL)
	if inst.ConvexURL == "" || inst.ConvexSiteURL == "" {
		discovered, discoveredSite, err := keel.Discover(ctx, inst.URL)
		if err != nil {
			return "", nil, err
		}
		inst.ConvexURL = or(inst.ConvexURL, discovered)
		inst.ConvexSiteURL = or(inst.ConvexSiteURL, discoveredSite)
	}
	inst.ConvexURL = strings.TrimRight(inst.ConvexURL, "/")
	inst.ConvexSiteURL = strings.TrimRight(inst.ConvexSiteURL, "/")
	return name, inst, nil
}

// waitForApproval polls a pending login until it is approved, turned down, expired or
// interrupted, and returns the session token.
func waitForApproval(ctx context.Context, inst *config.Instance) (string, error) {
	interval := time.Duration(inst.Pending.Interval) * time.Second
	for {
		select {
		case <-ctx.Done():
			return "", output.Errorf(output.CodeCancelled, "keel login --wait (the link stays valid)", "Stopped waiting")
		case <-time.After(interval):
		}
		token, slowDown, err := keel.PollLogin(ctx, inst)
		if err != nil || token != "" {
			return token, err
		}
		if slowDown { // RFC 8628 §3.5
			interval += 5 * time.Second
		}
	}
}

// loggedIn saves a working session and prints who it is.
func (a *app) loggedIn(cmd *cobra.Command, cfg *config.Config, name string, inst *config.Instance, api *keel.API) error {
	id, err := whoami(cmd, api)
	if err != nil {
		return err
	}
	inst.Email = id.User.Email
	if err := saveConfig(cfg); err != nil {
		return err
	}
	id.Instance, id.URL = name, inst.URL
	if id.Organization == nil {
		a.out.Warn("this account isn't in the install's organization yet; projects stay empty until a member invites it")
	}
	a.out.Result(loginResult{"loggedIn", id}, func(w io.Writer) {
		fmt.Fprintf(w, "Logged in to %s as %s\n", name, id.User.Email)
	})
	return nil
}

// prettyCode is a user code as the dashboard shows it: ABCD-EFGH.
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
			name, inst, err := a.target(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			if inst.Token == "" {
				// Only a pending login: nothing to revoke.
			} else if err := keel.SignOut(cmd.Context(), inst); err != nil {
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
			name, inst, err := a.target(cmd.Context(), cfg)
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
