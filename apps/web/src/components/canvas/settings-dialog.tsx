import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { useAction, useMutation, useQuery } from "convex/react";
import { AlignLeft, Globe, KeyRound, Link2Off, Unplug } from "lucide-react";
import { useMemo } from "react";
import { toast } from "sonner";

import { Palette, type PalettePage } from "@/components/palette";

import { useEnvironment } from "./environment";
import { attempt } from "./errors";

/**
 * Project settings as a command palette (⌘, or the rail's gear). One section today: Logs,
 * which picks where container logs are shipped and read from. Docker (the default) reads
 * `docker service logs` from the manager and ships nothing; Axiom has the worker on every
 * node stream lines to a dataset and the Logs tab query it. Connecting is region → dataset
 * → token, each one ↵; the token is verified before anything is saved.
 */

const REGIONS = [
  { id: "api.axiom.co", label: "United States", hint: "api.axiom.co" },
  { id: "api.eu.axiom.co", label: "European Union", hint: "api.eu.axiom.co" },
] as const;

const DEFAULT_DATASET = (project: string) => `keel-${project}`;

export function SettingsDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const { projectId, projectName } = useEnvironment();
  const sink = useQuery(api.logSinks.get, { projectId });
  const connectAxiom = useAction(api.logSinks.connectAxiom);
  const disconnect = useMutation(api.logSinks.disconnect);

  const root = useMemo<PalettePage>(() => {
    const connect = (domain: string, dataset: string, token: string) => {
      const p = attempt(connectAxiom({ projectId, domain, dataset, token }));
      toast.promise(p, {
        loading: "Checking the token with Axiom…",
        success: (r) => (r ? `Logs now stream to Axiom · ${r.dataset}` : ""),
      });
    };

    const tokenPage = (domain: string, dataset: string): PalettePage => ({
      title: "Token",
      placeholder: "Paste an Axiom API token (ingest + query on the dataset)",
      secret: true,
      items: [],
      onSubmit: (token) => void connect(domain, dataset, token),
      submitLabel: () => "Connect",
      submitHint: `${dataset} · ${domain}`,
      submitIcon: <KeyRound size={14} strokeWidth={1.5} aria-hidden />,
    });

    const datasetPage = (domain: string): PalettePage => ({
      title: "Dataset",
      placeholder: `Dataset name, e.g. ${DEFAULT_DATASET(projectName)}`,
      onSubmit: (dataset) => tokenPage(domain, dataset),
      submitLabel: (dataset) => `Use ${dataset}`,
      submitHint: "Created if it does not exist",
      submitIcon: <AlignLeft size={14} strokeWidth={1.5} aria-hidden />,
      items: [
        {
          id: "default",
          label: DEFAULT_DATASET(projectName),
          hint: "Default · created if it does not exist",
          icon: <AlignLeft size={14} strokeWidth={1.5} aria-hidden />,
          onSelect: () => tokenPage(domain, DEFAULT_DATASET(projectName)),
        },
      ],
    });

    const axiomPage: PalettePage = {
      title: "Axiom",
      placeholder: "Region",
      items: REGIONS.map((r) => ({
        id: r.id,
        label: r.label,
        hint: r.hint,
        icon: <Globe size={14} strokeWidth={1.5} aria-hidden />,
        onSelect: () => datasetPage(r.id),
      })),
    };

    const logsPage: PalettePage = {
      title: "Logs",
      placeholder: "Where do container logs go?",
      items: [
        {
          id: "docker",
          label: "Docker",
          hint: sink
            ? "Read from the manager, ship nothing"
            : "Current · read from the manager, ship nothing",
          icon: <Unplug size={14} strokeWidth={1.5} aria-hidden />,
          onSelect: () => {
            if (sink)
              void attempt(disconnect({ projectId })).then(() => toast("Logs back to Docker"));
          },
        },
        {
          id: "axiom",
          label: "Axiom",
          hint:
            sink?.kind === "axiom"
              ? `Current · ${sink.dataset} · token ${sink.tokenHint}`
              : "Stream every line to a dataset · dashboards, search, retention",
          keywords: ["logs", "observability"],
          icon: <AlignLeft size={14} strokeWidth={1.5} aria-hidden />,
          onSelect: () => axiomPage,
        },
        ...(sink
          ? [
              {
                id: "disconnect",
                label: "Disconnect",
                hint: "Back to Docker; nothing already shipped is deleted",
                icon: <Link2Off size={14} strokeWidth={1.5} aria-hidden />,
                onSelect: () => {
                  void attempt(disconnect({ projectId })).then(() => toast("Logs back to Docker"));
                },
              },
            ]
          : []),
      ],
    };

    return {
      title: "Settings",
      placeholder: "Project settings",
      items: [
        {
          id: "logs",
          label: "Logs",
          hint: sink ? `Axiom · ${sink.dataset}` : "Docker · read from the manager",
          keywords: ["axiom", "sink", "observability"],
          icon: <AlignLeft size={14} strokeWidth={1.5} aria-hidden />,
          onSelect: () => logsPage,
        },
      ],
    };
  }, [sink, projectId, projectName, connectAxiom, disconnect]);

  return <Palette open={open} onOpenChange={onOpenChange} root={root} />;
}
