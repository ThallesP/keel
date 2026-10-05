/**
 * What the auth page shows beside the form. One reference per surface, never a repeat
 * (docs/canvas.md, "Auth pages"): here, Steve Jobs's line to the Macintosh team and the year that
 * followed it, from Andy Hertzfeld's folklore.org ("Pirate Flag", "Real Artists Ship",
 * "Signing Party"). The last sentence is a link to /humans.txt (apps/web/public), the inside of
 * this case.
 */
export function RealArtistsShip() {
  return (
    <div className="max-w-md">
      <p className="text-xl font-semibold tracking-tight text-ink">Real artists ship.</p>
      <p className="mt-2 text-sm text-muted-foreground">
        Steve Jobs, to the Macintosh team. January 1983.
      </p>
      <dl className="mt-10 grid grid-cols-[6.5rem_1fr] gap-x-4 gap-y-4 text-sm leading-relaxed">
        <dt className="text-faint tabular-nums">Jan 1983</dt>
        <dd className="text-muted-foreground">
          A retreat in Carmel. Three sayings on one slide; another was{" "}
          <span className="text-ink">“It’s better to be a pirate than join the navy.”</span>
        </dd>
        <dt className="text-faint tabular-nums">Jan 16, 1984</dt>
        <dd className="text-muted-foreground">
          The team asks for another week or two and doesn’t get them. The final build comes together
          around 5:30 on a Monday morning; the factory opens at six.
        </dd>
        <dt className="text-faint tabular-nums">Jan 24, 1984</dt>
        <dd className="text-muted-foreground">
          The Macintosh ships. Moulded inside the case are the signatures of the forty-seven people
          who built it.{" "}
          {/* The inside of this case: a plain <a>, not <Link>, so the browser fetches the file. */}
          <a
            href="/humans.txt"
            className="rounded-xs decoration-dot underline-offset-4 hover:underline focus-visible:underline focus-visible:outline-none"
          >
            No one who bought one would ever see them.
          </a>{" "}
          <span className="text-ink">They signed anyway.</span>
        </dd>
      </dl>
    </div>
  );
}

/**
 * Kept for a later surface (owner's pick, 2026-10-05), not rendered yet: the day after Dropbox
 * launched on Hacker News, the top reply explained you could already build it with an FTP account,
 * and Drew Houston answered. Items 8863, 9224 and 9272 on news.ycombinator.com; the two comments
 * are quoted in part, with their authors. Caption it "Dropbox shipped anyway."
 */
export function DropboxThread() {
  return (
    <div className="max-w-md">
      <div className="text-sm text-ink">
        <span className="mr-1 text-faint">▲</span>My YC app: Dropbox - Throw away your USB drive{" "}
        <span className="text-faint">(getdropbox.com)</span>
      </div>
      <div className="mt-0.5 text-2xs text-faint">
        104 points by dhouston · April 4, 2007 · 71 comments
      </div>
      <div className="mt-4 border-l border-line pl-4">
        <div className="text-2xs text-faint">BrandonM</div>
        <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
          I have a few qualms with this app: 1. For a Linux user, you can already build such a
          system yourself quite trivially by getting an FTP account, mounting it locally with
          curlftpfs, and then using SVN or CVS on the mounted filesystem.{" "}
          <span className="text-faint">…</span>
        </p>
        <div className="mt-4 border-l border-line pl-4">
          <div className="text-2xs text-faint">dhouston</div>
          <p className="mt-1 text-sm leading-relaxed text-muted-foreground">
            1. re: the first part, many people want something plug and play.{" "}
            <span className="text-faint">…</span> dropbox uses a <em>local</em> folder with
            efficient sync in the background, which is an important difference :)
          </p>
        </div>
      </div>
    </div>
  );
}
