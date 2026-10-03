import { api } from "@my-better-t-app/backend/convex/_generated/api";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@my-better-t-app/ui/components/dropdown-menu";
import { useQuery } from "convex/react";
import { useState } from "react";

import { InviteDialog } from "@/components/invite-dialog";
import { authClient } from "@/lib/auth-client";

/** Topbar avatar. Signing out flips the `_auth` gate back to the sign-in form. */
export function AccountMenu() {
  const user = useQuery(api.auth.getCurrentUser);
  const organization = useQuery(api.organizations.current);
  const [inviting, setInviting] = useState(false);
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          aria-label="Account"
          className="size-[26px] rounded-full bg-[linear-gradient(135deg,#1F4BFF_0%,#9FB4FF_100%)] outline-none focus-visible:ring-2 focus-visible:ring-primary/40"
        />
        <DropdownMenuContent align="end" className="min-w-48">
          <DropdownMenuGroup>
            <DropdownMenuLabel className="flex flex-col gap-0.5">
              <span className="text-sm font-medium text-ink">{user?.name ?? "…"}</span>
              <span className="font-mono text-2xs font-normal text-faint">{user?.email}</span>
              {organization && (
                <span className="text-2xs font-normal text-muted-foreground">
                  {organization.name} · {organization.role}
                </span>
              )}
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            {organization && (
              <DropdownMenuItem onClick={() => setInviting(true)}>Invite people…</DropdownMenuItem>
            )}
            <DropdownMenuItem variant="destructive" onClick={() => void authClient.signOut()}>
              Sign out
            </DropdownMenuItem>
          </DropdownMenuGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      {organization && (
        <InviteDialog open={inviting} onOpenChange={setInviting} organizationId={organization.id} />
      )}
    </>
  );
}
