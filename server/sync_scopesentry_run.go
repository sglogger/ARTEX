package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

type ssSyncReq struct {
	Dimension     string            `json:"dimension"`
	Targets       []string          `json:"targets"`
	TargetIDs     map[string]string `json:"target_ids,omitempty"`
	AssetTypes    []string          `json:"asset_types"`
	CreateCompany bool              `json:"create_company"`
	PageSize      int               `json:"page_size"`
	Background    bool              `json:"background,omitempty"`
	RetryRunID    int64             `json:"retry_run_id,omitempty"`
}

type ssTargetResult struct {
	Target         string       `json:"target"`
	SourceTargetID string       `json:"source_target_id,omitempty"`
	AssetType      string       `json:"asset_type"`
	Status         string       `json:"status"`
	Stats          db.SyncStats `json:"stats"`
	Truncated      bool         `json:"truncated"`
	Error          string       `json:"error,omitempty"`
}

type ssSyncResult struct {
	RunID               int64               `json:"run_id"`
	Status              string              `json:"status"`
	Synced              map[string]int      `json:"synced"`
	Companies           []string            `json:"companies"`
	Warnings            []string            `json:"warnings"`
	Errors              []string            `json:"errors"`
	Stats               db.SyncStats        `json:"stats"`
	UniqueAssets        db.SyncUniqueAssets `json:"unique_assets"`
	Counting            string              `json:"counting"`
	CountingDescription string              `json:"counting_description"`
	Targets             []ssTargetResult    `json:"targets"`
	StartedAt           time.Time           `json:"started_at"`
	FinishedAt          *time.Time          `json:"finished_at,omitempty"`
}

func newSSSyncResult() ssSyncResult {
	return ssSyncResult{
		Status: "running", Synced: map[string]int{"subdomain": 0, "app": 0, "service": 0, "ip": 0},
		Companies: []string{}, Warnings: []string{}, Errors: []string{}, Targets: []ssTargetResult{},
		Counting:            "source_records_and_unique_assets",
		CountingDescription: "Processing counts classify source records as added, updated, unchanged, skipped and failed; added/updated ignore time fields such as last_seen. Deduplicated assets are counted by source record and the asset IDs touched by derived writes, and each asset is classified in the order added, updated, unchanged. Paginated traversal does not guarantee snapshot consistency while the source changes.",
	}
}

func (req *ssSyncReq) normalize() error {
	if req.Dimension != "project" && req.Dimension != "task" {
		return errors.New("dimension must be project or task")
	}
	if len(req.Targets) == 0 {
		return errors.New("targets must not be empty")
	}
	targets := []string{}
	seen := map[string]bool{}
	for _, target := range req.Targets {
		if strings.TrimSpace(target) == "" {
			return errors.New("target must not be blank")
		}
		if !seen[target] {
			targets = append(targets, target)
			seen[target] = true
		}
	}
	req.Targets = targets
	if len(req.AssetTypes) == 0 {
		req.AssetTypes = []string{"subdomain", "service", "app"}
	}
	types := []string{}
	seen = map[string]bool{}
	for _, kind := range req.AssetTypes {
		if kind != "subdomain" && kind != "service" && kind != "app" {
			return fmt.Errorf("unknown asset type: %s", kind)
		}
		if !seen[kind] {
			types = append(types, kind)
			seen[kind] = true
		}
	}
	req.AssetTypes = types
	if req.PageSize <= 0 || req.PageSize > 500 {
		req.PageSize = syncDefaultPage
	}
	return nil
}

func (req ssSyncReq) sourceTargetID(target string) string {
	if req.Dimension == "project" {
		return target
	}
	return req.TargetIDs[target]
}

