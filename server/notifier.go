package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/notify"
)

// Global settings keys (stored in the settings key/value table; no table needed).
const (
	// settingNotifyEnabled is the master push switch. On by default: it is a one-click kill switch for maintenance,
	// not the feature's enabling condition; the real enabling condition is "whether any channel is configured".
	settingNotifyEnabled = "notify_enabled"
	// settingNotifyPublicBaseURL is the external URL used to build finding detail backlinks
	// (such as https://artex.example.com). When empty, messages carry no backlink button.
	// The project has no reusable external URL setting, so a new one is added here.
	settingNotifyPublicBaseURL = "notify_public_base_url"
	// settingNotifyDigestMinutes is the digest mode period (minutes).
	settingNotifyDigestMinutes = "notify_digest_interval_min"
)

const (
	// notifyTick is the delivery engine's polling interval. 3 seconds is the engine's latency ceiling
	// and the main source of delay between "finding persisted" and "message arrives in IM".
	notifyTick = 3 * time.Second
	// notifyLease is the lease duration when claiming deliveries. It must be well above the worst-case time of one delivery
	// (the notify package's HTTP client timeout is 15 seconds), or the same row could be delivered by two
	// dispatchers at once.
	notifyLease = 3 * time.Minute
	// notifyFanOutPerTick caps the number of events dispatched per round, so that enabling a channel for the first time
	// does not expand the entire historical backlog into delivery tasks in one go.
	notifyFanOutPerTick = 200
	// notifyDefaultDigestMinutes is the default digest period.
	notifyDefaultDigestMinutes = 30
	// notifyUnlimitedBurstPerTick is the per-round delivery cap for channels without a rate limit.
	// It exists to keep "one channel configured without a rate limit + one scan producing thousands of findings" from
	// dragging a single round into a long block.
	notifyUnlimitedBurstPerTick = 50
	// notifyMaxSendsPerChannelPerTick is the maximum number of deliveries per channel per round.
	//
	// This cap is derived from the **lease duration**: claimed rows get a lease (notifyLease = 3 minutes),
	// and if one round delivers so many items serially that the worst case exceeds the lease, the lease expires before the last ones are sent.
	// Within a single process this does not matter (Run is one goroutine running serially, and ticks do not re-enter), but when **two
	// processes share one database**, the other one would reclaim rows with expired leases and send them again, and also
	// double-increment attempts, marking them failed while the original process is still delivering.
	//
	// Value: 3-minute lease / 30-second per-send timeout = 6 uses **exactly the whole lease** with zero margin,
	// so it cannot be used; 5 leaves a worst case of 150 seconds with 30 seconds of margin. This relationship is
	// pinned by TestNotifyTickBudgetFitsWithinLease: changing any of notifyLease,
	// notifySendTimeout or this value makes that assertion fail.
	notifyMaxSendsPerChannelPerTick = 5
	// notifySendTimeout is the timeout for one delivery. It also determines the previous constant's value;
	// their product must not exceed notifyLease, see TestNotifyTickBudgetFitsWithinLease.
	notifySendTimeout = 30 * time.Second
)

// notifyBackoff is the retry backoff sequence, indexed by attempts so far.
// 3 chances (including the first) match db.MaxNotifyAttempts; the two must change together.
var notifyBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
}

// Notifier is the delivery engine for finding pushes.
//
// It runs alongside Scheduler as an independent goroutine (see server.New). It deliberately does not reuse
// Scheduler's tick: push latency requirements (3 seconds) differ from the triggers' business rhythm,
// and their failures stay independent: a stuck push should not affect agent triggers.
type Notifier struct {
	s  *Server
	pg *db.DB

	// mu protects buckets. There are few channels and little contention, so one mutex is enough,
	// and a finer-grained structure is not worth it.
	mu      sync.Mutex
	buckets map[int64]*notifyBucket
}

