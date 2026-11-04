"use client";

import * as React from "react";

import { FileDownIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { ProfessionalReportRequest, Task } from "@/lib/types";

/** Statuses that are left out by default: they are not open security issues. */
const DEFAULT_EXCLUDED = ["false_positive", "ignored", "duplicate"];
const STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: "pending", label: "Pending" },
  { value: "in_progress", label: "In progress" },
  { value: "confirmed", label: "Confirmed" },
  { value: "resolved", label: "Resolved" },
  { value: "fixed", label: "Fixed" },
  { value: "risk_accepted", label: "Risk accepted" },
  { value: "false_positive", label: "False positive" },
  { value: "ignored", label: "Ignored" },
  { value: "duplicate", label: "Duplicate" },
];

// Cover details are remembered per browser so recurring reports for the same client need no retyping.
const STORAGE_KEY = "artex.professional-report.meta";
type Meta = { title: string; client: string; author: string; classification: string; language: "en" | "de" };
const DEFAULT_META: Meta = { title: "", client: "", author: "", classification: "", language: "en" };

function loadMeta(): Meta {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    return raw ? { ...DEFAULT_META, ...(JSON.parse(raw) as Partial<Meta>) } : DEFAULT_META;
  } catch {
    return DEFAULT_META;
  }
}

function saveMeta(meta: Meta) {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(meta));
  } catch {
    // storage unavailable (private mode): the report still works
  }
}

function taskLabel(t: Task): string {
  const name = (t.name?.trim() ? t.name : t.description).split("\n")[0].trim();
  return `#${t.id} ${name.length > 70 ? `${name.slice(0, 70)}…` : name}`;
}

function findingCount(t: Task): number | undefined {
  if (!t.findings) return undefined;
  return t.findings.critical + t.findings.high + t.findings.medium + t.findings.low;
}

