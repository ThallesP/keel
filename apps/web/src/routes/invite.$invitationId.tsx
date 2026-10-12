import { Button } from "@my-better-t-app/ui/components/button";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";

import { type PublicInvitation, useGetInvitation } from "@/gen/api";
import { AuthShell } from "@/components/auth/shell";
import Loader from "@/components/loader";
import SignUpForm from "@/components/sign-up-form";
import { errorMessage } from "@/lib/api";
import { SessionGate, useAuth, useSession } from "@/lib/session";

/**
 * An invite link, made in Account → Invite people. Signed out: the sign-up form for the invited
 * email. Signed in as that email: one click to join. Sign-up elsewhere is closed once the first
 * account exists (`POST /api/auth/sign-up`), so this is how everyone else gets in.
 */
export const Route = createFileRoute("/invite/$invitationId")({ component: InvitePage });

function Accept({
  invitationId,
  invitation,
  onDone,
}: {
  invitationId: string;
  invitation: PublicInvitation;
  onDone: () => void;
}) {
  const auth = useAuth();
  const { user } = useSession();
  const [busy, setBusy] = useState(false);
  if (user === undefined) return <Loader />;
  const matches = user?.email.toLowerCase() === invitation.email.toLowerCase();
  const join = async () => {
    setBusy(true);
    try {
      await auth.acceptInvitation(invitationId);
    } catch (err) {
      setBusy(false);
      toast.error(errorMessage(err) || "Could not accept the invitation");
      return;
    }
    setBusy(false);
    onDone();
  };
  return (
    <div>
      <h1 className="mb-1 text-lg font-semibold tracking-tight text-ink">
        Join {invitation.organization}
      </h1>
      {matches ? (
        <>
          <p className="mb-6 text-xs text-muted-foreground">
            You are signed in as {user.email}. Join to see its projects.
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
          <Button
            variant="outline"
            className="w-full"
            onClick={() =>
              void auth.signOut().catch((err: unknown) => toast.error(errorMessage(err)))
            }
          >
            Sign out
          </Button>
        </>
      )}
    </div>
  );
}

/**
 * The invitation as the link first showed it. Signing up or joining spends it, and the session
 * change that follows refetches every query, so a later answer would read "not found" for the
 * moment before the page moves on.
 */
function useInvitation(invitationId: string): PublicInvitation | null | undefined {
  const { data } = useGetInvitation(
    { path: { id: invitationId } },
    { query: { staleTime: Infinity, refetchOnWindowFocus: false, meta: { realtime: false } } },
  );
  const [first, setFirst] = useState<{ id: string; invitation: PublicInvitation | null }>();
  if (data !== undefined && first?.id !== invitationId) {
    setFirst({ id: invitationId, invitation: data.invitation });
  }
  return first?.id === invitationId ? first.invitation : data?.invitation;
}

function InvitePage() {
  const { invitationId } = Route.useParams();
  const invitation = useInvitation(invitationId);
  const navigate = useNavigate();
  const goHome = () => void navigate({ to: "/", replace: true });

  let body: React.ReactNode;
  if (invitation === undefined) body = <Loader />;
  else if (invitation === null) {
    body = (
      <div>
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
      <SessionGate
        loading={<Loader />}
        signedOut={
          <SignUpForm invitation={{ id: invitationId, ...invitation }} onSuccess={goHome} />
        }
      >
        <Accept invitationId={invitationId} invitation={invitation} onDone={goHome} />
      </SessionGate>
    );
  }
  return <AuthShell>{body}</AuthShell>;
}
