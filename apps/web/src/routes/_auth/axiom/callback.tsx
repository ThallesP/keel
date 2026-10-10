import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Loader2 } from "lucide-react";
import { useEffect, useRef } from "react";
import { toast } from "sonner";

import { useCompleteAxiomSignIn } from "@/gen/api";
import { errorMessage } from "@/lib/api";
import { takeAxiomReturn } from "@/lib/axiom-sign-in";

type Search = { code?: string; state?: string; error?: string; error_description?: string };

const str = (x: unknown) => (typeof x === "string" ? x : undefined);

/** Axiom's OAuth redirect lands here; the code goes to the control plane, then back to Observability. */
export const Route = createFileRoute("/_auth/axiom/callback")({
  component: AxiomCallback,
  validateSearch: (s: Record<string, unknown>): Search => ({
    code: str(s.code),
    state: str(s.state),
    error: str(s.error),
    error_description: str(s.error_description),
  }),
});

function AxiomCallback() {
  const search = Route.useSearch();
  const navigate = useNavigate();
  const { mutateAsync: signIn } = useCompleteAxiomSignIn();
  // Exactly once: `state` is single-use on the server.
  const ran = useRef(false);

  useEffect(() => {
    if (ran.current) return;
    ran.current = true;
    const from = takeAxiomReturn();
    const back = () =>
      from
        ? navigate({
            to: "/p/$projectId",
            params: { projectId: from.slug },
            search: { view: "observability" },
            replace: true,
          })
        : navigate({ to: "/", replace: true });

    if (search.error || !search.code || !search.state) {
      toast.error(`Axiom: ${search.error_description || search.error || "no code returned"}`);
      void back();
      return;
    }
    signIn({ body: { state: search.state, code: search.code } })
      .then((r) => {
        if (!r.choose) {
          toast.success(
            `Every project's logs and traces now go to Axiom · ${r.org} · ${r.dataset}`,
          );
        }
      })
      .catch((err) => toast.error(errorMessage(err)))
      .finally(() => void back());
  }, [search, navigate, signIn]);

  return (
    <div className="flex h-svh flex-col items-center justify-center gap-2 bg-canvas text-sm text-muted-foreground">
      <Loader2 className="animate-spin text-faint" aria-hidden />
      <span>Connecting Axiom…</span>
    </div>
  );
}
