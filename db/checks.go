package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// CheckSpec identifies a bounded check independently of the intent's wording.
type CheckSpec struct {
	Target    string `json:"target"`
	Mechanism string `json:"mechanism"`
}

func (c CheckSpec) Key() string {
	target := strings.TrimSpace(c.Target)
	if parsed, err := url.Parse(target); err == nil && parsed.Host != "" && parsed.Scheme != "" {
		parsed.Scheme, parsed.Host, parsed.Fragment = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host), ""
		if parsed.Path == "" {
			parsed.Path = "/"
		}
		parsed.RawQuery = parsed.Query().Encode()
		target = parsed.String()
	}
	raw, _ := json.Marshal([]string{target, strings.ToLower(strings.TrimSpace(c.Mechanism))})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

const MaxCheckAttempts = 3

type AuthCandidate struct {
	ID       string `json:"id"`    // opaque reference; never a password
	State    string `json:"state"` // remaining|failed|skipped|verified
	Reason   string `json:"reason,omitempty"`
	Attempts int    `json:"attempts,omitempty"` // includes requests interrupted by rate limits/transport
}

func (c AuthCandidate) requestAttempts() int {
	if c.Attempts == 0 && (c.State == "failed" || c.State == "verified") {
		return 1
	}
	return c.Attempts
}

type AuthCoverage struct {
	Request          string          `json:"request"` // method, endpoint, encoding and field names
	Budget           int             `json:"budget"`
	Candidates       []AuthCandidate `json:"candidates"`
	Identity         string          `json:"identity,omitempty"`
	IdentityEvidence string          `json:"identity_evidence,omitempty"`
	StopReason       string          `json:"stop_reason,omitempty"`
}

type Checkpoint struct {
	Result     string        `json:"result"` // positive|negative|partial|blocked
	Scope      []string      `json:"scope"`
	Evidence   string        `json:"evidence"`
	Gaps       []string      `json:"gaps"`
	Barrier    string        `json:"barrier,omitempty"`
	NextAction string        `json:"next_action,omitempty"`
	Auth       *AuthCoverage `json:"auth,omitempty"`
}

func (c Checkpoint) Validate() error {
	if len(c.Scope) == 0 || strings.TrimSpace(c.Evidence) == "" {
		return fmt.Errorf("a check result must record the actual scope and evidence")
	}
	switch c.Result {
	case "positive", "negative":
		if len(c.Gaps) > 0 {
			return fmt.Errorf("a check with gaps can only be saved as partial/blocked")
		}
	case "partial", "blocked":
		if len(c.Gaps) == 0 {
			return fmt.Errorf("partial/blocked must describe the gaps")
		}
	default:
		return fmt.Errorf("result must be positive/negative/partial/blocked")
	}
	if c.Result == "blocked" && strings.TrimSpace(c.Barrier) == "" {
		return fmt.Errorf("blocked must describe the barrier")
	}
	if c.Auth == nil {
		return nil
	}
	a := c.Auth
	if strings.TrimSpace(a.Request) == "" || a.Budget <= 0 || len(a.Candidates) == 0 {
		return fmt.Errorf("an authentication check must record the request spec, authorized budget and candidate refs")
	}
	seen, attempted, verified := map[string]bool{}, 0, 0
	for _, candidate := range a.Candidates {
		if candidate.ID == "" || seen[candidate.ID] {
			return fmt.Errorf("candidate refs must be non-empty and unique")
		}
		seen[candidate.ID] = true
		if candidate.Attempts < 0 {
			return fmt.Errorf("actual candidate request count cannot be negative")
		}
		attempted += candidate.requestAttempts()
		switch candidate.State {
		case "failed":
		case "verified":
			verified++
			if a.Identity == "" || a.IdentityEvidence == "" {
				return fmt.Errorf("a successful authentication must record the identity read after login and its evidence; HTTP 200 is not enough")
			}
		case "remaining", "skipped":
			if c.Result == "negative" {
				return fmt.Errorf("not all candidates have been executed, cannot save the authentication check as negative and complete")
			}
			if candidate.State == "skipped" && candidate.Reason == "" {
				return fmt.Errorf("a skipped candidate must record a reason")
			}
		default:
			return fmt.Errorf("invalid candidate status")
		}
	}
	if attempted > a.Budget {
		return fmt.Errorf("actual candidate attempts exceed the authorized budget")
	}
	if c.Result == "positive" && verified == 0 {
		return fmt.Errorf("a positive authentication result needs a verified candidate and an identity read")
	}
	return nil
}