// notifyBucket is a per-channel token bucket.
//
// A token bucket is used instead of a "count per minute and reset" window because the latter has bad boundary effects:
// sending 20 at the end of a window and another 20 the next instant is 40 within a second to the platform,
// which gets rate limited; a token bucket refills at a constant rate and naturally avoids such bursts.
type notifyBucket struct {
	tokens   float64
	lastFill time.Time
}

func newNotifier(s *Server) *Notifier {
	return &Notifier{s: s, pg: s.m.pg, buckets: map[int64]*notifyBucket{}}
}

// Run loops until ctx is done. Started once by server.New.
func (n *Notifier) Run(ctx context.Context) {
	if n.pg == nil {
		return
	}
	t := time.NewTicker(notifyTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.step(ctx)
		}
	}
}

// step runs one round: dispatch new events first, then deliver due tasks.
//
// Any step that fails is only logged and does not break the loop: notification failures must never escalate into process-level problems.
// Each tick is independent, and the next round naturally retries.
func (n *Notifier) step(ctx context.Context) {
	if !n.enabled() {
		return
	}
	if _, _, err := n.pg.FanOutPendingEvents(ctx, notifyFanOutPerTick); err != nil {
		log.Printf("[notify] failed to dispatch events: %v", err)
		return
	}
	channels, err := n.pg.ListNotificationChannels(ctx)
	if err != nil {
		log.Printf("[notify] failed to read channels: %v", err)
		return
	}
	baseURL := n.publicBaseURL()
	for _, ch := range channels {
		if !ch.IsEnabled() {
			continue
		}
		// The token bucket's unit is **messages** (equivalent to HTTP requests), not findings.
		// In realtime mode the two are the same (one message per finding); in digest mode a whole batch of findings is combined into
		// one message, so only one token is consumed.
		//
		// Both modes ask the token bucket first and then claim by quota; the order cannot be reversed, or deliveries blocked by rate limiting
		// would already have used up a retry attempt.
		now := time.Now()
		if ch.Mode == db.NotifyModeDigest {
			tokens, claimLimit := digestTickPlan()
			if n.takeTokens(ch.ID, ch.RatePerMin, tokens, now) <= 0 {
				continue
			}
			n.stepDigest(ctx, ch, claimLimit, baseURL)
			continue
		}
		allow := n.takeTokens(ch.ID, ch.RatePerMin, notifyMaxSendsPerChannelPerTick, now)
		if allow <= 0 {
			continue
		}
		n.stepRealtime(ctx, ch, allow, baseURL)
	}
}

// digestTickPlan returns a digest channel's token consumption and batch size upper bound for this round.
//
// The two return values have **two different units**, which is exactly why this is its own function:
//
//   - tokens is the message count. A batch of findings becomes one message and one HTTP request, so it is always 1.
//     rate_per_min therefore still applies to digests (at most this many digest messages per minute).
//   - claimLimit is the maximum number of findings in this batch. It is bounded only by memory, unrelated to the request budget.
//
// To make rate_per_min apply to digests, the per-round request budget
// (notifyMaxSendsPerChannelPerTick, derived from the lease) was once passed straight down as the batch size.
// As a result a channel with rate_per_min=20 only refilled 1 token per 3-second tick, so every digest
// message held only 1 finding: digest degraded into "realtime pushes with digest wording", and readers got a stream of
// "1 new findings in the last 30 minutes", while db.MaxDigestBatchSize was never reachable.
//
// This symptom is hard to spot in end-to-end tests (existing cases all pass a large enough limit to
// stepDigest manually, bypassing the quota computation in step), so the decision is collected here and pinned directly by
// TestDigestTickPlanDecouplesBatchSizeFromSendBudget.
func digestTickPlan() (tokens, claimLimit int) {
	return 1, db.MaxDigestBatchSize
}

// stepRealtime claims and delivers a channel's realtime tasks, one message per finding.
func (n *Notifier) stepRealtime(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	deliveries, err := n.pg.ClaimRealtimeDeliveries(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] failed to claim realtime deliveries channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("channel kind %q is not registered", ch.Kind))
		return
	}
	for _, dl := range deliveries {
		msg, err := n.renderSingle(ctx, dl, baseURL)
		if err != nil {
			// A render failure is a local data problem; retrying will not help.
			_ = n.pg.FailDeliveries(ctx, []int64{dl.ID}, err.Error())
			continue
		}
		n.send(ctx, channel, cfg, msg, []*db.NotificationDelivery{dl})
	}
}

