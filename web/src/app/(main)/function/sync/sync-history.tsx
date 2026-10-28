"use client";

import * as React from "react";

import { RefreshCwIcon } from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import { syncResultStatus, syncStatusLabels } from "@/lib/sync-result";
import type { SSSyncRequest, SSSyncResult, SSSyncRun, SSSyncStatus } from "@/lib/types";

const assetLabels: Record<string, string> = { subdomain: "Subdomain", service: "Service", app: "App", ip: "IP" };
const outcomeLabels = { created: "Created", updated: "Updated", unchanged: "Unchanged" };

export function showSyncResult(result: SSSyncResult) {
  const status = syncResultStatus(result);
  if (status === "running") {
    toast.success("Background sync started; check progress in the history");
    return;
  }
  if (status === "success") toast.success("Sync succeeded");
  else if (status === "failed" || status === "interrupted")
    toast.error(`Sync status: ${syncStatusLabels[status]}; see the result details`);
  else toast.warning(`Sync status: ${syncStatusLabels[status]}; see the unfinished items`);
}

function SyncBadge({ status }: { status: SSSyncStatus }) {
  let variant: "destructive" | "secondary" | "outline" = "outline";
  if (status === "failed") variant = "destructive";
  if (status === "success") variant = "secondary";
  return <Badge variant={variant}>{syncStatusLabels[status]}</Badge>;
}

