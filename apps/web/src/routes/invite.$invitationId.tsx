import { api } from "@my-better-t-app/backend/convex/_generated/api";
import { Button } from "@my-better-t-app/ui/components/button";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Authenticated, AuthLoading, Unauthenticated, useQuery } from "convex/react";
import { useState } from "react";
import { toast } from "sonner";

import Loader from "@/components/loader";
import SignUpForm from "@/components/sign-up-form";
import { authClient } from "@/lib/auth-client";

/**
 * An invite link, made in Account → Invite people. Signed out: the sign-up form for the invited
 * email. Signed in as that email: one click to join. Sign-up elsewhere is closed once the first
 * account exists (convex/auth.ts), so this is how everyone else gets in.
 */
export const Route = createFileRoute("/invite/$invitationId")({ component: InvitePage });

const card =
  "w-full max-w-sm rounded-lg border border-line bg-bg p-8 shadow-[0_1px_2px_rgba(11,18,32,0.05),0_4px_12px_rgba(11,18,32,0.04)]";

function Accept({
  invitationId,
  invitation,
  onDone,
}: {
  invitationId: string;
  invitation: { email: string; organization: string };
  onDone: () => void;
}) {
  const user = useQuery(api.auth.getCurrentUser);
  const [busy, setBusy] = useState(false);
  if (user === undefined) return <Loader />;
  const matches = user?.email.toLowerCase() === invitation.email.toLowerCase();
  const join = async () => {
    setBusy(true);
    const { error } = await authClient.organization.acceptInvitation({ invitationId });
    setBusy(false);
    if (error) toast.error(error.message ?? "Could not accept the invitation");
    else onDone();
  };
  return (
    <div className={card}>
      <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">
        Join {invitation.organization}
      </h1>
      {matches ? (
        <>
          <p className="mb-6 text-xs text-muted-foreground">
            You are signed in as {user?.email}. Join to see its projects.
          </p>
          <Button className="w-full" disabled={busy} onClick={() => void join()}>
            {busy ? "Joining…" : "Join"}
          </Button>
        </>
      ) : (
        <>
          <p className="mb-6 text-xs text-muted-foreground">
            This invite is for {invitation.email}, but you are signed in as {user?.email}. Sign out
            to create that account.
          </p>
          <Button variant="outline" className="w-full" onClick={() => void authClient.signOut()}>
            Sign out
          </Button>
        </>
      )}
    </div>
  );
}

function InvitePage() {
  const { invitationId } = Route.useParams();
  const invitation = useQuery(api.organizations.invitation, { id: invitationId });
  const navigate = useNavigate();
  const goHome = () => void navigate({ to: "/", replace: true });

  let body: React.ReactNode;
  if (invitation === undefined) body = <Loader />;
  else if (invitation === null) {
    body = (
      <div className={card}>
        <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">Invite not found</h1>
        <p className="mb-6 text-xs text-muted-foreground">
          This invite link is unknown, already used or expired. Ask a member for a new one.
        </p>
        <Link to="/" className="text-xs text-primary hover:underline">
          Go to sign in
        </Link>
      </div>
    );
  } else {
    body = (
      <>
        <Authenticated>
          <Accept invitationId={invitationId} invitation={invitation} onDone={goHome} />
        </Authenticated>
        <Unauthenticated>
          <SignUpForm invitation={{ id: invitationId, ...invitation }} onSuccess={goHome} />
        </Unauthenticated>
        <AuthLoading>
          <Loader />
        </AuthLoading>
      </>
    );
  }
  return <div className="flex h-svh items-center justify-center bg-canvas">{body}</div>;
}