// stepDigest aggregates a channel's pending deliveries into one message and sends it when the batch is due.
func (n *Notifier) stepDigest(ctx context.Context, ch *db.NotificationChannel, allow int, baseURL string) {
	window := n.digestInterval()
	due, err := n.pg.DigestBatchDue(ctx, ch.ID, window)
	if err != nil {
		log.Printf("[notify] failed to check digest batch channel=%d: %v", ch.ID, err)
		return
	}
	if !due {
		return
	}
	deliveries, err := n.pg.ClaimDigestBatch(ctx, ch.ID, allow, notifyLease)
	if err != nil {
		log.Printf("[notify] failed to claim digest batch channel=%d: %v", ch.ID, err)
		return
	}
	if len(deliveries) == 0 {
		return
	}
	channel, cfg, ok := n.adapt(ch)
	if !ok {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), fmt.Sprintf("channel kind %q is not registered", ch.Kind))
		return
	}
	msg, included, err := n.renderBatch(ctx, deliveries, baseURL, int(window.Minutes()))
	if err != nil {
		_ = n.pg.FailDeliveries(ctx, deliveryIDs(deliveries), err.Error())
		return
	}
	// Deliveries whose snapshot is broken and did not make it into the message must be explicitly failed. Otherwise they would stay
	// outside included, in neither the message nor the failure list; when the send succeeds their status would be
	// missed by the subsequent bulk marking, stuck in sending forever until the lease expires and they are claimed again and again.
	if skipped := excludeDeliveries(deliveries, included); len(skipped) > 0 {
		reason := "event snapshot cannot be parsed; this finding cannot be rendered into a message"
		if fErr := n.pg.FailDeliveries(ctx, deliveryIDs(skipped), reason); fErr != nil {
			log.Printf("[notify] failed to mark bad-snapshot deliveries failed channel=%s ids=%v: %v", ch.Kind, deliveryIDs(skipped), fErr)
		}
		log.Printf("[notify] skipped %d deliveries with unparseable snapshots channel=%d", len(skipped), ch.ID)
	}
	// Only the ones that made it into the message go to send: included[i] corresponds strictly to msg.Items[i],
	// and send relies on this correspondence to apply "the channel reported the first K fit" to the right delivery rows.
	n.send(ctx, channel, cfg, msg, included)
}