export function SyncResult({ result }: { result: SSSyncResult }) {
  return (
    <div className="space-y-3 rounded-md border bg-muted/40 p-3 text-sm">
      <div className="flex flex-wrap items-center gap-3">
        <SyncBadge status={syncResultStatus(result)} />
        {result.run_id && <span className="text-muted-foreground">Run #{result.run_id}</span>}
      </div>
      {result.stats && (
        <p>
          Source records: fetched {result.stats.fetched}, written {result.stats.written}, skipped {result.stats.skipped}, failed{" "}
          {result.stats.failed}.
        </p>
      )}
      {result.unique_assets && (
        <p>
          Unique assets this run {result.unique_assets.total}: created {result.unique_assets.created}, updated{" "}
          {result.unique_assets.updated}, unchanged {result.unique_assets.unchanged}. Direct assets{" "}
          {result.unique_assets.direct}, derived assets {result.unique_assets.derived}.
        </p>
      )}
      <div className="flex flex-wrap gap-2">
        {Object.entries(result.synced).map(([key, count]) => (
          <Badge key={key} variant="secondary">
            {assetLabels[key] ?? key}: {count} writes
          </Badge>
        ))}
      </div>
      <p className="text-muted-foreground text-xs">
        Write counts include updates to existing assets and derived assets, so they don't equal the number of new or unique assets. Unique assets are deduplicated by this run's asset
        ID; "succeeded" means no failures were found in this read and processing, and live source data isn't guaranteed to be a fixed snapshot. Source task scan progress doesn't mean the sync is complete.
      </p>
      {!!result.companies?.length && <p>Companies created/updated: {result.companies.join(", ")}</p>}
      {!!result.warnings?.length && (
        <ul className="list-inside list-disc text-amber-700 dark:text-amber-400">
          {[...new Set(result.warnings)].map((message) => (
            <li key={message}>{message}</li>
          ))}
        </ul>
      )}
      {!!result.errors?.length && (
        <ul className="list-inside list-disc text-destructive">
          {[...new Set(result.errors)].slice(0, 20).map((message) => (
            <li key={message}>{message}</li>
          ))}
          {result.errors.length > 20 && <li>{result.errors.length} errors in total; see the per-item records.</li>}
        </ul>
      )}
      {!!result.targets?.length && (
        <div className="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Source scope</TableHead>
                <TableHead>Type</TableHead>
                <TableHead>Sync status</TableHead>
                <TableHead>Fetched / written / skipped / failed</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {result.targets.map((target) => (
                <TableRow key={`${target.target}-${target.asset_type}`}>
                  <TableCell>
                    <div className="max-w-xs break-all">{target.target}</div>
                    {target.error && <p className="text-destructive text-xs">{target.error}</p>}
                  </TableCell>
                  <TableCell>{assetLabels[target.asset_type] ?? target.asset_type}</TableCell>
                  <TableCell>
                    <SyncBadge status={target.status} />
                    {target.truncated && <p className="text-amber-700 text-xs dark:text-amber-400">Some data was not read yet</p>}
                  </TableCell>
                  <TableCell>
                    {target.stats.fetched} / {target.stats.written} / {target.stats.skipped} / {target.stats.failed}
                    {target.stats.created !== undefined && (
                      <p className="text-muted-foreground text-xs">
                        Primary assets: created {target.stats.created} / updated {target.stats.updated} / unchanged{" "}
                        {target.stats.unchanged}
                      </p>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </div>
      )}
    </div>
  );
}

export function SyncHistory({
  refreshKey,
  canRetry,
  onChanged,
  syncing,
  onSyncingChange,
}: {
  refreshKey: number;
  canRetry: boolean;
  onChanged: () => void;
  syncing: boolean;
  onSyncingChange: (value: boolean) => void;
}) {
  const [page, setPage] = React.useState(1);
  const [revision, setRevision] = React.useState(0);
  const [runs, setRuns] = React.useState<SSSyncRun[]>([]);
  const [total, setTotal] = React.useState(0);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState("");
  const [selected, setSelected] = React.useState<number | null>(null);
  let emptyMessage = "No sync history yet";
  if (error) emptyMessage = "History temporarily unavailable";
  if (loading) emptyMessage = "Loading…";
  // biome-ignore lint/correctness/useExhaustiveDependencies: Explicit refreshes and completed runs must reload server history.
  React.useEffect(() => {
    let active = true;
    setLoading(true);
    api
      .ssSyncHistory(page)
      .then((data) => {
        if (!active) return;
        setRuns(data.runs ?? []);
        setTotal(data.total);
        setError("");
      })
      .catch((reason: Error) => {
        if (active) setError(reason.message);
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [page, revision, refreshKey]);
  React.useEffect(() => {
    if (loading || (!syncing && !runs.some((run) => run.status === "running"))) return;
    const timer = setTimeout(() => setRevision((value) => value + 1), 5000);
    return () => clearTimeout(timer);
  }, [runs, syncing, loading]);
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="text-base">Sync history</CardTitle>
        <Button variant="ghost" size="sm" disabled={loading} onClick={() => setRevision((value) => value + 1)}>
          <RefreshCwIcon className={loading ? "size-4 animate-spin" : "size-4"} />
          Refresh
        </Button>
      </CardHeader>
      <CardContent className="space-y-4">
        <p className="text-muted-foreground text-sm">
          History is stored on the server and stays viewable after the data source is disabled. Click a run to see per-item results and why items didn't finish.
        </p>
        {error && (
          <p role="alert" className="text-destructive text-sm">
            Failed to load history: {error}
          </p>
        )}
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Run / started at</TableHead>
              <TableHead>Sync scope</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Details</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {runs.map((run) => (
              <TableRow key={run.id}>
                <TableCell>
                  #{run.id}
                  <div className="text-muted-foreground text-xs">{new Date(run.started_at).toLocaleString()}</div>
                </TableCell>
                <TableCell>
                  <div className="max-w-md break-all">{run.request.targets.join(", ")}</div>
                  <span className="text-muted-foreground text-xs">
                    {run.request.asset_types.map((type) => assetLabels[type] ?? type).join(" / ")}
                  </span>
                </TableCell>
                <TableCell>
                  <SyncBadge status={run.status} />
                </TableCell>
                <TableCell>
                  <Button variant="outline" size="sm" onClick={() => setSelected(selected === run.id ? null : run.id)}>
                    {selected === run.id ? "Hide" : "View"}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {!runs.length && (
              <TableRow>
                <TableCell colSpan={4} className="py-6 text-center text-muted-foreground">
                  {emptyMessage}
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
        <div className="flex items-center justify-end gap-2">
          <Button variant="outline" size="sm" disabled={page <= 1 || loading} onClick={() => setPage(page - 1)}>
            Previous
          </Button>
          <span className="text-xs">
            Page {page} · {total} total
          </span>
          <Button
            variant="outline"
            size="sm"
            disabled={page * 20 >= total || loading}
            onClick={() => setPage(page + 1)}
          >
            Next
          </Button>
        </div>
        {selected !== null && (
          <HistoryDetail
            key={selected}
            id={selected}
            refreshKey={refreshKey + revision}
            canRetry={canRetry}
            onChanged={onChanged}
            onSyncingChange={onSyncingChange}
          />
        )}
      </CardContent>
    </Card>
  );
}

function HistoryDetail({
  id,
  refreshKey,
  canRetry,
  onChanged,
  onSyncingChange,
}: {
  id: number;
  refreshKey: number;
  canRetry: boolean;
  onChanged: () => void;
  onSyncingChange: (value: boolean) => void;
}) {
  const [page, setPage] = React.useState(1);
  const [data, setData] = React.useState<Awaited<ReturnType<typeof api.ssSyncHistoryDetail>> | null>(null);
  const [error, setError] = React.useState("");
  const [retrying, setRetrying] = React.useState(false);
  // biome-ignore lint/correctness/useExhaustiveDependencies: A refreshed run must reload its persisted item results.
  React.useEffect(() => {
    let active = true;
    api
      .ssSyncHistoryDetail(id, page)
      .then((value) => {
        if (active) {
          setData(value);
          setError("");
        }
      })
      .catch((reason: Error) => {
        if (active) setError(reason.message);
      });
    return () => {
      active = false;
    };
  }, [id, page, refreshKey]);
  const retry = async (request: SSSyncRequest) => {
    setRetrying(true);
    onSyncingChange(true);
    try {
      showSyncResult(await api.ssStartSync(request));
    } catch (reason) {
      toast.error(`Retry failed: ${(reason as Error).message}`);
    } finally {
      setRetrying(false);
      onSyncingChange(false);
      onChanged();
    }
  };
  if (error)
    return (
      <p role="alert" className="text-destructive text-sm">
        Failed to load details: {error}
      </p>
    );
  if (!data) return <p className="text-sm">Loading details…</p>;
  const unfinished = data.run.result?.targets?.filter((target) => target.status !== "success") ?? [];
  return (
    <div className="space-y-3 rounded-md border p-3">
      <h3 className="font-medium text-sm">Run #{id}</h3>
      {data.run.result && <SyncResult result={data.run.result} />}
      {data.run.status !== "running" && (
        <div className="flex flex-wrap gap-2">
          {(data.run.result?.stats?.failed ?? 0) + (data.run.result?.stats?.skipped ?? 0) > 0 && (
            <Button
              variant="outline"
              size="sm"
              disabled={retrying}
              onClick={() => retry({ ...data.run.request, retry_run_id: id })}
            >
              Retry only failed and skipped records
            </Button>
          )}
          {data.run.request.background && data.run.status !== "success" && (
            <Button
              variant="outline"
              size="sm"
              disabled={!canRetry || retrying}
              onClick={async () => {
                setRetrying(true);
                try {
                  showSyncResult(await api.ssResumeSync(id));
                } catch (reason) {
                  toast.error(`Resume failed: ${(reason as Error).message}`);
                } finally {
                  setRetrying(false);
                  onChanged();
                }
              }}
            >
              Continue from the saved page
            </Button>
          )}
          {unfinished.map((target) => (
            <Button
              key={`${target.target}-${target.asset_type}`}
              variant="outline"
              size="sm"
              disabled={!canRetry || retrying}
              onClick={() => retry({ ...data.run.request, targets: [target.target], asset_types: [target.asset_type] })}
            >
              Resync {target.target} / {assetLabels[target.asset_type] ?? target.asset_type}
            </Button>
          ))}
          {!unfinished.length && ["failed", "partial", "interrupted"].includes(data.run.status) && (
            <Button
              variant="outline"
              size="sm"
              disabled={!canRetry || retrying}
              onClick={() => retry(data.run.request)}
            >
              Resync this run's scope
            </Button>
          )}
        </div>
      )}
      <p className="text-muted-foreground text-xs">
        Background syncs keep running after the page is closed; resuming skips processed records and continues from the saved page. Resync creates a new run and checks the whole scope. Later source pages may shift as scans change.
      </p>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Source asset / scope</TableHead>
            <TableHead>Type</TableHead>
            <TableHead>Result</TableHead>
            <TableHead>Reason</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {(data.items ?? []).map((item, index) => (
            <TableRow key={item.id}>
              <TableCell>
                <div className="max-w-xs break-all">{item.source_asset_id || "No source asset ID provided"}</div>
                <div className="text-muted-foreground text-xs">{item.target}</div>
                {item.raw_omitted ? (
                  <p className="text-amber-700 text-xs">Raw JSON exceeded the limit and was not saved</p>
                ) : (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={async () => {
                      try {
                        const detail = await api.ssSyncHistoryDetail(id, (page - 1) * 20 + index + 1, 1, true);
                        const raw = detail.items[0]?.raw;
                        if (!raw) {
                          toast.error("This record did not keep the raw JSON");
                          return;
                        }
                        const url = URL.createObjectURL(
                          new Blob([JSON.stringify(raw, null, 2)], { type: "application/json" }),
                        );
                        const link = document.createElement("a");
                        link.href = url;
                        link.download = `sync-${id}-${item.id}.json`;
                        link.click();
                        setTimeout(() => URL.revokeObjectURL(url), 1000);
                      } catch (reason) {
                        toast.error(`Failed to load raw data: ${(reason as Error).message}`);
                      }
                    }}
                  >
                    Download raw JSON
                  </Button>
                )}
              </TableCell>
              <TableCell>{assetLabels[item.asset_type] ?? item.asset_type}</TableCell>
              <TableCell>
                {(
                  { written: "Written", success: "Written", skipped: "Skipped", failed: "Failed" } as Record<string, string>
                )[item.status] ?? item.status}
                {item.asset_id ? ` #${item.asset_id}` : ""}
                {item.outcome && <div className="text-muted-foreground text-xs">{outcomeLabels[item.outcome]}</div>}
              </TableCell>
              <TableCell className="max-w-md whitespace-normal break-all">{item.reason || "—"}</TableCell>
            </TableRow>
          ))}
          {!data.items?.length && (
            <TableRow>
              <TableCell colSpan={4} className="text-center text-muted-foreground">
                No per-item records yet
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
      <div className="flex items-center justify-end gap-2">
        <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>
          Previous
        </Button>
        <span className="text-xs">
          Page {page} · {data.total} items
        </span>
        <Button variant="outline" size="sm" disabled={page * 20 >= data.total} onClick={() => setPage(page + 1)}>
          Next
        </Button>
      </div>
    </div>
  );
}
