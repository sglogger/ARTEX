"use client";

import * as React from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import type { Task, TaskScopeRow } from "@/lib/types";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";

type GoalDraft = { text: string; goal_type: "" | "inspection" | "outcome"; vulnclass?: string; checks: string };
type ConstraintDraft = { kind: "allow" | "deny"; text: string };
type ScopeDraft = { kind: string; value: string };

function scopeValue(scope: TaskScopeRow): string {
  if (scope.kind === "company") return String(scope.company_id ?? "");
  return scope.value ?? scope.domain ?? scope.net ?? "";
}

export function TaskEngagementEditor({ task, onUpdated }: { task: Task; onUpdated: () => void }) {
  const [open, setOpen] = React.useState(false);
  const [busy, setBusy] = React.useState(false);
  const [ready, setReady] = React.useState(false);
  const [goal, setGoal] = React.useState(task.goal);
  const [description, setDescription] = React.useState(task.description);
  const [reason, setReason] = React.useState("");
  const [goals, setGoals] = React.useState<GoalDraft[]>([]);
  const [constraints, setConstraints] = React.useState<ConstraintDraft[]>([]);
  const [scopes, setScopes] = React.useState<ScopeDraft[]>([]);
  const canEdit = task.paused && !["done", "failed", "timeout"].includes(task.status);

  React.useEffect(() => {
    if (!open) return;
    let active = true;
    setReady(false);
    setGoal(task.goal);
    setDescription(task.description);
    setReason("");
    Promise.all([api.taskGoals(task.id), api.taskConstraints(task.id), api.taskScope(task.id)])
      .then(([g, c, s]) => {
        if (!active) return;
        setGoals(
          g.goals.map((row) => ({
            text: row.text,
            goal_type: row.goal_type ?? "",
            vulnclass: row.vulnclass,
            checks: (row.required_checks ?? []).join("\n"),
          })),
        );
        setConstraints(c.constraints.map((row) => ({ kind: row.kind, text: row.text })));
        setScopes(
          (s.scope ?? [])
            .filter((row) => String(row.task_id) === task.id)
            .map((row) => ({ kind: row.kind, value: scopeValue(row) })),
        );
        setReady(true);
      })
      .catch((error) => {
        if (active) toast.error(error instanceof Error ? error.message : "Failed to load task scope");
      });
    return () => {
      active = false;
    };
  }, [open, task.id, task.goal, task.description]);

  async function save() {
    if (
      !goal.trim() ||
      !reason.trim() ||
      !goals.length ||
      goals.some((row) => !row.text.trim() || !row.goal_type || (row.goal_type === "inspection" && !row.checks.trim()))
    ) {
      toast.error("Enter the overall goal, change reason, goal types and required checks");
      return;
    }
    setBusy(true);
    try {
      const result = await api.updateTaskEngagement(task.id, {
        goal,
        description,
        reason,
        goals: goals.map(({ checks, ...row }) => ({
          ...row,
          goal_type: row.goal_type as "inspection" | "outcome",
          required_checks: checks
            .split("\n")
            .map((text) => text.trim())
            .filter(Boolean),
        })),
        constraints,
        scopes,
      });
      toast.success(result.note);
      setOpen(false);
      onUpdated();
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Save failed");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(value) => {
        if (!busy) setOpen(value);
      }}
    >
      <DialogTrigger asChild>
        <Button
          size="sm"
          variant="outline"
          disabled={!canEdit}
          title={canEdit ? "Edit goals, constraints and scope together" : "Pause the task and wait for work to exit before editing"}
        >
          Adjust goals and scope
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>Adjust goals and scope</DialogTitle>
          <DialogDescription>
            Goals, constraints and asset scope are saved together. Current work stops, earlier conclusions stay on record and new goals are reviewed again; the task stays paused after saving.
          </DialogDescription>
        </DialogHeader>
        {!ready ? (
          <p className="text-sm text-muted-foreground">Loading goals and scope…</p>
        ) : (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="engagement-description">Task description</Label>
              <Textarea
                id="engagement-description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="engagement-goal">Overall goal</Label>
              <Textarea id="engagement-goal" value={goal} onChange={(e) => setGoal(e.target.value)} />
            </div>
            <div className="space-y-2">
              <Label>Check goals</Label>
              <p className="text-sm text-muted-foreground">
                Inspection goals can be met with negative results within the actual scope; outcome goals need the corresponding result. When changing ports, update both the goal and its required checks.
              </p>
              {goals.map((row, i) => (
                <div key={i} className="space-y-2 rounded border p-3">
                  <div className="flex gap-2">
                    <select
                      aria-label={`Goal ${i + 1} type`}
                      className="rounded border bg-background p-2 text-sm"
                      value={row.goal_type}
                      onChange={(e) =>
                        setGoals(
                          goals.map((g, n) =>
                            n === i ? { ...g, goal_type: e.target.value as GoalDraft["goal_type"] } : g,
                          ),
                        )
                      }
                    >
                      <option value="" disabled>
                        Select a type
                      </option>
                      <option value="inspection">Inspection</option>
                      <option value="outcome">Outcome</option>
                    </select>
                    <Input
                      aria-label={`Goal ${i + 1}`}
                      value={row.text}
                      onChange={(e) => setGoals(goals.map((g, n) => (n === i ? { ...g, text: e.target.value } : g)))}
                    />
                    <Button variant="ghost" onClick={() => setGoals(goals.filter((_, n) => n !== i))}>
                      Remove
                    </Button>
                  </div>
                  <Textarea
                    aria-label={`Goal ${i + 1} required checks`}
                    placeholder="Required checks, one per line"
                    value={row.checks}
                    onChange={(e) => setGoals(goals.map((g, n) => (n === i ? { ...g, checks: e.target.value } : g)))}
                  />
                </div>
              ))}
              <Button
                size="sm"
                variant="outline"
                onClick={() => setGoals([...goals, { text: "", goal_type: "inspection", checks: "" }])}
              >
                Add goal
              </Button>
            </div>
            <div className="space-y-2">
              <Label>Allowed and forbidden actions</Label>
              {constraints.map((row, i) => (
                <div key={i} className="flex gap-2">
                  <select
                    aria-label={`Constraint ${i + 1} type`}
                    className="rounded border bg-background p-2 text-sm"
                    value={row.kind}
                    onChange={(e) =>
                      setConstraints(
                        constraints.map((c, n) =>
                          n === i ? { ...c, kind: e.target.value as ConstraintDraft["kind"] } : c,
                        ),
                      )
                    }
                  >
                    <option value="allow">Allow</option>
                    <option value="deny">Deny</option>
                  </select>
                  <Input
                    aria-label={`Constraint ${i + 1}`}
                    value={row.text}
                    onChange={(e) =>
                      setConstraints(constraints.map((c, n) => (n === i ? { ...c, text: e.target.value } : c)))
                    }
                  />
                  <Button variant="ghost" onClick={() => setConstraints(constraints.filter((_, n) => n !== i))}>
                    Remove
                  </Button>
                </div>
              ))}
              <Button
                size="sm"
                variant="outline"
                onClick={() => setConstraints([...constraints, { kind: "deny", text: "" }])}
              >
                Add constraint
              </Button>
            </div>
            <div className="space-y-2">
              <Label>Asset scope for this task</Label>
              {scopes.map((row, i) => (
                <div key={i} className="flex gap-2">
                  <select
                    aria-label={`Scope ${i + 1} type`}
                    className="rounded border bg-background p-2 text-sm"
                    value={row.kind}
                    onChange={(e) => setScopes(scopes.map((s, n) => (n === i ? { ...s, kind: e.target.value } : s)))}
                  >
                    {["ip", "cidr", "root_domain", "subdomain", "company", "icp", "keyword"].map((kind) => (
                      <option key={kind} value={kind}>
                        {kind}
                      </option>
                    ))}
                  </select>
                  <Input
                    aria-label={`Scope ${i + 1}`}
                    value={row.value}
                    onChange={(e) => setScopes(scopes.map((s, n) => (n === i ? { ...s, value: e.target.value } : s)))}
                  />
                  <Button variant="ghost" onClick={() => setScopes(scopes.filter((_, n) => n !== i))}>
                    Remove
                  </Button>
                </div>
              ))}
              <Button size="sm" variant="outline" onClick={() => setScopes([...scopes, { kind: "ip", value: "" }])}>
                Add scope
              </Button>
            </div>
            <div className="space-y-2">
              <Label htmlFor="engagement-reason">Change reason</Label>
              <Input id="engagement-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
            </div>
          </div>
        )}
        <DialogFooter>
          <Button variant="outline" disabled={busy} onClick={() => setOpen(false)}>
            Cancel
          </Button>
          <Button disabled={!ready || busy || !canEdit} onClick={save}>
            {busy ? "Saving…" : "Save all"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