// AddCheckIntent serializes admission with graph mutations. Renaming or creating
// a replacement intent cannot reset the attempts for the same target/mechanism.
func (s *ExplorationStore) AddCheckIntent(ctx context.Context, payload map[string]any, priority int, anchors []int64, origin string, check CheckSpec, retryOf int64, change string) (int64, error) {
	var id int64
	err := s.goalTransaction(ctx, func(tx *sql.Tx) error {
		if retryOf > 0 {
			var previous json.RawMessage
			var state string
			if err := tx.QueryRowContext(ctx, `SELECT payload,state FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, retryOf, s.expID).Scan(&previous, &state); err != nil {
				return err
			}
			var saved struct {
				Retired    bool        `json:"retired_contract"`
				Check      CheckSpec   `json:"check"`
				Checkpoint *Checkpoint `json:"checkpoint"`
			}
			if err := json.Unmarshal(previous, &saved); err != nil {
				return err
			}
			if saved.Retired {
				return fmt.Errorf("the original check belongs to a superseded contract; replan against the new scope")
			}
			if state != "blocked" && state != "exhausted" && state != "stopped" {
				return fmt.Errorf("only blocked or exhausted work can be resumed")
			}
			if saved.Check.Target == "" {
				return fmt.Errorf("the old work has no stable check identity; specify target/mechanism first")
			}
			if check.Target != "" && check.Key() != saved.Check.Key() {
				return fmt.Errorf("a resume must not change the original check identity")
			}
			check = saved.Check
			var latestID int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND payload->>'check_key'=$2 ORDER BY id DESC LIMIT 1`, s.expID, check.Key()).Scan(&latestID); err != nil {
				return err
			}
			if retryOf != latestID {
				return fmt.Errorf("a resume must reference the latest check attempt #%d so executed coverage is not lost", latestID)
			}
			if saved.Checkpoint != nil {
				payload["checkpoint"] = saved.Checkpoint
			}
		}
		if strings.TrimSpace(check.Target) == "" || strings.TrimSpace(check.Mechanism) == "" {
			return fmt.Errorf("check.target and check.mechanism are required")
		}
		var attempts, active int
		if err := tx.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE state IN ('open','running','paused')) FROM exploration_nodes WHERE exploration_id=$1 AND kind='intent' AND payload->>'check_key'=$2 AND COALESCE(payload->>'retired_contract','false')<>'true'`, s.expID, check.Key()).Scan(&attempts, &active); err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("this check already has unfinished work; continue the original work")
		}
		if attempts >= MaxCheckAttempts {
			return fmt.Errorf("this check has reached the limit of %d attempts; keep the gap and wait for the user or new conditions", MaxCheckAttempts)
		}
		if attempts > 0 && strings.TrimSpace(change) == "" {
			return fmt.Errorf("a resume must state recovery_change: what this attempt actually changes")
		}
		if attempts > 0 && retryOf == 0 {
			return fmt.Errorf("an original check already exists; a resume must provide retry_of and inherit the actual coverage checkpoint")
		}
		payload["check"], payload["check_key"], payload["check_attempt"] = check, check.Key(), attempts+1
		if retryOf > 0 {
			payload["retry_of"] = retryOf
		}
		if change != "" {
			payload["recovery_change"] = change
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'intent',$2,$3,'open',$4) RETURNING id`, s.expID, string(raw), priority, origin).Scan(&id); err != nil {
			return err
		}
		for _, a := range anchors {
			if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, a); err != nil {
				return err
			}
		}
		return nil
	})
	return id, err
}

