import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { Button } from "@my-better-t-app/ui/components/button";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { Authenticated, AuthLoading, Unauthenticated, useMutation } from "convex/react";
import { useEffect, useState } from "react";

import { AuthForms } from "@/components/auth-forms";
import { errorMessage } from "@/components/canvas/errors";
import Loader from "@/components/loader";
import { authClient } from "@/lib/auth-client";

export const Route = createFileRoute("/")({ component: Index });

/**
 * First visit: founds the organization if there is none, creates the first project, then jumps
 * to its canvas. An account whose invite did not go through has no organization to land in and
 * is told so (projects.ensureDefault).
 */
function Bootstrap() {
  const ensureDefault = useMutation(api.projects.ensureDefault);
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    ensureDefault({})
      .then((slug) => navigate({ to: "/p/$projectId", params: { projectId: slug }, replace: true }))
      .catch((err: unknown) => setError(errorMessage(err)));
  }, [ensureDefault, navigate]);
  if (!error) return <Loader />;
  return (
    <div className="flex h-full flex-col items-center justify-center gap-3 text-sm text-muted-foreground">
      <span>{error}</span>
      <Button variant="outline" size="sm" onClick={() => void authClient.signOut()}>
        Sign out
      </Button>
    </div>
  );
}

function Index() {
  return (
    <div className="h-svh bg-canvas">
      <Authenticated>
        <Bootstrap />
      </Authenticated>
      <Unauthenticated>
        <div className="flex h-full items-center justify-center">
          <AuthForms />
        </div>
      </Unauthenticated>
      <AuthLoading>
        <Loader />
      </AuthLoading>
    </div>
  );
}
