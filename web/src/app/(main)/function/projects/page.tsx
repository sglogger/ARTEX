"use client";

import * as React from "react";

import { useRouter, useSearchParams } from "next/navigation";

import {
  ArchiveIcon,
  ArchiveRestoreIcon,
  ArrowRightIcon,
  BriefcaseIcon,
  PencilIcon,
  PlusIcon,
  Trash2Icon,
  TruckIcon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
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
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import { setActiveProjectId, useActiveProjectId } from "@/lib/project";
import type { Project, ProjectInput, Task } from "@/lib/types";

const dateInput = (v?: string | null) => (v ? v.slice(0, 10) : "");
const toRFC3339 = (v: string) => (v ? `${v}T00:00:00Z` : null);

function ProjectFormDialog({
  open,
  onOpenChange,
  project,
  onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project?: Project;
  onSaved: (p: Project) => void;
}) {
  const [form, setForm] = React.useState({ name: "", client: "", description: "", start: "", end: "" });
  const [saving, setSaving] = React.useState(false);

  React.useEffect(() => {
    if (!open) return;
    setForm({
      name: project?.name ?? "",
      client: project?.client ?? "",
      description: project?.description ?? "",
      start: dateInput(project?.start_date),
      end: dateInput(project?.end_date),
    });
  }, [open, project]);

  async function save() {
    const input: ProjectInput = {
      name: form.name.trim(),
      client: form.client.trim(),
      description: form.description.trim(),
      start_date: toRFC3339(form.start),
      end_date: toRFC3339(form.end),
    };
    setSaving(true);
    try {
      const saved = project ? await api.updateProject(project.id, input) : await api.createProject(input);
      onSaved(saved);
      onOpenChange(false);
      toast.success(project ? "Project saved" : "Project created");
    } catch (e) {
      toast.error(`Saving failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{project ? "Edit project" : "New project"}</DialogTitle>
          <DialogDescription>
            A project bundles the tasks, chats and findings of one engagement. The client is used on the professional
            report.
          </DialogDescription>
        </DialogHeader>
        <div className="grid gap-3">
          <div className="grid gap-1.5">
            <Label htmlFor="project-name">Name</Label>
            <Input
              id="project-name"
              placeholder="e.g. Ascano web platform Q4"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
            />
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="project-client">Client</Label>
            <Input
              id="project-client"
              value={form.client}
              onChange={(e) => setForm({ ...form, client: e.target.value })}
            />
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="grid gap-1.5">
              <Label htmlFor="project-start">Start</Label>
              <Input
                id="project-start"
                type="date"
                value={form.start}
                onChange={(e) => setForm({ ...form, start: e.target.value })}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="project-end">End</Label>
              <Input
                id="project-end"
                type="date"
                value={form.end}
                onChange={(e) => setForm({ ...form, end: e.target.value })}
              />
            </div>
          </div>
          <div className="grid gap-1.5">
            <Label htmlFor="project-description">Scope / notes</Label>
            <Textarea
              id="project-description"
              rows={4}
              value={form.description}
              onChange={(e) => setForm({ ...form, description: e.target.value })}
            />
          </div>
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={saving}>
            Cancel
          </Button>
          <Button onClick={save} disabled={saving || !form.name.trim()}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function MoveTasksDialog({
  open,
  onOpenChange,
  project,
  projects,
  onMoved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  project?: Project;
  projects: Project[];
  onMoved: () => void;
}) {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [selected, setSelected] = React.useState<Set<string>>(new Set());
  const [moving, setMoving] = React.useState(false);

  React.useEffect(() => {
    if (!open || !project) return;
    setSelected(new Set());
    api
      .allTasks()
      .then((list) => setTasks(list.filter((t) => t.project_id !== project.id)))
      .catch((e) => toast.error(`Loading tasks failed: ${(e as Error).message}`));
  }, [open, project]);

  const projectName = (id?: number) => projects.find((p) => p.id === id)?.name ?? "—";

  async function move() {
    if (!project) return;
    setMoving(true);
    try {
      const r = await api.moveTasksToProject(project.id, [...selected]);
      toast.success(`${r.moved} task(s) moved to ${project.name}`);
      onMoved();
      onOpenChange(false);
    } catch (e) {
      toast.error(`Moving failed: ${(e as Error).message}`);
    } finally {
      setMoving(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Move tasks to {project?.name}</DialogTitle>
          <DialogDescription>
            Moved tasks take their findings along. Their category is cleared, because categories belong to a project.
          </DialogDescription>
        </DialogHeader>
        <div className="max-h-80 overflow-y-auto rounded-md border p-2">
          {tasks.length === 0 && <p className="text-muted-foreground text-sm">No tasks in other projects.</p>}
          {tasks.map((t) => (
            <label key={t.id} htmlFor={`move-${t.id}`} className="flex items-center gap-2 py-1 text-sm">
              <Checkbox
                id={`move-${t.id}`}
                checked={selected.has(t.id)}
                onCheckedChange={(v) =>
                  setSelected((s) => {
                    const next = new Set(s);
                    if (v === true) next.add(t.id);
                    else next.delete(t.id);
                    return next;
                  })
                }
              />
              <span className="min-w-0 flex-1 truncate">
                #{t.id} {(t.name?.trim() ? t.name : t.description).split("\n")[0]}
              </span>
              <span className="shrink-0 text-muted-foreground text-xs">{projectName(t.project_id)}</span>
            </label>
          ))}
        </div>
        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)} disabled={moving}>
            Cancel
          </Button>
          <Button onClick={move} disabled={moving || selected.size === 0}>
            {moving ? "Moving…" : `Move ${selected.size} task(s)`}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ProjectCard({
  project,
  active,
  onOpen,
  onEdit,
  onMove,
  onChanged,
}: {
  project: Project;
  active: boolean;
  onOpen: () => void;
  onEdit: () => void;
  onMove: () => void;
  onChanged: () => void;
}) {
  const f = project.findings;
  const findings = f.critical + f.high + f.medium + f.low + f.info;

  async function toggleArchive() {
    try {
      await api.updateProject(project.id, { status: project.status === "archived" ? "active" : "archived" });
      onChanged();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function remove() {
    if (!window.confirm(`Delete the empty project "${project.name}"?`)) return;
    try {
      await api.deleteProject(project.id);
      toast.success("Project deleted");
      onChanged();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  return (
    <Card className={active ? "border-primary" : undefined}>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <BriefcaseIcon className="size-4 shrink-0" />
          <span className="truncate">{project.name}</span>
          {project.is_default && <Badge variant="secondary">Default</Badge>}
          {project.status === "archived" && <Badge variant="outline">Archived</Badge>}
          {active && <Badge>Active</Badge>}
        </CardTitle>
        <CardDescription className="truncate">
          {project.client !== "" ? project.client : "No client"}
          {(project.start_date ?? project.end_date) &&
            ` · ${dateInput(project.start_date) !== "" ? dateInput(project.start_date) : "…"} – ${dateInput(project.end_date) !== "" ? dateInput(project.end_date) : "…"}`}
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        {project.description && <p className="line-clamp-3 text-muted-foreground text-sm">{project.description}</p>}
        <div className="grid grid-cols-3 gap-2 text-center text-sm">
          <div className="rounded-md border p-2">
            <div className="font-semibold text-lg">{project.tasks}</div>
            <div className="text-muted-foreground text-xs">
              Tasks{project.running_tasks > 0 && ` (${project.running_tasks} running)`}
            </div>
          </div>
          <div className="rounded-md border p-2">
            <div className="font-semibold text-lg">{project.conversations}</div>
            <div className="text-muted-foreground text-xs">Chats</div>
          </div>
          <div className="rounded-md border p-2">
            <div className="font-semibold text-lg">{findings}</div>
            <div className="text-muted-foreground text-xs">Findings</div>
          </div>
        </div>
        {findings > 0 && (
          <div className="flex flex-wrap gap-1.5 text-xs">
            {f.critical > 0 && <Badge className="bg-rose-700 text-white">{f.critical} critical</Badge>}
            {f.high > 0 && <Badge className="bg-red-600 text-white">{f.high} high</Badge>}
            {f.medium > 0 && <Badge className="bg-amber-600 text-white">{f.medium} medium</Badge>}
            {f.low > 0 && <Badge className="bg-blue-600 text-white">{f.low} low</Badge>}
            {f.info > 0 && <Badge variant="secondary">{f.info} info</Badge>}
          </div>
        )}
      </CardContent>
      <CardFooter className="flex flex-wrap gap-2">
        <Button size="sm" onClick={onOpen}>
          Open <ArrowRightIcon />
        </Button>
        <Button size="sm" variant="outline" onClick={onEdit}>
          <PencilIcon /> Edit
        </Button>
        <Button size="sm" variant="outline" onClick={onMove}>
          <TruckIcon /> Move tasks here
        </Button>
        {!project.is_default && (
          <Button size="sm" variant="ghost" onClick={toggleArchive}>
            {project.status === "archived" ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
            {project.status === "archived" ? "Reactivate" : "Archive"}
          </Button>
        )}
        {!project.is_default && project.tasks === 0 && project.conversations === 0 && (
          <Button size="sm" variant="ghost" className="text-destructive" onClick={remove}>
            <Trash2Icon /> Delete
          </Button>
        )}
      </CardFooter>
    </Card>
  );
}

function ProjectsPageInner() {
  const router = useRouter();
  const params = useSearchParams();
  const activeId = useActiveProjectId();
  const [projects, setProjects] = React.useState<Project[]>([]);
  const [loaded, setLoaded] = React.useState(false);
  const [editing, setEditing] = React.useState<Project | undefined>();
  const [formOpen, setFormOpen] = React.useState(false);
  const [moveTarget, setMoveTarget] = React.useState<Project | undefined>();

  const load = React.useCallback(() => {
    api
      .projects()
      .then(setProjects)
      .catch((e) => toast.error(`Loading projects failed: ${(e as Error).message}`))
      .finally(() => setLoaded(true));
  }, []);

  React.useEffect(() => {
    load();
  }, [load]);

  React.useEffect(() => {
    if (params.get("new") === "1") {
      setEditing(undefined);
      setFormOpen(true);
    }
  }, [params]);

  function open(p: Project) {
    setActiveProjectId(String(p.id));
    router.push("/dashboard");
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h1 className="font-semibold text-xl">Projects</h1>
          <p className="text-muted-foreground text-sm">
            Each project keeps its own tasks, chats, findings and report. Pick the active project in the sidebar.
          </p>
        </div>
        <Button
          onClick={() => {
            setEditing(undefined);
            setFormOpen(true);
          }}
        >
          <PlusIcon /> New project
        </Button>
      </div>
      {!loaded && <p className="text-muted-foreground text-sm">Loading…</p>}
      <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        {projects.map((p) => (
          <ProjectCard
            key={p.id}
            project={p}
            active={String(p.id) === activeId}
            onOpen={() => open(p)}
            onEdit={() => {
              setEditing(p);
              setFormOpen(true);
            }}
            onMove={() => setMoveTarget(p)}
            onChanged={load}
          />
        ))}
      </div>
      <ProjectFormDialog
        open={formOpen}
        onOpenChange={setFormOpen}
        project={editing}
        onSaved={(p) => {
          load();
          if (!editing) setActiveProjectId(String(p.id));
        }}
      />
      <MoveTasksDialog
        open={moveTarget !== undefined}
        onOpenChange={(o) => !o && setMoveTarget(undefined)}
        project={moveTarget}
        projects={projects}
        onMoved={load}
      />
    </div>
  );
}

export default function ProjectsPage() {
  return (
    <React.Suspense fallback={null}>
      <ProjectsPageInner />
    </React.Suspense>
  );
}
