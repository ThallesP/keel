import { Outlet, createFileRoute } from "@tanstack/react-router";

import { AuthShell } from "@/components/auth/shell";
import { AuthForms } from "@/components/auth-forms";
import Loader from "@/components/loader";
import { SessionGate } from "@/lib/session";

export const Route = createFileRoute("/_auth")({
  component: AuthLayout,
});

/** Every route under it needs a session; signed out, the same URL shows the sign-in form. */
function AuthLayout() {
  return (
    <SessionGate
      loading={
        <div className="h-svh bg-canvas">
          <Loader />
        </div>
      }
      signedOut={
        <AuthShell>
          <AuthForms />
        </AuthShell>
      }
    >
      <Outlet />
    </SessionGate>
  );
}
