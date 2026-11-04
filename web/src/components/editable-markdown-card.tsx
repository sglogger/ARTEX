"use client";

import * as React from "react";

import { PencilIcon } from "lucide-react";
import { toast } from "sonner";

import { Markdown } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";

/** A card showing Markdown that a person can edit in place, with a write/preview switch. */
export function EditableMarkdownCard({
  title,
  value,
  emptyText,
  addLabel,
  placeholder,
  hint,
  readOnly,
  actions,
  banner,
  onSave,
}: {
  title: string;
  value?: string;
  emptyText: string;
  addLabel: string;
  placeholder?: string;
  /** Shown next to the save button while editing. */
  hint?: React.ReactNode;
  readOnly?: boolean;
  /** Extra header buttons (e.g. copy), shown when not editing. */
  actions?: React.ReactNode;
  /** Content above the text (alerts), shown when not editing. */
  banner?: React.ReactNode;
  /** Persists the text; throws to keep the editor open. */
  onSave: (text: string) => Promise<void>;
}) {
  const [editing, setEditing] = React.useState(false);
  const [draft, setDraft] = React.useState(value ?? "");
  const [saving, setSaving] = React.useState(false);

  function startEdit() {
    setDraft(value ?? "");
    setEditing(true);
  }

  async function save() {
    setSaving(true);
    try {
      await onSave(draft);
      setEditing(false);
      toast.success(`${title} saved`);
    } catch (e) {
      toast.error(`Saving failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  let body: React.ReactNode;
  if (editing) {
    body = (
      <div className="flex flex-col gap-3">
        <Tabs defaultValue="write">
          <TabsList>
            <TabsTrigger value="write">Write</TabsTrigger>
            <TabsTrigger value="preview">Preview</TabsTrigger>
          </TabsList>
          <TabsContent value="write">
            <Textarea
              aria-label={`${title} (Markdown)`}
              rows={14}
              className="font-mono text-sm"
              placeholder={placeholder}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
          </TabsContent>
          <TabsContent value="preview">
            <div className="min-h-24 rounded-md border bg-muted/20 p-3">
              {draft.trim() ? (
                <Markdown text={draft} />
              ) : (
                <p className="text-muted-foreground text-sm">Nothing to preview.</p>
              )}
            </div>
          </TabsContent>
        </Tabs>
        <div className="flex items-center justify-between gap-2">
          <div className="text-muted-foreground text-xs">{hint}</div>
          <div className="flex shrink-0 gap-2">
            <Button size="sm" variant="ghost" onClick={() => setEditing(false)} disabled={saving}>
              Cancel
            </Button>
            <Button size="sm" onClick={save} disabled={saving || draft === (value ?? "")}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </div>
        </div>
      </div>
    );
  } else if (value) {
    body = (
      <>
        {banner}
        <Markdown text={value} />
      </>
    );
  } else {
    body = (
      <>
        {banner}
        <p className="text-muted-foreground text-sm">{emptyText}</p>
      </>
    );
  }

  return (
    <Card>
      <CardHeader className="flex-row items-center justify-between">
        <CardTitle className="text-sm">{title}</CardTitle>
        {!editing && (
          <div className="flex items-center gap-2">
            {actions}
            {!readOnly && (
              <Button size="sm" variant="outline" onClick={startEdit}>
                <PencilIcon /> {value ? "Edit" : addLabel}
              </Button>
            )}
          </div>
        )}
      </CardHeader>
      <CardContent>{body}</CardContent>
    </Card>
  );
}
