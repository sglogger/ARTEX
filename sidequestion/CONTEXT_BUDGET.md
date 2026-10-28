# Long side-question conversations and the context budget

Fixed on 2026-09-11. The original implementation treated the character count of the request JSON directly as tokens and inherited the main task's 32K output reservation, so ordinary side questions were rejected early when there was a lot of HTML, JS or tool results.

## Review of open-source implementations

- [Grok CLI side-question context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): takes excerpts from the most recent user and assistant text, with a character budget of about 2000 and at most 400 characters per entry. This path does not keep a continuous side-question history.
- [Grok CLI independent request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): independent cancellation signal; an output limit of 2048 tokens when the model supports it; no tools provided.
- [Grok CLI main conversation compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): estimates tokens, keeps recent content, folds new content into the old summary and handles cross-turn truncation.
- [OpenCode conversation compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): full-request estimation, output/buffer reservation, recent content plus a rolling summary, tool-free summary requests; by default it uses a recent budget of 8000 and a summary output limit of 4096 tokens.
- [OpenCode overflow recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): overflow recovery is only attempted before assistant output has started, and the recovered call does not enter the same overflow recovery path again.

ARTEX adopts the independent output budget, recent content plus rolling summary, and bounded recovery. It keeps norma v0.3.6's structured messages and tool pairing, does not copy Grok's text excerpts, and does not write OpenCode-style main conversation compaction events into the ARTEX main transcript.

## Request budget and execution

- Messages reuse norma's per-content-block UTF-8 byte estimate with a 4/3 margin; system prompts, tool schemas and message envelope overhead are counted on top. The estimate is not an exact model token count.
- Side-question output defaults to at most 8192 tokens and never exceeds the output limit already set in the main config. The service environment variable `ARTEX_BTW_MAX_OUTPUT_TOKENS` can set a limit between 256 and 32768; it does not change the product's default model or the main task's parameters.
- The input budget is the context window minus the output limit and a safety margin; an unknown window uses the platform default of 200K. The safety margin is 5% of the window, at least 128 and at most 8192 tokens.
- Successful Q&A pairs are loaded by increasing ordinal, at most 20 per batch. At most 20 pairs are kept verbatim, with a token budget of at most 1/4 of the input budget and no more than 16K.
- Q&A pairs over the limit are folded into the rolling summary. The summary records history source and context time; past assistant answers are not new tool evidence, and the latest main snapshot wins on conflict.
- When the main context is still too long, only the old messages of the copy are summarized, keeping at most 8K tokens of recent content; the cut point never separates a tool call from its result. An oversized single pair goes into the summary whole.
- Summary input is split into UTF-8-safe chunks according to the actual remaining window, with an output limit of 2048 tokens; empty summaries, truncation, tool calls or exceeding the summary budget are never cached. A single side question makes at most 12 summary calls under the same 120-second timeout; reaching the limit fails explicitly rather than looping forever.
- If the model first reports a context overflow before any text or tool call has been output, the context is reduced further and retried at most once; if the estimated size does not shrink, recovery stops immediately. Other model errors and partial streaming output do not trigger this recovery.
- All usage obtained, including summaries, failed attempts and usage at cancellation, is added to the same side-question request. When a Provider returns no usage only zero can be recorded; estimates are never passed off as actual usage.

## Persistence and UI

`side_question_sessions.memory` stores the summary of old Q&A, the ordinals it covers and main-context summaries cached per snapshot identity. `side_question_requests.context_info` stores the preparation phase, the actual number of replayed pairs, summary usage and the budget estimate.

A summary is saved only while the original request is still running and the clear version matches; clear also clears the cache, and late writes never restore cleared data. A new snapshot does not reuse summaries of an old snapshot. Summary fields are saved with v3 task archives; restoring an old v3 archive missing these fields fills in empty objects, and v1/v2 remain compatible.

POST accepts and returns the request first; preparation and compaction run in the background without holding the admission lock or a database transaction. SSE/history show the preparing, consolidating Q&A, compacting copy and answering phases; a compaction failure is saved as that side-question request's failed terminal state. The frontend keeps the error and restores the draft of the failed question instead of covering the input box with a floating notification; history polling no longer clears submission errors.

## Validation record

- Replay of 19/20/21/50 pairs, keeping old conclusions across 20 pairs, reusing the summary cache after restart: automated, passing.
- Very long CJK answers and code context, chunked request budget, tool pairing, unchanged snapshots, cache invalidation on new snapshots: automated, passing.
- Summary failure/cancel/truncation/oversize/tool return, clear races, the call limit, only one overflow recovery and no retry on partial streams: automated, passing.
- Against an isolated PostgreSQL: pagination, restart, v1/v2/v3 archives, archive restore of summary cache and budget metadata, old v3 missing new fields, 20 parent conversations sharing four concurrency slots: passing.
- Go candidate service build, frontend TypeScript check, Biome check of the modified components, and a Next.js production build in a separate directory: passing.
- The built-in browser used an isolated UI fixture at 1280×720 and 390×844 to verify the consolidation phase, the summary scope hint, draft restore after failure, no floating error notification, no horizontal overflow and no console errors. The temporary fixture has been removed.
- Read-only replay of existing local Worker snapshots passed the new budget check; for example Worker #3's 293085-character snapshot is no longer wrongly rejected by local character counting. This item made no external model calls.
- The real conversation test that would have sent a private Worker snapshot to Grok was rejected by the automatic approval review and did not run, so it is not counted as passing.
- On 2026-09-11 00:37 the local backend was restarted at the user's request, keeping the original database, data directory and login config. The running file matches the candidate binary's SHA-256; `/api/health` returns OK both on the backend and through the frontend proxy.

Validation commands (isolated test database only):

```sh
go run ./tests/run.go -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

The frontend production build used a separate copy to avoid overwriting the `.next` of the current preview. The candidate service is at `/private/tmp/artex-btw-budget-candidate`, copied to `/private/tmp/artex-btw-preview/artex` and started; the original binary is backed up in the same directory as `artex.before-context-budget`.