// syncSSRun keeps the legacy synchronous response while recording the lifecycle
// before dialing the source. A browser disconnect cannot erase committed items.
func (s *Server) syncSSRun(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var req ssSyncReq
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if err := req.normalize(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	result := newSSSyncResult()
	seenAssets := map[int64]db.SyncAssetChange{}
	requestJSON, _ := json.Marshal(req)
	initialJSON, _ := json.Marshal(result)
	source, sourceErr := s.findMCPByName(scopeSentryMCPName)
	var sourceID *int64
	if source != nil {
		sourceID = &source.ID
	}
	run, err := pg.CreateSyncRun(r.Context(), sourceID, requestJSON, initialJSON)
	if err != nil {
		writeErr(w, 500, "could not save sync history, sync not started: "+err.Error())
		return
	}
	result.RunID, result.StartedAt = run.ID, run.StartedAt
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	finished := false
	defer func() {
		if !finished {
			result.Status = "interrupted"
			ssAppendIssue(&result.Errors, "sync processing exited abnormally; committed records can be viewed in the history details")
			_ = s.persistSSResult(context.Background(), &result)
		}
	}()
	finish := func() {
		result.UniqueAssets = ssUniqueAssets(seenAssets)
		if ctx.Err() != nil {
			result.Status = "interrupted"
			ssAppendIssue(&result.Errors, "sync interrupted: "+ctx.Err().Error())
		} else {
			result.Status = ssOverallStatus(result)
		}
		if err := s.persistSSResult(context.WithoutCancel(r.Context()), &result); err != nil {
			result.Status = "failed"
			ssAppendIssue(&result.Errors, "failed to save the final sync state, check it via the history ID: "+err.Error())
		} else {
			finished = true
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
			defer cancel()
			if stored, err := pg.GetSyncRun(refreshCtx, result.RunID); err == nil && stored != nil {
				normalizeSSHistoryResult(stored)
				_ = json.Unmarshal(stored.Result, &result)
			}
		}
		writeJSON(w, 200, result)
	}
	if sourceErr != nil {
		ssAppendIssue(&result.Errors, "failed to read data source: "+sourceErr.Error())
		finish()
		return
	}
	if source == nil || !source.Enabled {
		ssAppendIssue(&result.Errors, "ScopeSentry data source does not exist or is not enabled")
		finish()
		return
	}
	cl, err := s.scopeSentryClient(ctx)
	if err != nil {
		ssAppendIssue(&result.Errors, "failed to connect to data source: "+err.Error())
		finish()
		return
	}
	defer cl.Close()
	for _, target := range req.Targets {
		if ctx.Err() != nil {
			break
		}
		if req.Dimension == "project" && req.CreateCompany {
			s.syncSSCompany(ctx, cl, target, &result)
		}
		for _, kind := range req.AssetTypes {
			if ctx.Err() != nil {
				break
			}
			one := ssTargetResult{Target: target, SourceTargetID: req.sourceTargetID(target), AssetType: kind, Status: "running"}
			result.Targets = append(result.Targets, one)
			index := len(result.Targets) - 1
			if err := s.persistSSResult(ctx, &result); err != nil {
				ssAppendIssue(&result.Errors, "failed to save sync progress: "+err.Error())
				finish()
				return
			}
			ssType := map[string]string{"subdomain": "subdomain", "service": "asset", "app": "app"}[kind]
			items, truncated, fetchErr := s.ssPageAll(ctx, cl, ssType, ssAssetQuery(ssType, req.Dimension, target), req.PageSize)
			one.Stats.Fetched = len(items)
			one.Truncated = truncated
			result.Stats.Fetched += len(items)
			if fetchErr != nil {
				one.Error = fetchErr.Error()
				ssAppendIssue(&result.Errors, fmt.Sprintf("%s(%s) fetch incomplete: %v", kind, target, fetchErr))
			}
			if truncated {
				ssAppendIssue(&result.Warnings, fmt.Sprintf("%s(%s) exceeds %d items; the remaining data was not processed this time", kind, target, syncMaxPerType))
			}
			if err := pg.AddSyncFetched(ctx, run.ID, len(items)); err != nil {
				one.Error = "failed to save fetch count: " + err.Error()
				ssAppendIssue(&result.Errors, one.Error)
				one.Status = "failed"
				result.Targets[index] = one
				finish()
				return
			}
			// Preserve per-target fetched counts and source errors before any item
			// commits. History derives written outcomes from committed observations.
			result.Targets[index] = one
			if err := s.persistSSResult(ctx, &result); err != nil {
				ssAppendIssue(&result.Errors, "failed to save fetch progress: "+err.Error())
				finish()
				return
			}
			// Even when a later page failed, preceding pages remain eligible for
			// ingestion. An actual cancellation stops new writes instead.
			for itemIndex, raw := range items {
				if ctx.Err() != nil {
					break
				}
				observation := &db.SyncObservation{RunID: run.ID, Target: target,
					SourceTargetID: one.SourceTargetID, AssetType: kind, ItemIndex: itemIndex,
					SourceAssetID: ssSourceAssetID(raw), Raw: raw}
				if err := s.storeSSItem(ctx, observation); err != nil {
					one.Error = "failed to save asset processing details: " + err.Error()
					ssAppendIssue(&result.Errors, one.Error)
					one.Status = "failed"
					result.Targets[index] = one
					finish()
					return
				}
				ssCountObservation(&one.Stats, observation)
				ssCountObservation(&result.Stats, observation)
				if observation.Status == "written" {
					ssMergeAssetChanges(seenAssets, observation.AssetChanges)
					result.Synced[kind]++
					result.Synced["ip"] += observation.DerivedIPWritten
				} else if observation.Status == "skipped" {
					ssAppendIssue(&result.Warnings, fmt.Sprintf("%s(%s) item %d: %s", kind, target, itemIndex+1, observation.Reason))
				} else {
					ssAppendIssue(&result.Errors, fmt.Sprintf("%s(%s) item %d: %s", kind, target, itemIndex+1, observation.Reason))
				}
			}
			one.Status = ssTargetStatus(one)
			if ctx.Err() != nil {
				one.Status = "interrupted"
			}
			result.Targets[index] = one
		}
	}
	finish()
}

func (s *Server) syncSSCompany(ctx context.Context, cl ssToolCaller, target string, result *ssSyncResult) {
	name, roots, err := s.ssProjectMeta(ctx, cl, target)
	if err != nil {
		ssAppendIssue(&result.Warnings, fmt.Sprintf("failed to fetch details of project %s: %v", target, err))
		return
	}
	if strings.TrimSpace(name) == "" {
		ssAppendIssue(&result.Warnings, fmt.Sprintf("project %s has no name; no company created", target))
		return
	}
	cs := s.companyStore()
	id, err := cs.UpsertByName(name)
	if err != nil {
		ssAppendIssue(&result.Warnings, fmt.Sprintf("failed to create company %s: %v", name, err))
		return
	}
	result.Companies = append(result.Companies, name)
	if len(roots) > 0 {
		_, _, _, issues := cs.AddScope(id, roots, "scopesentry:"+name)
		for _, issue := range issues {
			ssAppendIssue(&result.Warnings, fmt.Sprintf("failed to write scope for company %s: %s", name, issue))
		}
	}
	// AddScope already rebuilds existing attribution atomically; each following
	// asset upsert resolves its own company, so another global rebuild is redundant.
}

func (s *Server) storeSSItem(ctx context.Context, item *db.SyncObservation) error {
	prepared, err := ssPrepareItem(item.AssetType, item.Raw)
	if err != nil {
		item.Status, item.Reason = "failed", err.Error()
	} else if prepared.skipReason != "" {
		item.Status, item.Reason = "skipped", prepared.skipReason
	} else {
		item.Status = "written"
		if err = s.m.pg.ApplySyncObservation(ctx, item, func(as *db.AssetStore) error {
			return prepared.apply(as, item)
		}); err == nil {
			return nil
		}
		item.Status, item.Reason = "failed", err.Error()
		item.AssetID, item.DerivedIPWritten = nil, 0
		item.Outcome, item.AssetChanges = "", nil
	}
	// The failure detail must survive cancellation of the source/request context.
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.m.pg.RecordSyncObservation(persistCtx, item)
}

func (s *Server) persistSSResult(ctx context.Context, result *ssSyncResult) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if result.Status != "running" {
		now := time.Now().UTC()
		result.FinishedAt = &now
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return s.m.pg.UpdateSyncRun(ctx, result.RunID, result.Status, raw)
}

func ssCountObservation(stats *db.SyncStats, item *db.SyncObservation) {
	switch item.Status {
	case "written":
		stats.Written++
		switch item.Outcome {
		case "created":
			stats.Created++
		case "updated":
			stats.Updated++
		case "unchanged":
			stats.Unchanged++
		}
	case "skipped":
		stats.Skipped++
	case "failed":
		stats.Failed++
	}
	stats.DerivedIPWritten += item.DerivedIPWritten
	stats.DerivedIPFailed += item.DerivedIPFailed
}

func ssMergeAssetChanges(seen map[int64]db.SyncAssetChange, changes []db.SyncAssetChange) {
	for _, change := range changes {
		if previous, exists := seen[change.AssetID]; exists {
			change.Direct = change.Direct || previous.Direct
			if previous.Outcome == "created" || previous.Outcome == "updated" && change.Outcome != "created" {
				change.Outcome = previous.Outcome
			}
		}
		seen[change.AssetID] = change
	}
}

func ssUniqueAssets(seen map[int64]db.SyncAssetChange) db.SyncUniqueAssets {
	stats := db.SyncUniqueAssets{Total: len(seen)}
	for _, change := range seen {
		if change.Direct {
			stats.Direct++
		} else {
			stats.Derived++
		}
		switch change.Outcome {
		case "created":
			stats.Created++
		case "updated":
			stats.Updated++
		case "unchanged":
			stats.Unchanged++
		}
	}
	return stats
}

func ssTargetStatus(result ssTargetResult) string {
	if result.Error == "" && !result.Truncated && result.Stats.Skipped == 0 && result.Stats.Failed == 0 {
		return "success"
	}
	if result.Stats.Written > 0 {
		return "partial"
	}
	return "failed"
}

func ssOverallStatus(result ssSyncResult) string {
	anySuccess, allSuccess := false, len(result.Targets) > 0
	for _, one := range result.Targets {
		anySuccess = anySuccess || one.Status == "success" || one.Stats.Written > 0
		allSuccess = allSuccess && one.Status == "success"
	}
	if allSuccess && len(result.Warnings) == 0 && len(result.Errors) == 0 {
		return "success"
	}
	if anySuccess {
		return "partial"
	}
	return "failed"
}

func ssAppendIssue(issues *[]string, message string) {
	const limit = 40
	if len(*issues) < limit {
		*issues = append(*issues, message)
	} else if len(*issues) == limit {
		*issues = append(*issues, "see the sync history details for the rest")
	}
}
