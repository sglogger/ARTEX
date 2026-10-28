package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the hard limit (bytes, not characters) on WeCom group robot markdown content.
// It is the tightest limit of all six channels and the main reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements the WeCom group robot.
//
// Platform traits:
//   - Authenticates only via the key in the URL and does not support signing, so the webhook URL itself is the entire credential.
//   - markdown content is capped at 4096 **bytes**, and anything longer is rejected outright (not truncated). At 3 bytes per CJK character,
//     that leaves only a thousand-odd characters for the body, so the client must truncate.
//   - Rate limited to 20 messages/minute, also handled by client-side rate limiting.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom has only one credential, the webhook (the key in the URL), and it does not support signing:
// the whole URL is the entire credential, and no other field needs masking.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom has only the webhook field, which is both destination and credential, so there is no "credential left over after changing the destination".
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("missing webhook URL")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("invalid webhook URL: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// Digest batches can be long (50 items × one line each + prefix), and 4096 bytes is easily exceeded.
	// Truncation happens here instead of relying on a platform error: a rejection loses the whole batch, while truncation delivers at least the first few items.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("failed to parse WeCom response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 means the API call limit was exceeded; the platform's rate limit window rolls, so retrying after backoff works,
		// hence it is explicitly retryable. Reaching here means the client's rate_per_min is set too aggressively;
		// retrying is only a backstop, and the real fix is to lower the channel's rate limit.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom rate limited %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means the webhook key is invalid: a permanent failure that retries will not fix.
		return 0, Permanent(fmt.Errorf("WeCom returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
