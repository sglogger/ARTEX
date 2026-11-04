"use client";

import * as React from "react";

import { ScrollTextIcon } from "lucide-react";

import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { api } from "@/lib/api";

type ChangelogState = { status: "idle" | "loading" | "error" } | { status: "ready"; text: string };

// The running version as a pill in the top bar; clicking it opens the release notes
// embedded in this binary, so they always match the version shown. Local builds
// report "dev"/"local" as version; releaseVersion (the newest release in the
// embedded changelog) then shows which release they are based on.
export function VersionPill({ version, releaseVersion = "" }: { version: string; releaseVersion?: string }) {
  let label = version;
  if (/^\d/.test(version)) label = `v${version}`;
  else if (releaseVersion) label = `v${releaseVersion} · ${version}`;
  const [open, setOpen] = React.useState(false);
  const [state, setState] = React.useState<ChangelogState>({ status: "idle" });

  const load = React.useCallback(() => {
    setState({ status: "loading" });
    api
      .changelog()
      .then((text) => setState({ status: "ready", text }))
      .catch(() => setState({ status: "error" }));
  }, []);

  function onOpenChange(next: boolean) {
    setOpen(next);
    if (next && state.status !== "ready" && state.status !== "loading") load();
  }

  let body: React.ReactNode;
  if (state.status === "ready") {
    body = <Markdown text={state.text} />;
  } else if (state.status === "error") {
    body = (
      <div role="alert" className="flex flex-col items-center gap-2 py-10 text-sm">
        <p>Could not load the changelog.</p>
        <Button size="sm" variant="outline" onClick={load}>
          Retry
        </Button>
      </div>
    );
  } else {
    body = (
      <p role="status" className="py-10 text-center text-muted-foreground text-sm">
        Loading…
      </p>
    );
  }

  return (
    <>
      <button
        type="button"
        onClick={() => onOpenChange(true)}
        title="Show the changelog"
        className="inline-flex items-center gap-1 rounded-full border bg-muted/40 px-2.5 py-0.5 font-medium text-muted-foreground text-xs tabular-nums transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <ScrollTextIcon className="size-3" />
        {label}
      </button>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="flex max-h-[85vh] flex-col sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>Changelog</DialogTitle>
            <DialogDescription>Release notes included in the running version {label}.</DialogDescription>
          </DialogHeader>
          <div className="min-h-0 flex-1 overflow-y-auto pr-1">{body}</div>
        </DialogContent>
      </Dialog>
    </>
  );
}
