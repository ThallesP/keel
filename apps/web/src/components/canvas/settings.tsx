import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { Button } from "@my-better-t-app/ui/components/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@my-better-t-app/ui/components/dialog";
import { useMutation, useQuery } from "convex/react";
import { useState } from "react";
import { toast } from "sonner";

import { attempt } from "./errors";
import { AxiomMark, SignInButton } from "./observability/axiom-gate";
import { PageHeader, Spinner } from "./primitives";

/**
 * The rail's Settings page (`?view=settings`): what is set once and rarely touched, kept off the
 * pages used every day. So far where the organization's logs and traces go; a service's own
 * settings are the bottom panel's Settings tab.
 */
export function SettingsPage() {
  return (
    <div className="flex h-full flex-col bg-bg">
      <PageHeader title="Settings" />
      <div className="min-h-0 flex-1 overflow-auto">
        <div className="mx-auto w-full max-w-[640px] px-6 py-8">
          <ObservabilitySettings />
        </div>
      </div>
    </div>
  );
}

/**
 * The organization's sink (docs/logs.md): Axiom with its org and datasets, or Docker only. Sign
 * in with Axiom lands back on Observability, where a pending org picker shows.
 */
function ObservabilitySettings() {
  const sink = useQuery(api.logSinks.get, {});
  const organization = useQuery(api.organizations.current);
  const [confirming, setConfirming] = useState(false);
  const projects = organization ? `every project in ${organization.name}` : "every project";

  return (
    <section>
      <h2 className="text-sm font-medium text-ink">Observability</h2>
      <p className="mt-1 text-xs text-muted-foreground">
        Where the logs and traces of {projects} go.
      </p>
      <div className="mt-3 rounded-lg border border-line">
        {sink === undefined ? (
          <div className="flex h-14 items-center justify-center">
            <Spinner />
          </div>
        ) : sink?.kind === "axiom" ? (
          <>
            <div className="flex h-14 items-center gap-2.5 px-4">
              <AxiomMark size={16} />
              <span className="text-sm font-medium text-ink">Axiom</span>
              {sink.org && (
                <span className="min-w-0 truncate text-sm text-muted-foreground">{sink.org}</span>
              )}
              <button
                type="button"
                onClick={() => setConfirming(true)}
                className="ml-auto flex h-7 shrink-0 items-center rounded-md border border-line px-2.5 text-2xs text-ink hover:border-danger hover:text-danger"
              >
                Disconnect…
              </button>
            </div>
            <Dataset label="Logs" name={sink.dataset} />
            <Dataset label="Traces" name={sink.traces} />
          </>
        ) : (
          <div className="flex h-14 items-center justify-between gap-3 px-4">
            <span className="text-xs text-muted-foreground">
              Not connected: logs come from Docker, and there are no traces.
            </span>
            <SignInButton compact />
          </div>
        )}
      </div>
      <DisconnectDialog open={confirming} onOpenChange={setConfirming} projects={projects} />
    </section>
  );
}

/** One of the sink's datasets; a sink from before traces has no traces dataset yet. */
function Dataset({ label, name }: { label: string; name: string | null }) {
  return (
    <div className="flex h-10 items-center gap-3 border-t border-line px-4 text-xs">
      <span className="w-14 shrink-0 text-muted-foreground">{label}</span>
      {name ? (
        <span className="truncate font-mono text-ink">{name}</span>
      ) : (
        <>
          <span className="truncate text-faint">None: this connection predates traces.</span>
          <span className="ml-auto">
            <SignInButton compact />
          </span>
        </>
      )}
    </div>
  );
}

/** Disconnect turns Axiom off for every project at once, so it asks first. */
function DisconnectDialog({
  open,
  onOpenChange,
  projects,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  projects: string;
}) {
  const disconnect = useMutation(api.logSinks.disconnect);
  const [busy, setBusy] = useState(false);
  const confirm = async () => {
    setBusy(true);
    const ok = (await attempt(disconnect({}))) !== undefined;
    setBusy(false);
    if (!ok) return;
    onOpenChange(false);
    toast("Axiom disconnected for every project");
  };
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Disconnect Axiom?</DialogTitle>
          <DialogDescription>
            Logs of {projects} go back to Docker, and traced services stop getting traces. What
            already reached Axiom stays there.
          </DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button variant="destructive" disabled={busy} onClick={() => void confirm()}>
            Disconnect
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