// send delivers and transitions states based on the result.
//
// One batch of deliveries (possibly dozens in digest mode) shares one send result: either delivered, or the whole batch retries.
// There is no per-item retry: a digest is one message, and resending part of it would scramble batch semantics.
//
// The only exception is **splitting caused by the channel length limit**: if the channel reports that only the first K fit,
// items from K+1 on must wait for the next batch instead of being marked successful along with the rest. Otherwise the truncated
// findings would be in neither the message nor the failure list, gone entirely.
func (n *Notifier) send(ctx context.Context, channel notify.Channel, cfg map[string]any, msg notify.Message, deliveries []*db.NotificationDelivery) {
	// Cap each delivery so one stuck channel cannot hold up all remaining channels in this round.
	sendCtx, cancel := context.WithTimeout(ctx, notifySendTimeout)
	defer cancel()
	delivered, err := channel.Send(sendCtx, cfg, msg)
	if err == nil && delivered > 0 {
		if delivered > len(deliveries) {
			// The channel cannot report more items than there are deliveries; if it happens, the rendering layer miscounted.
			// Treating it as all delivered and logging the problem beats corrupting the records.
			log.Printf("[notify] channel reported %d delivered, more than %d deliveries channel=%s; treating all as delivered",
				delivered, len(deliveries), channel.Kind())
			delivered = len(deliveries)
		}
		sent, rest := deliveries[:delivered], deliveries[delivered:]
		if err := n.pg.MarkDeliveriesSent(ctx, deliveryIDs(sent)); err != nil {
			log.Printf("[notify] failed to mark delivered channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(sent), err)
		}
		if len(rest) > 0 {
			// This message hit the channel length limit: requeue the rest immediately for the next tick to continue.
			// Use DeferDeliveries rather than RescheduleDeliveries: this is not a failure
			// and should not consume the retry budget (claiming already optimistically added 1; it is subtracted back there).
			if err := n.pg.DeferDeliveries(ctx, deliveryIDs(rest),
				fmt.Sprintf("this message hit the channel length limit; only the first %d were delivered, the rest wait for the next batch", delivered)); err != nil {
				log.Printf("[notify] failed to requeue continuation channel=%s ids=%v: %v", channel.Kind(), deliveryIDs(rest), err)
			}
		}
		return
	}
	if err == nil {
		// The channel neither reported an error nor said how many items were delivered. Treat it as a failure (with backoff),
		// so this delivery is not claimed over and over without ever being marked.
		err = fmt.Errorf("channel did not report a delivered count (delivered=%d)", delivered)
	}

	// Failure handling is decided **per item**, not from the batch's maximum attempt count.
	//
	// It used to be `if maxAttempts(deliveries) >= MaxNotifyAttempts`, condemning the whole batch, but attempts differ
	// among items in a batch: an old delivery already retried twice (attempts=2) would drag brand-new deliveries
	// (attempts=1) in the same batch into failed; new findings were lost for good without a single retry,
	// the exact opposite of the intent "do not let old rows drag new rows down".
	permanent := notify.IsPermanent(err)
	var failIDs, exhaustedIDs []int64
	byDelay := map[time.Duration][]int64{}
	for _, dl := range deliveries {
		switch {
		case permanent:
			failIDs = append(failIDs, dl.ID)
		case dl.Attempts >= db.MaxNotifyAttempts:
			exhaustedIDs = append(exhaustedIDs, dl.ID)
		default:
			delay := notifyBackoff[min(dl.Attempts, len(notifyBackoff)-1)]
			byDelay[delay] = append(byDelay[delay], dl.ID)
		}
	}

	if len(failIDs) > 0 {
		if fErr := n.pg.FailDeliveries(ctx, failIDs, err.Error()); fErr != nil {
			log.Printf("[notify] failed to mark failure status channel=%s ids=%v: %v", channel.Kind(), failIDs, fErr)
		}
	}
	if len(exhaustedIDs) > 0 {
		reason := fmt.Sprintf("still failing after %d attempts: %s", db.MaxNotifyAttempts, err)
		if fErr := n.pg.FailDeliveries(ctx, exhaustedIDs, reason); fErr != nil {
			log.Printf("[notify] failed to mark failure status channel=%s ids=%v: %v", channel.Kind(), exhaustedIDs, fErr)
		}
	}
	// Reschedule grouped by delay: there are only 3 backoff levels, so the number of groups is naturally tiny and there is no need for one
	// UPDATE per item (which would make a 500-item batch take 500 round trips).
	for delay, group := range byDelay {
		if rErr := n.pg.RescheduleDeliveries(ctx, group, delay, err.Error()); rErr != nil {
			log.Printf("[notify] failed to reschedule deliveries channel=%s ids=%v: %v", channel.Kind(), group, rErr)
		}
	}
	if len(failIDs)+len(exhaustedIDs) > 0 {
		log.Printf("[notify] delivery failed channel=%d kind=%s permanent=%d exhausted=%d retrying=%d: %s",
			deliveries[0].ChannelID, channel.Kind(), len(failIDs), len(exhaustedIDs), len(byDelay), err)
	}
}

// excludeDeliveries returns those in all that are not in keep (compared by pointer identity).
// Used to find deliveries that "did not make it into the message"; they must be handled explicitly, not left in a gray zone.
func excludeDeliveries(all, keep []*db.NotificationDelivery) []*db.NotificationDelivery {
	inKeep := make(map[*db.NotificationDelivery]bool, len(keep))
	for _, dl := range keep {
		inKeep[dl] = true
	}
	var out []*db.NotificationDelivery
	for _, dl := range all {
		if !inKeep[dl] {
			out = append(out, dl)
		}
	}
	return out
}

// adapt gets the channel implementation and parses its config.
// ok=false means the kind is not registered, and delivery should fail outright rather than retry forever.
func (n *Notifier) adapt(ch *db.NotificationChannel) (notify.Channel, map[string]any, bool) {
	channel, ok := notify.Get(ch.Kind)
	if !ok {
		return nil, nil, false
	}
	var cfg map[string]any
	if len(ch.Config) > 0 {
		// On config parse failure use an empty map: the channel's own Validate reports which field is missing,
		// an error that guides users to a fix better than a JSON parse error.
		_ = json.Unmarshal(ch.Config, &cfg)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	return channel, cfg, true
}

// renderSingle renders a single finding message.
func (n *Notifier) renderSingle(ctx context.Context, dl *db.NotificationDelivery, baseURL string) (notify.Message, error) {
	snap, err := parseSnapshot(dl)
	if err != nil {
		return notify.Message{}, err
	}
	item, err := n.itemFor(ctx, snap, baseURL)
	if err != nil {
		return notify.Message{}, err
	}
	return notify.Message{Items: []notify.Item{item}, HomeURL: baseURL}, nil
}

// renderBatch renders a digest message. Snapshots are parsed one by one; a single bad one skips only that item
// and does not take the whole digest down with it.
//
// The returned included corresponds **strictly one to one** with msg.Items (i-th delivery ↔ i-th item).
// This correspondence is a hard requirement: the caller marks the first K deliveries delivered based on "the channel reported the first K fit".
// If a bad snapshot were skipped here without removing the skipped delivery from included,
// the indexes would shift: a bad item that should fail would be marked delivered, and good items misjudged as undelivered.
// The bad ones are explicitly marked failed by the caller, see stepDigest.
func (n *Notifier) renderBatch(ctx context.Context, deliveries []*db.NotificationDelivery, baseURL string, windowMinutes int) (notify.Message, []*db.NotificationDelivery, error) {
	items := make([]notify.Item, 0, len(deliveries))
	included := make([]*db.NotificationDelivery, 0, len(deliveries))
	for _, dl := range deliveries {
		snap, err := parseSnapshot(dl)
		if err != nil {
			// A bad snapshot goes into neither the message nor included; the caller is responsible for handling it
			// (explicitly marking it failed instead of slipping it through as "delivered").
			log.Printf("[notify] skipping unparseable snapshot in digest batch delivery=%d: %v", dl.ID, err)
			continue
		}
		item, err := n.itemFor(ctx, snap, baseURL)
		if err != nil {
			return notify.Message{}, nil, err
		}
		items = append(items, item)
		included = append(included, dl)
	}
	if len(items) == 0 {
		return notify.Message{}, nil, fmt.Errorf("none of the %d deliveries in the digest batch could be parsed", len(deliveries))
	}
	return notify.Message{
		Items:         items,
		Batch:         true,
		WindowMinutes: windowMinutes,
		HomeURL:       baseURL,
	}, included, nil
}

// itemFor renders an event snapshot into an item to push, resolving asset names and the detail backlink along the way.
func (n *Notifier) itemFor(ctx context.Context, snap notify.Snapshot, baseURL string) (notify.Item, error) {
	assets, err := n.pg.NotificationAssetNames(ctx, snap.AssetIDs)
	if err != nil {
		// Failing to resolve asset names should not block the push: a missing name is far less serious than a missing notification;
		// the message just lacks an asset line.
		log.Printf("[notify] failed to resolve asset names finding=%d: %v", snap.FindingID, err)
	}
	item := notify.Item{
		FindingID:  snap.FindingID,
		Name:       snap.Name,
		VulnClass:  snap.VulnClass,
		Severity:   snap.Severity,
		Summary:    snap.Summary,
		Assets:     assets,
		FromStatus: snap.FromStatus,
		ToStatus:   snap.ToStatus,
	}
	if baseURL != "" {
		// The detail page route is web/src/app/(main)/function/findings/detail/page.tsx,
		// which reads the finding id from the id query parameter.
		item.DetailURL = fmt.Sprintf("%s/function/findings/detail?id=%d", baseURL, snap.FindingID)
	}
	return item, nil
}

// takeTokens takes **at most want** tokens from the channel's token bucket and returns how many it actually got.
//
// One token = one message (one HTTP request). In realtime mode the caller passes as many as it wants;
// in digest mode a whole batch of findings is sent as one message, so it passes 1.
//
// The bucket capacity is the channel's per-minute limit, refilled at a constant rate. ratePerMin<=0 means no rate limit,
// and returns a finite but large enough value so a single round is not held up by an unbounded backlog.
//
// The want cap is required: without it the bucket could only be drained completely, while the caller has its own per-round cap,
// so the extra tokens would go unused and vanish before the next refill; the accumulated burst capacity would never be reachable,
// and even "no pending deliveries this round" would still be charged.
func (n *Notifier) takeTokens(channelID int64, ratePerMin, want int, now time.Time) int {
	if want <= 0 {
		return 0
	}
	if ratePerMin <= 0 {
		return min(want, notifyUnlimitedBurstPerTick)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	b := n.buckets[channelID]
	if b == nil {
		b = &notifyBucket{tokens: float64(ratePerMin), lastFill: now}
		n.buckets[channelID] = b
	}
	// Refill by real elapsed time, at ratePerMin/60 per second.
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = minF(float64(ratePerMin), b.tokens+elapsed*float64(ratePerMin)/60)
		b.lastFill = now
	}
	// Add a tiny epsilon before rounding: the token count is accumulated in floating point, and refilling in two halves
	// 0.5 + 0.5 may give 0.9999999999, which a direct int() would truncate to 0:
	// a mathematically full bucket that yields no token. 1e-9 is far less than one token and will not let a real deficit through.
	take := min(int(b.tokens+1e-9), want)
	if take <= 0 {
		return 0
	}
	b.tokens -= float64(take)
	return take
}

// enabled reads the master switch.
func (n *Notifier) enabled() bool {
	return n.pg.GetBool(settingNotifyEnabled, true)
}

// publicBaseURL returns the external URL for backlinks, without a trailing slash.
func (n *Notifier) publicBaseURL() string {
	v, ok, err := n.pg.GetSetting(settingNotifyPublicBaseURL)
	if err != nil || !ok {
		return ""
	}
	return trimTrailingSlash(v)
}

// digestInterval returns the digest period, falling back to the default when invalid or unset.
func (n *Notifier) digestInterval() time.Duration {
	v, ok, err := n.pg.GetSetting(settingNotifyDigestMinutes)
	if err != nil || !ok {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	m := 0
	if _, err := fmt.Sscanf(v, "%d", &m); err != nil || m <= 0 {
		return time.Duration(notifyDefaultDigestMinutes) * time.Minute
	}
	return time.Duration(m) * time.Minute
}

// parseSnapshot parses the snapshot of a delivery's event.
func parseSnapshot(dl *db.NotificationDelivery) (notify.Snapshot, error) {
	var snap notify.Snapshot
	if len(dl.Snapshot) == 0 {
		return snap, fmt.Errorf("event snapshot for delivery %d is empty", dl.ID)
	}
	if err := json.Unmarshal(dl.Snapshot, &snap); err != nil {
		return snap, fmt.Errorf("failed to parse event snapshot for delivery %d: %w", dl.ID, err)
	}
	if snap.Kind == "" {
		// The event kind follows the event row; the copy in the snapshot may have been written by an older version.
		snap.Kind = dl.EventKind
	}
	return snap, nil
}

func deliveryIDs(deliveries []*db.NotificationDelivery) []int64 {
	out := make([]int64, 0, len(deliveries))
	for _, dl := range deliveries {
		out = append(out, dl.ID)
	}
	return out
}

func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
