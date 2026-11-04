"use client";

import * as React from "react";

import { CheckIcon, CopyIcon, FileTextIcon } from "lucide-react";
import { toast } from "sonner";

import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import { copyText } from "@/lib/utils";

export function ReportTab({ taskId }: { taskId: string }) {
  // A different task must never render or copy the previous task's report.
  return <TaskReport key={taskId} taskId={taskId} />;
}

// A failed fetch is a state of its own, so it is never shown as "No report yet".
type ReportState = { status: "loading" | "error" } | { status: "ready"; text: string };

function TaskReport({ taskId }: { taskId: string }) {
  const [state, setState] = React.useState<ReportState>({ status: "loading" });
  const [attempt, setAttempt] = React.useState(0);
  const [copied, setCopied] = React.useState(false);
  const report = state.status === "ready" ? state.text : "";

  // biome-ignore lint/correctness/useExhaustiveDependencies: attempt intentionally restarts the request after an explicit retry.
  React.useEffect(() => {
    let active = true;
    setState({ status: "loading" });
    api
      .report(taskId)
      .then((text) => {
        if (active) setState({ status: "ready", text });
      })
      .catch(() => {
        if (active) setState({ status: "error" });
      });
    return () => {
      active = false;
    };
  }, [taskId, attempt]);

  function retry() {
    setState({ status: "loading" });
    setAttempt((current) => current + 1);
  }

  async function copy() {
    if (!report) return;
    const ok = await copyText(report);
    if (ok) {
      setCopied(true);
      toast.success("Markdown copied");
      setTimeout(() => setCopied(false), 1500);
    } else {
      toast.error("Copy failed, please select and copy the text manually");
    }
  }

  let content: React.ReactNode;
  if (state.status === "loading") {
    content = (
      <div
        role="status"
        className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm"
      >
        <FileTextIcon className="size-8 opacity-40" />
        Loading…
      </div>
    );
  } else if (state.status === "error") {
    content = (
      <div
        role="alert"
        className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-sm"
      >
        <p>Could not load the report. Please try again.</p>
        <Button size="sm" variant="outline" onClick={retry}>
          Retry
        </Button>
      </div>
    );
  } else if (report) {
    content = (
      <div className="max-h-[60vh] overflow-auto rounded-md border bg-muted/20 p-4">
        <Markdown text={report} />
      </div>
    );
  } else {
    content = (
      <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-dashed py-16 text-muted-foreground text-sm">
        <FileTextIcon className="size-8 opacity-40" />
        No report yet
      </div>
    );
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-sm">
          <FileTextIcon className="size-4" /> Penetration test report (Markdown)
        </CardTitle>
        <div className="flex gap-2">
          {report && (
            <Button size="sm" variant="outline" onClick={copy}>
              {copied ? <CheckIcon /> : <CopyIcon />} Copy
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent>{content}</CardContent>
    </Card>
  );
}
