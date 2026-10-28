package agent

import (
	"log"
	"path/filepath"

	"github.com/Autumn-27/norma/agentcore"
	"github.com/Autumn-27/norma/noaadapter"
)

// noaWarn returns a diagnostics sink tagging non-fatal noa messages with the
// session, routed through the package logger (agents have no per-instance one).
func noaWarn(session string) func(string) {
	return func(msg string) { log.Printf("[noa] %s: %s", session, msg) }
}

// noa is the "model-driven context compaction" mechanism introduced in norma v0.4.0, offered as an experimental platform feature that users toggle in
// system settings. It is mutually exclusive with the built-in compaction: noaadapter.Enable is the only entry point and attaches in one go
// the context takeover (Compactor), the Compress tool and the three resident prompts; not calling Enable means it is off
// (built-in compaction works as usual). The switch is resolved by the noaEnabledFn injected into each agent, read once per run,
// so toggling only affects runs started afterwards, with no need to rebuild agents.

// enableNoa attaches noa to opts when the resolver reports it enabled. archiveRoot is the persistent base directory for the compacted originals
// (the global workDir; all agents land under <workDir>/noa rather than scattered across task/intent directories), and sessionID
// names the archive subdirectory under it (globally unique, so no collisions within the same base directory).
//
// noa is experimental: a failure to attach must not interrupt real tasks. On error it is reported via onWarn and falls back to built-in compaction.
// On success opts.Compaction is cleared, so agentcore doesn't warn about "two context managers set at the same time".
func enableNoa(opts *agentcore.Options, enabled func() bool, archiveRoot, sessionID string, onWarn func(string)) {
	if enabled == nil || !enabled() {
		return
	}
	if opts.OnWarn == nil {
		opts.OnWarn = onWarn
	}
	if err := noaadapter.Enable(opts, noaadapter.Options{
		ArchiveBaseDir: filepath.Join(archiveRoot, "noa"),
		SessionID:      sessionID,
		OnWarn:         onWarn,
	}); err != nil {
		if onWarn != nil {
			onWarn("failed to enable noa compaction, falling back to built-in compaction: " + err.Error())
		}
		return
	}
	// Compactor overrides Compaction, but agentcore warns every time both are set; clear it explicitly.
	opts.Compaction = nil
}
