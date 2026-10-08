import { Button } from "@my-better-t-app/ui/components/button";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

import { ensureDefaultProject } from "@/api/gen";
import { AuthShell } from "@/components/auth/shell";
import { AuthForms } from "@/components/auth-forms";
import Loader from "@/components/loader";
import { errorMessage } from "@/lib/api";
import { SessionGate, useAuth, useSession } from "@/lib/session";

export const Route = createFileRoute("/")({ component: Index });

/**
 * First visit: founds the organization if there is none, creates the first project, then jumps
 * to its canvas. An account whose invite did not go through has no organization to land in and
 * is told so (`POST /api/projects/default`).
 */
function Bootstrap() {
  const navigate = useNavigate();
  const auth = useAuth();
  const { organization } = useSession();
  const [error, setError] = useState<string | null>(null);
  // Once per mount: the call founds the organization the first time.
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    const founding = organization === null;
    ensureDefaultProject({})
      .unwrap()
      .then(async ({ slug }) => {
        // Founding put the caller in an organization: the cache and the socket must follow.
        if (founding) await auth.refreshSession();
        await navigate({ to: "/p/$projectId", params: { projectId: slug }, replace: true });
      })
      .catch((err: unknown) => setError(errorMessage(err)));
  }, [auth, navigate, organization]);
  if (!error) return <Loader />;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-sm text-muted-foreground">
      <span>{error}</span>
      <Button
        variant="outline"
        size="sm"
        onClick={() => void auth.signOut().catch((err: unknown) => toast.error(errorMessage(err)))}
      >
        Sign out
      </Button>
    </div>
  );
}

function Index() {
  return (
    <div className="h-svh bg-canvas">
      <SessionGate
        loading={<Loader />}
        signedOut={
          <AuthShell>
            <AuthForms />
          </AuthShell>
        }
      >
        <Bootstrap />
      </SessionGate>
    </div>
  );
}
