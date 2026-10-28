"use client";
import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { toast } from "sonner";

import { FindingCaseMembers, FindingCaseReviewPanel, FindingSeverityCounts } from "@/components/finding-case-list";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import type { FindingCaseDetail } from "@/lib/types";

function CaseDetail() {
  const params = useSearchParams();
  const id = params.get("id") ?? "";
  const contextTask = params.get("context_task") ?? undefined;
  const [data, setData] = React.useState<FindingCaseDetail | null>(null);
  const [error, setError] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [selected, setSelected] = React.useState<Set<string>>(() => new Set());
  const [refresh, setRefresh] = React.useState(0);
  // biome-ignore lint/correctness/useExhaustiveDependencies: refresh explicitly reloads group after a detach.
  React.useEffect(() => {
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    async function load() {
      try {
        const v = await api.getFindingCase(id, contextTask);
        if (active) {
          setData(v);
          setError("");
        }
      } catch (e) {
        if (active) setError((e as Error).message);
      } finally {
        if (active) timer = setTimeout(load, 5000);
      }
    }
    if (id) void load();
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [id, contextTask, refresh]);
  const readOnly = !!contextTask && contextTask !== data?.case.task_id;
  const select = (fid: string, on: boolean) =>
    setSelected((v) => {
      const n = new Set(v);
      if (on) n.add(fid);
      else n.delete(fid);
      return n;
    });
  async function regenerate() {
    setBusy(true);
    try {
      await api.regenerateFindingCase(id, contextTask);
      toast.success("Re-organization submitted; you can check its execution status");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  async function detach() {
    setBusy(true);
    try {
      for (const fid of selected) await api.removeFindingCaseMember(id, fid, "Manually detached from merge");
      setSelected(new Set());
      setRefresh((v) => v + 1);
      toast.success("Removed; original records kept");
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setBusy(false);
    }
  }
  if (!data)
    return error ? (
      <Alert>
        <AlertDescription>{error}</AlertDescription>
      </Alert>
    ) : (
      <Spinner />
    );
  const c = data.case;
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Button asChild variant="outline">
          <Link href="/function/findings">Back to findings</Link>
        </Button>
        <h1 className="font-semibold text-xl">{c.title}</h1>
        <FindingSeverityCounts counts={c} />
        {c.severity ? <StatusBadge domain="severity" value={c.severity} /> : <span>Unified rating pending</span>}
      </div>
      {error ? (
        <Alert>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      ) : null}
      {!c.active ? (
        <Alert>
          <AlertDescription>This folder was dissolved or merged into another group; original records are still available from the findings list.</AlertDescription>
        </Alert>
      ) : null}
      {readOnly ? (
        <Alert>
          <AlertDescription>Finding group inherited from another task, read-only.</AlertDescription>
        </Alert>
      ) : (
        <FindingCaseReviewPanel taskId={c.task_id ?? undefined} />
      )}
      <Tabs defaultValue="report">
        <TabsList>
          <TabsTrigger value="report">Unified report</TabsTrigger>
          <TabsTrigger value="members">Original reports ({c.count})</TabsTrigger>
          <TabsTrigger value="history">Merge history</TabsTrigger>
        </TabsList>
        <TabsContent value="report">
          <Card>
            <CardHeader>
              <CardTitle>Full reproduction and remediation report</CardTitle>
              <CardDescription>{c.severity_reason || "Unified rating not yet complete"}</CardDescription>
            </CardHeader>
            <CardContent className="flex min-w-0 flex-col gap-4">
              {c.report_version !== c.version ? (
                <Alert>
                  <AlertDescription>Members or evidence changed; the unified report needs updating.</AlertDescription>
                </Alert>
              ) : null}
              {!readOnly && c.active ? (
                <Button variant="outline" onClick={regenerate} disabled={busy}>
                  {busy ? <Spinner /> : null}Regenerate unified report
                </Button>
              ) : null}
              {c.report ? (
                <Markdown text={c.report} />
              ) : (
                <p className="text-muted-foreground text-sm">The unified report hasn't been generated yet; original reports are under "Original reports".</p>
              )}
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="members">
          <Card>
            <CardHeader>
              <CardTitle>Original reports and evidence are fully preserved</CardTitle>
              <CardDescription>Members keep their original rating; the unified rating does not overwrite original records.</CardDescription>
            </CardHeader>
            <CardContent>
              {!readOnly && selected.size > 0 ? (
                <AlertDialog>
                  <AlertDialogTrigger asChild>
                    <Button variant="outline" disabled={busy}>
                      Remove selected ({selected.size})
                    </Button>
                  </AlertDialogTrigger>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Remove from merge group?</AlertDialogTitle>
                      <AlertDialogDescription>
                        The original reports and evidence are kept, and the agent will not automatically merge these again.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Cancel</AlertDialogCancel>
                      <AlertDialogAction onClick={detach}>Remove</AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
              ) : null}
              <FindingCaseMembers
                caseId={id}
                version={c.version}
                selectedIds={selected}
                onSelect={readOnly ? undefined : select}
                contextTask={contextTask}
              />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="history">
          <Card>
            <CardHeader>
              <CardTitle>Rationale and activity log</CardTitle>
              <CardDescription>{c.reason}</CardDescription>
            </CardHeader>
            <CardContent className="flex flex-col gap-3">
              {data.events.map((e) => (
                <div key={e.id} className="border-b pb-3 text-sm">
                  <p>
                    {({ reporter: "Reporter agent", human: "Human", system: "System" } as Record<string, string>)[e.actor] ??
                      e.actor}{" "}
                    ·{" "}
                    {(
                      {
                        merge: "Merged",
                        detach: "Member removed",
                        member_deleted: "Member removed or deleted",
                        report_updated: "Unified report updated",
                      } as Record<string, string>
                    )[e.action] ?? e.action}{" "}
                    · {new Date(e.created_at).toLocaleString("en-US")}
                  </p>
                  <p className="text-muted-foreground">
                    {e.reason}
                    {e.finding_ids.length > 0 ? ` · #${e.finding_ids.join(" / #")}` : ""}
                  </p>
                </div>
              ))}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  );
}
export default function Page() {
  return (
    <React.Suspense fallback={<Spinner />}>
      <CaseDetail />
    </React.Suspense>
  );
}
