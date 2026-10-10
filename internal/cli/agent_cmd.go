package cli

import (
	"github.com/spf13/cobra"

	"github.com/ThallesP/keel/internal/agent"
)

func init() {
	Extra = append(Extra, func() *cobra.Command {
		return &cobra.Command{
			Use:   "agent",
			Short: "Run the node agent (Docker events to the control plane, container logs to the sink)",
			Long: "Runs on every Swarm node as a global service. Outbound only, read-only on Docker.\n\n" +
				"Environment: KEEL_URL (required), KEEL_WORKER_TOKEN or /run/secrets/keel_worker_token,\n" +
				"KEEL_STATE (/var/lib/keel-agent/state.json), KEEL_CONFIG_POLL_MS (30000),\n" +
				"DOCKER_SOCKET (/var/run/docker.sock), KEEL_TS_AUTHKEY (optional: reach KEEL_URL through\n" +
				"an embedded, ephemeral Tailscale node keel-agent-<hostname> instead of the host network).",
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return agent.Main(cmd.Context())
			},
		}
	})
}
