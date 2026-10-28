"use client";

import { useState } from "react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { Task } from "@/lib/types";

export function TaskCompletionBadge({ task }: { task: Task }) {
  const phase = task.completion?.phase;
  if (task.paused || task.status !== "running") return null;
  if (phase === "interrupted" && task.completion?.failure_kind)
    return <Badge variant="outline">{task.completion.next_retry_at ? "Waiting to retry review" : "Review failed, awaiting recovery"}</Badge>;
  if (phase !== "draining" && phase !== "reviewing") return null;
  return <Badge variant="outline">{phase === "draining" ? "Saving wrap-up results" : "Final review"}</Badge>;
}

export function TaskCompletionPanel({ task }: { task: Task }) {
  const [retrying, setRetrying] = useState(false);
  const completion = task.completion;
  if (!completion || completion.phase === "idle") return null;
  const active =
    task.status === "running" && !task.paused && (completion.phase === "draining" || completion.phase === "reviewing");
  let title = "Latest wrap-up record";
  if (active) title = completion.phase === "draining" ? "Saving existing worker results" : "Running final review";
  const failed = task.status === "running" && completion.phase === "interrupted" && !!completion.failure_kind;
  if (failed)
    title = completion.next_retry_at
      ? "Waiting to retry review"
      : completion.failure_kind === "evidence"
        ? "Insufficient evidence; required work must continue"
        : "Review failed, awaiting recovery";
  const actions: Record<string, string> = {
    retry: "Waiting for automatic retry; the task can still be paused.",
    manual_retry: "Automatic retry has stopped. Check the failure reason, fix it and review again.",
    repair_evidence: "Add evidence or correct the verdicts for goals and remaining checks, then review again.",
    repair_history: "Repair the reasoning history or adjust the model profile, then review again.",
    configure_model: "Check the model and review budget settings, then review again.",
    wait_for_exit: "Check why the worker was interrupted, confirm its results are saved, then review again.",
  };
  const retry = async () => {
    setRetrying(true);
    try {
      await api.retryTaskCompletion(task.id, completion.attempt_id, "User confirmed the review failure cause is resolved and requests a new review");
      toast.success("New review requested");
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to request a new review");
    } finally {
      setRetrying(false);
    }
  };
  return (
    <details className="rounded-md border bg-muted/30 px-3 py-2 text-sm" open={active || failed}>
      <summary className="cursor-pointer font-medium">
        {title}
        <span className="ml-2 font-normal text-muted-foreground">#{completion.attempt_id}</span>
      </summary>
      <div className="mt-2 space-y-2">
        <p className="whitespace-pre-wrap break-words">{completion.reason}</p>
        {active && (
          <p className="text-muted-foreground text-xs">
            No new probes start in this phase. {completion.phase === "draining" ? "Wrap-up save deadline" : "Review deadline"}:
            {completion.phase === "reviewing"
              ? completion.review_deadline_at
                ? new Date(completion.review_deadline_at).toLocaleString()
                : "Awaiting confirmation"
              : new Date(completion.deadline_at).toLocaleString()}
            ; the task can still be paused.
          </p>
        )}
        {failed && (
          <div className="space-y-2">
            <p>
              This review type has failed {completion.failure_count ?? 0} times.
              {actions[completion.recovery_action ?? ""] ?? "Check the failure reason, then recover."}
            </p>
            {completion.next_retry_at && (
              <p className="text-muted-foreground text-xs">
                Next review: {new Date(completion.next_retry_at).toLocaleString()}; no automatic retry while paused.
              </p>
            )}
            {!task.paused && (
              <Button variant="outline" size="sm" disabled={retrying} onClick={retry}>
                {retrying ? "Requesting…" : "Review again after fixing"}
              </Button>
            )}
          </div>
        )}
        {!!completion.recovery_history?.length && (
          <ul className="list-inside list-disc text-muted-foreground text-xs">
            {completion.recovery_history.map((entry, index) => (
              <li key={`${entry.at}-${index}`}>
                {new Date(entry.at).toLocaleString()}: {entry.reason}
              </li>
            ))}
          </ul>
        )}
        {completion.review && <p className="whitespace-pre-wrap break-words">{completion.review}</p>}
        {!!completion.goal_gaps?.length && (
          <div className="space-y-1">
            <p className="font-medium">Goal evidence gaps</p>
            <p className="text-muted-foreground text-xs">Check the original proof in goal management, correct the completion verdict and continue the required work.</p>
            <ul className="list-inside list-disc space-y-1">
              {completion.goal_gaps.map((gap) => (
                <li key={`${gap.goal_id}-${gap.expected_version}`} className="whitespace-pre-wrap break-words">
                  Goal #{gap.goal_id}: {gap.reason}
                </li>
              ))}
            </ul>
          </div>
        )}
        {!!completion.remaining_work?.length && (
          <>
            <p className="font-medium">Remaining checks at wrap-up and their rationale</p>
            <p className="text-muted-foreground text-xs">Stopping or interrupting does not mean a check has passed.</p>
            <ul className="list-inside list-disc space-y-1">
              {completion.remaining_work.map((work) => (
                <li key={work.intent_id} className="break-words">
                  #{work.intent_id} {work.summary}: {work.reason || "No completion conclusion reached"}
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </details>
  );
}