// RecordCheckFact commits the result, its provenance and the resumable checkpoint
// together. A failed link/write leaves no orphan fact claiming a completed check.
func (s *ExplorationStore) RecordCheckFact(ctx context.Context, payload map[string]any, intentID int64, checkpoint Checkpoint, origin string, anchors []int64) (int64, error) {
	if err := checkpoint.Validate(); err != nil {
		return 0, err
	}
	var id int64
	err := s.goalTransaction(ctx, func(tx *sql.Tx) error {
		var prior json.RawMessage
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(payload->'checkpoint','null'::jsonb) FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent'`, intentID, s.expID).Scan(&prior); err != nil {
			return err
		}
		var old Checkpoint
		if len(prior) > 0 && json.Unmarshal(prior, &old) == nil && old.Auth != nil {
			if checkpoint.Auth == nil {
				return fmt.Errorf("an authentication checkpoint must keep the original candidates, budget and identity record")
			}
			current := map[string]AuthCandidate{}
			for _, c := range checkpoint.Auth.Candidates {
				current[c.ID] = c
			}
			if len(current) != len(old.Auth.Candidates) || checkpoint.Auth.Request != old.Auth.Request {
				return fmt.Errorf("a continuation cannot change the authorized candidate set or request spec")
			}
			for _, c := range old.Auth.Candidates {
				next, ok := current[c.ID]
				if !ok || next.requestAttempts() < c.requestAttempts() || ((c.State == "failed" || c.State == "verified") && next.State != c.State) {
					return fmt.Errorf("an authentication checkpoint cannot lose or reset executed candidates")
				}
			}
			if checkpoint.Auth.Budget != old.Auth.Budget {
				return fmt.Errorf("a continuation cannot change the original authorized attempt budget")
			}
		}
		payload["checkpoint"] = checkpoint
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if err = tx.QueryRowContext(ctx, `INSERT INTO exploration_nodes(exploration_id,kind,payload,priority,state,origin) VALUES($1,'fact',$2,5,'confirmed',$3) RETURNING id`, s.expID, string(raw), origin).Scan(&id); err != nil {
			return err
		}
		for _, a := range anchors {
			if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_anchors(node_id,asset_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, a); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO exploration_edges(exploration_id,src_id,rel,dst_id) VALUES($1,$2,'yields',$3) ON CONFLICT DO NOTHING`, s.expID, intentID, id); err != nil {
			return err
		}
		cp, _ := json.Marshal(checkpoint)
		_, err = tx.ExecContext(ctx, `UPDATE exploration_nodes SET payload=payload || jsonb_build_object('checkpoint',$3::jsonb) WHERE id=$1 AND exploration_id=$2`, intentID, s.expID, string(cp))
		return err
	})
	return id, err
}

// FinalizeIntentResult distinguishes the worker's exit from a completed check.
func (s *ExplorationStore) FinalizeIntentResult(id int64, state, kind, action string) error {
	if state != "done" && state != "blocked" && state != "exhausted" && state != "stopped" {
		return fmt.Errorf("invalid work exit status")
	}
	_, err := s.db.Exec(`WITH result AS (SELECT id,CASE WHEN $3='done' AND payload ? 'check_key' AND COALESCE(payload->'checkpoint'->>'result','') NOT IN ('positive','negative') THEN 'exhausted' ELSE $3 END AS state FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND state='running' FOR UPDATE) UPDATE exploration_nodes n SET state=r.state,completed_at=now(),content_version=content_version+1,payload=n.payload || jsonb_build_object('execution_end',jsonb_build_object('state',r.state,'requested_state',$3::text,'failure_kind',$4::text,'next_action',$5::text)) FROM result r WHERE n.id=r.id`, id, s.expID, state, kind, action)
	return err
}

// ProgressDigest ignores new intent IDs and repeated copies of the same evidence.
func (s *ExplorationStore) ProgressDigest(ctx context.Context) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT jsonb_build_object('kind',kind,'state',CASE WHEN kind='goal' THEN state ELSE '' END,'payload',CASE WHEN kind='goal' THEN payload WHEN kind='finding' THEN payload - 'finding_id' ELSE jsonb_build_object('evidence',payload->'evidence','detail',payload->'detail','checkpoint',payload->'checkpoint') END)::text FROM exploration_nodes WHERE exploration_id=$1 AND (kind IN ('goal','finding') OR (kind='fact' AND (payload ? 'evidence' OR payload ? 'checkpoint'))) ORDER BY 1`, s.expID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var items []string
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return "", err
		}
		items = append(items, raw)
	}
	if err = rows.Err(); err != nil {
		return "", err
	}
	sort.Strings(items)
	sum := sha256.Sum256([]byte(strings.Join(items, "\n")))
	return hex.EncodeToString(sum[:]), nil
}

func (d *DB) RecordNoProgressRound(ctx context.Context, taskID int64, before, after string, _ bool) (int, error) {
	var rounds int
	err := d.QueryRowContext(ctx, `INSERT INTO task_progress(task_id,fingerprint,stale_rounds) VALUES($1,$2,CASE WHEN $3 THEN 0 ELSE 1 END) ON CONFLICT(task_id) DO UPDATE SET fingerprint=$2,stale_rounds=CASE WHEN $3 OR task_progress.fingerprint<>$4 THEN 0 ELSE task_progress.stale_rounds+1 END RETURNING stale_rounds`, taskID, after, before != after, before).Scan(&rounds)
	return rounds, err
}

func (d *DB) NoProgressLimitReached(ctx context.Context, taskID int64, current string) (bool, error) {
	var reached bool
	err := d.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM task_progress WHERE task_id=$1 AND fingerprint=$2 AND stale_rounds>=3)`, taskID, current).Scan(&reached)
	return reached, err
}

func (s *ExplorationStore) HasGoalContract() (bool, error) {
	var typed bool
	err := s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM exploration_nodes WHERE exploration_id=$1 AND kind='goal' AND payload->>'goal_type' IN ('inspection','outcome'))`, s.expID).Scan(&typed)
	return typed, err
}
