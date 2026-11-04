"use client";

import * as React from "react";

import { ExternalLinkIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { api } from "@/lib/api";
import type { VulnIntelSource } from "@/lib/types";

/** Per-source switches and API keys for vuln_lookup / cve_lookup / kev_search. */
export function VulnIntelCard() {
  const [sources, setSources] = React.useState<VulnIntelSource[]>([]);
  const [loaded, setLoaded] = React.useState(false);
  const [keyInput, setKeyInput] = React.useState<Record<string, string>>({});
  const [busy, setBusy] = React.useState<string | null>(null);

  React.useEffect(() => {
    api
      .vulnIntel()
      .then((r) => setSources(r.sources ?? []))
      .catch((e) => toast.error("Failed to load vulnerability sources: " + (e as Error).message))
      .finally(() => setLoaded(true));
  }, []);

  const toggle = (id: string, on: boolean) => {
    setBusy(id);
    api
      .setVulnIntel({ enabled: { [id]: on } })
      .then((r) => setSources(r.sources ?? []))
      .catch((e) => toast.error("Save failed: " + (e as Error).message))
      .finally(() => setBusy(null));
  };

  const saveKey = (id: string, value: string) => {
    setBusy(id);
    api
      .setVulnIntel({ keys: { [id]: value } })
      .then((r) => {
        setSources(r.sources ?? []);
        setKeyInput((k) => ({ ...k, [id]: "" }));
        toast.success(value ? "API key saved" : "API key removed");
      })
      .catch((e) => toast.error("Save failed: " + (e as Error).message))
      .finally(() => setBusy(null));
  };

  const test = (src: VulnIntelSource) => {
    setBusy(src.id);
    // The Wordfence test downloads the whole feed, which can take a while on a slow line.
    const pending = src.id === "wordfence" ? toast.loading("Downloading the Wordfence feed…") : undefined;
    api
      .testVulnIntel(src.id, keyInput[src.id]?.trim() || undefined)
      .then((r) => {
        if (r.ok) toast.success(`${src.name}: ${r.detail ?? "ok"}`, { id: pending });
        else toast.error(`${src.name}: ${r.error ?? "test failed"}`, { id: pending });
      })
      .catch((e) => toast.error(`${src.name}: ${(e as Error).message}`, { id: pending }))
      .finally(() => setBusy(null));
  };

  return (
    <Card className="mb-4 break-inside-avoid md:mb-6">
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <ShieldCheckIcon className="size-4" />
          Vulnerability intelligence sources
        </CardTitle>
        <CardDescription>
          Sources behind the <b>vuln_lookup</b>, <b>cve_lookup</b> and <b>kev_search</b> tools. Each source can be switched off on its own;
          sources that need a key are skipped until one is saved. Requests use the web search egress proxy, never the recording proxy.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col divide-y">
        {!loaded && <p className="text-muted-foreground text-sm">Loading…</p>}
        {sources.map((src) => {
          const needsKey = src.key === "required" && !src.key_set;
          const disabled = !loaded || busy === src.id;
          return (
            <div key={src.id} className="flex flex-col gap-2 py-3 first:pt-0 last:pb-0">
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-sm font-medium">{src.name}</span>
                    {src.key === "required" && (
                      <Badge variant="outline" className="text-xs">
                        API key required
                      </Badge>
                    )}
                    {src.key === "optional" && (
                      <Badge variant="outline" className="text-xs">
                        API key optional
                      </Badge>
                    )}
                    {src.key_set && <span className="text-xs text-emerald-500">Key configured</span>}
                    {src.enabled && needsKey && <span className="text-xs text-amber-500">Skipped until a key is saved</span>}
                  </div>
                  <p className="text-muted-foreground mt-1 text-xs">
                    {src.description}{" "}
                    <a href={src.url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-0.5 underline">
                      {src.key === "none" ? "Website" : "Get a key"}
                      <ExternalLinkIcon className="size-3" />
                    </a>
                  </p>
                </div>
                <Switch
                  checked={src.enabled}
                  disabled={disabled}
                  onCheckedChange={(on) => toggle(src.id, on)}
                  aria-label={`Use ${src.name}`}
                />
              </div>
              <div className="flex items-center gap-2">
                {src.key !== "none" && (
                  <>
                    <Input
                      type="password"
                      autoComplete="off"
                      className="h-8 text-sm"
                      placeholder={src.key_set ? "Configured (leave empty to keep)" : "API key"}
                      value={keyInput[src.id] ?? ""}
                      disabled={disabled}
                      onChange={(e) => setKeyInput((k) => ({ ...k, [src.id]: e.target.value }))}
                    />
                    <Button
                      size="sm"
                      onClick={() => saveKey(src.id, (keyInput[src.id] ?? "").trim())}
                      disabled={disabled || (keyInput[src.id] ?? "").trim() === ""}
                    >
                      Save
                    </Button>
                    {src.key_set && (
                      <Button size="sm" variant="ghost" onClick={() => saveKey(src.id, "")} disabled={disabled}>
                        Remove
                      </Button>
                    )}
                  </>
                )}
                <Button
                  size="sm"
                  variant="outline"
                  className={src.key === "none" ? "ml-auto" : "shrink-0"}
                  onClick={() => test(src)}
                  disabled={disabled || (src.key === "required" && !src.key_set && !(keyInput[src.id] ?? "").trim())}
                >
                  {busy === src.id ? "Working…" : "Test"}
                </Button>
              </div>
            </div>
          );
        })}
      </CardContent>
    </Card>
  );
}
