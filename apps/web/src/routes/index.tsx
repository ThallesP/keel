import { Button } from "@my-better-t-app/ui/components/button";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";

import { ensureDefaultProject } from "@/gen/api";
import { AuthShell } from "@/components/auth/shell";
import { AuthForms } from "@/components/auth-forms";
import Loader from "@/components/loader";
import { errorMessage } from "@/lib/api";
import { SessionGate, useAuth } from "@/lib/session";

export const Route = createFileRoute("/")({ component: Index });

function Bootstrap() {
  const navigate = useNavigate();
  const auth = useAuth();
  const [error, setError] = useState<string | null>(null);
  const started = useRef(false);
  useEffect(() => {
    if (started.current) return;
    started.current = true;
    ensureDefaultProject({})
      .unwrap()
      .then(({ slug }) =>
        navigate({ to: "/p/$projectId", params: { projectId: slug }, replace: true }),
      )
      .catch((err: unknown) => setError(errorMessage(err)));
  }, [navigate]);
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
