import { Button } from "@my-better-t-app/ui/components/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@my-better-t-app/ui/components/dialog";
import { Input } from "@my-better-t-app/ui/components/input";
import { Label } from "@my-better-t-app/ui/components/label";
import { useState } from "react";
import { toast } from "sonner";

import { useCreateInvitation } from "@/gen/api";
import { errorMessage } from "@/lib/api";

/**
 * Account → Invite people. Creates an organization invitation for an email and shows the link
 * to hand over; nothing is mailed. The link opens `/invite/$invitationId`, where that email can
 * create its account (sign-up is otherwise closed) or, if it already has one, join. The
 * invitation is for the caller's organization (the server reads it from the session).
 */
export function InviteDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const [email, setEmail] = useState("");
  const [link, setLink] = useState<string | null>(null);
  const { mutateAsync: createInvitation, isPending } = useCreateInvitation();

  const create = async () => {
    let id: string;
    try {
      ({ id } = await createInvitation({
        body: { email: email.trim(), role: "member" },
      }));
    } catch (err) {
      toast.error(errorMessage(err) || "Could not create the invite");
      return;
    }
    setLink(`${window.location.origin}/invite/${id}`);
  };

  const copy = async () => {
    if (!link) return;
    try {
      await navigator.clipboard.writeText(link);
      toast.success("Link copied");
    } catch {
      toast.error("Could not copy; select the link and copy it by hand");
    }
  };

  const close = (next: boolean) => {
    if (!next) {
      setEmail("");
      setLink(null);
    }
    onOpenChange(next);
  };

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Invite people</DialogTitle>
          <DialogDescription>
            {link
              ? "Send this link to them. It works once, for that email, for a week."
              : "Sign-up is by invitation. Enter their email to get a link they can sign up with."}
          </DialogDescription>
        </DialogHeader>
        {link ? (
          <div className="flex flex-col gap-2">
            <Label htmlFor="invite-link">Invite link</Label>
            <div className="flex gap-2">
              <Input
                id="invite-link"
                readOnly
                value={link}
                onFocus={(e) => e.currentTarget.select()}
                className="font-mono"
              />
              <Button onClick={() => void copy()}>Copy</Button>
            </div>
            <Button variant="ghost" size="sm" onClick={() => setLink(null)} className="self-start">
              Invite someone else
            </Button>
          </div>
        ) : (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void create();
            }}
          >
            <Label htmlFor="invite-email">Email</Label>
            <Input
              id="invite-email"
              type="email"
              required
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="name@example.com"
            />
            <Button type="submit" disabled={isPending || email.trim() === ""} className="self-end">
              {isPending ? "Creating…" : "Create invite link"}
            </Button>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