export function ProfessionalReportDialog({
  open,
  onOpenChange,
  initialTaskIds,
  findingIds = [],
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Tasks preselected when the dialog opens (e.g. the current task). */
  initialTaskIds?: string[];
  /** Checked findings from the findings page; they are included in addition to the selected tasks. */
  findingIds?: string[];
}) {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [taskIds, setTaskIds] = React.useState<Set<string>>(new Set());
  const [useFindings, setUseFindings] = React.useState(false);
  const [excluded, setExcluded] = React.useState<Set<string>>(new Set(DEFAULT_EXCLUDED));
  const [minSeverity, setMinSeverity] = React.useState<NonNullable<ProfessionalReportRequest["min_severity"]>>("");
  const [consolidated, setConsolidated] = React.useState(true);
  const [includeEvidence, setIncludeEvidence] = React.useState(true);
  const [meta, setMeta] = React.useState<Meta>(DEFAULT_META);
  const [summary, setSummary] = React.useState("");
  const [format, setFormat] = React.useState<"pdf" | "html">("pdf");
  const [pdfAvailable, setPdfAvailable] = React.useState(true);
  const [generating, setGenerating] = React.useState(false);

  // Reset the selection every time the dialog opens, so it reflects where it was opened from.
  // biome-ignore lint/correctness/useExhaustiveDependencies: only reopening resets the form; prop identity changes must not.
  React.useEffect(() => {
    if (!open) return;
    setTaskIds(new Set(initialTaskIds ?? []));
    setUseFindings(findingIds.length > 0);
    setMeta(loadMeta());
    api
      .tasks()
      .then((r) => setTasks(r.tasks))
      .catch((e) => toast.error("Failed to load tasks: " + (e as Error).message));
    api
      .professionalReportCapabilities()
      .then((c) => {
        setPdfAvailable(c.pdf);
        if (!c.pdf) setFormat("html");
      })
      .catch(() => setPdfAvailable(true));
  }, [open]);

  const toggle = (set: Set<string>, value: string, on: boolean) => {
    const next = new Set(set);
    if (on) next.add(value);
    else next.delete(value);
    return next;
  };

  const selectedFindings = useFindings ? findingIds : [];
  const nothingSelected = taskIds.size === 0 && selectedFindings.length === 0;

  async function generate() {
    setGenerating(true);
    saveMeta(meta);
    try {
      await api.professionalReport({
        task_ids: [...taskIds],
        finding_ids: selectedFindings,
        exclude_statuses: [...excluded],
        min_severity: minSeverity,
        consolidated,
        include_evidence: includeEvidence,
        ...meta,
        summary,
        format,
      });
      toast.success(format === "pdf" ? "PDF report downloaded" : "HTML report downloaded");
      onOpenChange(false);
    } catch (e) {
      toast.error("Report failed: " + (e as Error).message);
    } finally {
      setGenerating(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Professional report</DialogTitle>
          <DialogDescription>
            Combines the findings of the selected tasks into one client-ready report with cover page, management
            summary, scope, methodology and detailed findings.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-5 py-1">
          <div className="flex flex-col gap-2">
            <span className="text-muted-foreground text-xs">Tasks ({taskIds.size} selected)</span>
            <div className="max-h-48 overflow-y-auto rounded-md border p-2">
              {tasks.length === 0 && <p className="text-muted-foreground text-sm">Loading…</p>}
              {tasks.map((t) => {
                const n = findingCount(t);
                return (
                  <label key={t.id} htmlFor={`report-task-${t.id}`} className="flex items-center gap-2 py-1 text-sm">
                    <Checkbox
                      id={`report-task-${t.id}`}
                      checked={taskIds.has(t.id)}
                      onCheckedChange={(v) => setTaskIds((s) => toggle(s, t.id, v === true))}
                    />
                    <span className="min-w-0 flex-1 truncate">{taskLabel(t)}</span>
                    {n !== undefined && <span className="text-muted-foreground shrink-0 text-xs">{n} findings</span>}
                  </label>
                );
              })}
            </div>
            {findingIds.length > 0 && (
              <label htmlFor="report-use-findings" className="flex items-center gap-2 text-sm">
                <Checkbox
                  id="report-use-findings"
                  checked={useFindings}
                  onCheckedChange={(v) => setUseFindings(v === true)}
                />
                Also include the {findingIds.length} checked findings
              </label>
            )}
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="flex flex-col gap-2">
              <Label htmlFor="report-min-severity" className="text-muted-foreground text-xs font-normal">
                Minimum severity
              </Label>
              <NativeSelect
                id="report-min-severity"
                value={minSeverity}
                onChange={(e) => setMinSeverity(e.target.value as typeof minSeverity)}
              >
                <NativeSelectOption value="">All (incl. info)</NativeSelectOption>
                <NativeSelectOption value="low">Low and above</NativeSelectOption>
                <NativeSelectOption value="medium">Medium and above</NativeSelectOption>
                <NativeSelectOption value="high">High and above</NativeSelectOption>
                <NativeSelectOption value="critical">Critical only</NativeSelectOption>
              </NativeSelect>
            </div>
            <div className="flex flex-col gap-2">
              <span className="text-muted-foreground text-xs">Options</span>
              <label htmlFor="report-consolidated" className="flex items-center gap-2 text-sm">
                <Checkbox
                  id="report-consolidated"
                  checked={consolidated}
                  onCheckedChange={(v) => setConsolidated(v === true)}
                />
                Merge duplicate findings (finding groups)
              </label>
              <label htmlFor="report-evidence" className="flex items-center gap-2 text-sm">
                <Checkbox
                  id="report-evidence"
                  checked={includeEvidence}
                  onCheckedChange={(v) => setIncludeEvidence(v === true)}
                />
                Include raw evidence
              </label>
            </div>
          </div>

          <div className="flex flex-col gap-2">
            <span className="text-muted-foreground text-xs">Leave out findings with status</span>
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {STATUS_OPTIONS.map((o) => (
                <label key={o.value} htmlFor={`report-ex-${o.value}`} className="flex items-center gap-1.5 text-sm">
                  <Checkbox
                    id={`report-ex-${o.value}`}
                    checked={excluded.has(o.value)}
                    onCheckedChange={(v) => setExcluded((s) => toggle(s, o.value, v === true))}
                  />
                  {o.label}
                </label>
              ))}
            </div>
          </div>

          <div className="grid gap-3 sm:grid-cols-2">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="report-title" className="text-muted-foreground text-xs font-normal">
                Title
              </Label>
              <Input
                id="report-title"
                placeholder="Penetration Test Report"
                value={meta.title}
                onChange={(e) => setMeta({ ...meta, title: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="report-client" className="text-muted-foreground text-xs font-normal">
                Client
              </Label>
              <Input
                id="report-client"
                value={meta.client}
                onChange={(e) => setMeta({ ...meta, client: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="report-author" className="text-muted-foreground text-xs font-normal">
                Prepared by
              </Label>
              <Input
                id="report-author"
                value={meta.author}
                onChange={(e) => setMeta({ ...meta, author: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="report-classification" className="text-muted-foreground text-xs font-normal">
                Classification
              </Label>
              <Input
                id="report-classification"
                placeholder={meta.language === "de" ? "Vertraulich" : "Confidential"}
                value={meta.classification}
                onChange={(e) => setMeta({ ...meta, classification: e.target.value })}
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="report-language" className="text-muted-foreground text-xs font-normal">
                Report language
              </Label>
              <NativeSelect
                id="report-language"
                value={meta.language}
                onChange={(e) => setMeta({ ...meta, language: e.target.value as Meta["language"] })}
              >
                <NativeSelectOption value="en">English</NativeSelectOption>
                <NativeSelectOption value="de">Deutsch</NativeSelectOption>
              </NativeSelect>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="report-format" className="text-muted-foreground text-xs font-normal">
                Format
              </Label>
              <NativeSelect
                id="report-format"
                value={format}
                onChange={(e) => setFormat(e.target.value as "pdf" | "html")}
              >
                <NativeSelectOption value="pdf" disabled={!pdfAvailable}>
                  PDF{pdfAvailable ? "" : " (no Chromium on the server)"}
                </NativeSelectOption>
                <NativeSelectOption value="html">HTML (print from the browser)</NativeSelectOption>
              </NativeSelect>
            </div>
          </div>

          <div className="flex flex-col gap-1.5">
            <Label htmlFor="report-summary" className="text-muted-foreground text-xs font-normal">
              Management summary (optional, Markdown; empty = generated from the findings)
            </Label>
            <Textarea id="report-summary" rows={4} value={summary} onChange={(e) => setSummary(e.target.value)} />
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={generating}>
            Cancel
          </Button>
          <Button onClick={generate} disabled={generating || nothingSelected}>
            <FileDownIcon />
            {generating ? "Generating…" : `Generate ${format.toUpperCase()}`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
