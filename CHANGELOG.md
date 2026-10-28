# Changelog

Notable changes to this project are recorded in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.4.3] - 2026-10-27

### Fixed

- Fixed lost corrections when Anthropic-compatible streaming responses report input and cache usage late in the final frame. Usage is now metered from each call's cumulative snapshot, so repeated frames are not counted twice, and input reported before a dropped stream is kept. The dashboard cache ratio keeps two decimal places, shows tiny non-zero values as `<0.01%`, and notes that unreported cache does not mean a miss. The fix only affects later calls and does not backfill historical usage.
- Fixed duplicate reporter agent triggers caused by reused tool call IDs in multi-asset tasks ([#11](https://github.com/sharenjun/ARTEX-main/issues/11)). A tool result is now linked only to the most recent earlier call within the same worker, work node, and main conversation segment, so one completion event no longer creates several trigger records or mixes in another call's arguments. Normal independent report events are still handled according to the agent configuration.

### Verification and upgrade

- Added regressions for streaming usage, database persistence, and tool triggers. Local targeted tests, isolated PostgreSQL integration, frontend tests, type checks, and the 32-page static build pass. Race detection runs in GitHub Actions.
- The cache fix applies to calls made after upgrading; when a provider returns no cache fields, a real hit still cannot be determined. Historical usage is not backfilled and existing report conversations are not cleaned up; no new database migration is needed.
- The Issue #11 screenshots are not enough to confirm the on-site cause; the duplicate triggers on reused tool call IDs were reproduced and fixed, and normal independent report events continue to run according to the agent configuration.

## [0.4.2] - 2026-10-13

This release fixes task wrap-up, failure recovery, authentication coverage, evidence handoff, and scope sync issues from P0/P1, and improves tool statistics and wrap-up saving.

### Fixed

- New goals are fixed to the inspection or outcome type; inspection goals must save their required checks. Authentication checkpoints keep the actual requests, remaining candidates, authorization budget, and identity verification material; resumed work inherits the latest record and limits repeated attempts.
- Work exit and check completion are saved separately; material with gaps, inferences, or empty evidence cannot directly prove a goal achieved. After adding evidence or changing the scope, old proofs must be rechecked.
- Workers, the planner, and the reporter can read and add to existing finding evidence, bind traffic, and update reports within the current task's permissions; evidence edits check versions and keep history, and the UI shows the review status.
- While paused, the goal, description, required checks, constraints, and asset scope can be edited together; the old contract is kept, old unfinished work is stopped, and planning restarts on resume. A manual resume resets the stall counter; normal scheduling does not.
- The goal decomposition tool is hooked into the unified call ledger, recording role, task, and execution result; the tools page adds call result statistics.
- Model quota, refusal, configuration, protocol, and transport errors are distinguished, and deterministic errors keep a pending state; the missing wrap-up asset saving tool was added, and the normal wrap-up save window was extended to 60 seconds.

API requests that add a goal must include `goal_type` (`inspection` or `outcome`); `inspection` also requires a non-empty `required_checks`. Existing goals keep their original records, and old goals without a type can get an explicit contract by editing the task scope.

### Verification scope

Local regressions, targeted repeated tests, race checks, and the frontend build pass. The full effect on tasks with many assets and many findings still needs wider verification; P1-05's integration with specific model services and external systems remains pending verification, and skipped or unreproduced cases are not counted as passes.

## [4.0.1] - 2026-10-12

This release focuses on thinking history replay, goal misjudgments, and task wrap-up: real thinking blocks and executed tool results are kept, achieving a goal requires matching evidence, and final verification failures are traceable, correctable, and recovered within bounds.

### Fixed

- Recognize the raw HTTP 400 wording from gateways that require `content[].thinking` to be sent back, and enter bounded history recovery while keeping executed tool results; configuration errors such as forcing thinking on still keep the original error.
- Add an independent supporting review for positive goal-achieved verdicts; when the boolean verdict and the reasoning disagree, when the actual outcome belongs to another goal, or when the review fails, the achieved state is no longer written. Both reviews share the existing 60-second budget.
- The wrap-up final verification rejects completion as soon as it confirms a decisive gap and saves the goal gap, instead of exhausting the budget by walking every proof for a negative verdict; positive verdicts must still verify every required goal and check.
- Evidence is reviewed independently before a goal is marked achieved, distinguishing actual outcomes from completed checks; proof edges, status, and review reasons are saved in one transaction, and the task, goal, and evidence versions are rechecked.
- Incorrect goal completion verdicts can be corrected, keeping the original proof and reason; editing goal content revokes the old verdict. Final verification returns specific goal evidence gaps, and the task panel and report show recovery information.
- Wrap-up requests check every goal, so the goal list display limit no longer hides required work; negative facts cannot prove that a flag was obtained or a vulnerability confirmed.
- The default task final verification budget is now 180 seconds and 16 rounds, and is configurable; the review deadline and the worker drain deadline are shown separately, keeping the absolute deadline and protection against late verdicts.
- Final verification failures for the same input persist their category, count, and recovery action; transient failures are retried automatically at most twice with backoff; restarts or new planner requests do not reset the limit, and manual recovery records a reason.
- Unified the Anthropic thinking configuration and per-request overrides: the default mode keeps real thinking history, filtering only happens when explicitly disabled, and invalid modes report a clear error.
- Added raw block recovery, tool linkage checks, and bounded conversation recovery for missing thinking history; old transcripts and completed tool results are kept, the recovery boundary is recorded, and recovery continues after a restart.
- The model pool recognizes deterministic history and configuration errors, avoiding pointless failover and wrongly cleared health counters.
- Keep block boundaries, chunked signatures, empty blocks, and opaque content of streamed thinking; NOA no longer rewrites signed thinking text. The minimal model origin is saved, and context is restored from the record when switching to an incompatible configuration.
- A dropped stream does not execute tool calls that were not fully received, and persists the results of executed tools and the partial response, so continued conversations do not miss side effects; responses missing message_stop report a clear error.

### Code organization

- Split the HTTP API, routes, model wiring, prompt templates, and skill file operations in `server.go` and `server_mgmt.go` by responsibility, keeping the same `server` package and existing interfaces.
- The follow-up split keeps v0.4.0's background sync lock and startup recovery, the four sync history/job routes, the task wrap-up DTO/report, and the Windows skill path fix, without changing the database schema or configuration format.

### Verification and upgrade

- GitHub CI passes for the four batches of feature fixes and the follow-up fixes; locally, the build, static checks, Agent/DB/Server race regressions on isolated PostgreSQL, and Norma thinking, conversation, and transcript regressions are complete. Live cases that lack an external environment are not counted as passes.
- Real DeepSeek was verified for streaming and non-streaming multi-turn tool calls, cold history loading, context recovery, distinguishing outcomes from negative checks, and structured goal gaps and corrections. The missing-thinking HTTP 400 recorded in history was regression-tested with its original text; the current gateway returns 200 for the minimal request, so this round does not claim to have reproduced that 400 remotely.
- Upgrading keeps existing configuration and data; startup runs an idempotent database migration that adds goal review and final verification retry information. Achievement verdicts of historical tasks are not corrected automatically; historical mislabels need an explicit correction in the goal panel.
- The `claude-fable-5.1` gateway entry was verified for two-turn streaming/non-streaming tool calls with signed thinking, cold loading, NOA projection, and explicitly disabled thinking, all as expected. That entry labels its responses `claude-opus-5`, and requests with tampered or removed signatures still return 200; so this only confirms that this program preserves fidelity and that the gateway is replay-compatible, not that strict official signature validation passes. Actual responses did not contain `redacted_thinking`, so its fidelity is still evidenced only by regression tests.
- Release packages include the static frontend, with five platform packages for Linux/macOS amd64, arm64 and Windows amd64, plus a SHA256 checksum file.

## [0.4.0] - 2026-10-11

A feature release focused on asset sync, task completion checks, and worker wrap-up. For details see the [v0.4.0 Release](https://github.com/sharenjun/ARTEX-main/releases/tag/v0.4.0).

### Added and improved (2026-10-11)

- **ScopeSentry background sync**: adds persistent sync history, per-type progress, source records, and distinct asset statistics. Background mode supports paginated processing of more than 5000 records, saving pages before importing them; refreshing or closing the page does not cancel the job, interrupted jobs resume from saved pages, and already committed records are skipped. The old sync endpoint stays compatible.
- **Failed item retries and source tracking**: retry only failed or skipped records, resync a selected range, and download the raw JSON per item. Service assets keep the full MCP details and add source field mappings; each raw JSON item is stored up to 16 MiB, and oversize items and detail fetch failures are reported clearly.
- **Planner completion checks and worker wrap-up**: once goal evidence is achieved, the remaining work is assessed first, then new task claiming stops and a time-boxed wrap-up begins. During wrap-up tool execution is restricted while waiting for workers to save their results and reasons for unfinished work, and the completed state is committed only after a final review; pausing, cancelling, appending goals, and evidence changes can invalidate an old completion candidate.
- **Task status and recovery**: the task list and details show the wrap-up phase, stop reasons, and unfinished items; process restarts, archive restores, and migration of tool arguments from old databases are supported, keeping the original task's main status, model selection, and reporter flow.

### Fixed

- Correctly handle `list: null` when ScopeSentry has zero assets, and use the source count to confirm the end of pagination; when a later page fails, already fetched and committed data is kept, and a partial sync is not shown as fully successful.
- Fixed Windows evidence saving directory flush compatibility, detection of invalid Python system aliases, skill relative path separators, and cleanup failures caused by deleting output files before child processes exit.
- Restored the explicit company scope and keywords in the planner context; fixed retry test HTTP client initialization, cross-platform shell test commands, and flaky assertions caused by fixed waits on background jobs.

### Code organization and execution control

- Split ScopeSentry sync's background jobs, pagination recovery, import, history, details, and retry logic into their own files; sync history, raw records, and asset changes are persisted separately, making it easier to locate the failing stage.
- Organized the task completion candidate, remaining work assessment, engine wrap-up, and frontend status display separately; the completed state is committed through a database transaction with a candidate version check, reducing wrong status writes when pausing, cancelling, or evidence changes race with wrap-up.
- Norma checks wrap-up tool permissions at the execution entry point, covering subagents, deferred tools, and extra tool paths; the time-boxed wrap-up waits for real tool callbacks to exit and saves existing results before terminating the conversation.
- This round mainly improves code responsibilities, state consistency, and recovery; no performance benchmarks were run, and no throughput or token usage improvement is promised.

### GitHub Actions and release

- Added race regressions on isolated PostgreSQL for sync history, pagination recovery, failed item retries, and task completion, plus race regressions for Norma wrap-up control and execution permissions.
- Added Windows on-device PostgreSQL regressions for evidence saving, Python tool execution, and skill paths; existing finding organization, skill metadata, and frontend tests are kept.
- Updated the major versions of Actions used for build and release; the CHANGELOG notes for the version are validated before building and used as the Release body. `doc/` and `docs/` are kept locally only, and CI rejects tracking either directory.
- After tests pass, five platform packages are built for Linux/macOS amd64, arm64 and Windows amd64, SHA256 is verified, and the Linux dual-arch Docker image is published and verified.

### Verification and limits

- Local PostgreSQL regressions: 731 top-level tests passed and 14 skipped in the main module; 690 passed and 8 skipped in the Norma module. The server package was rerun in full on its own after the source was finalized and passed; skips caused by missing real model configuration or system permissions are not counted as passes.
- Real ScopeSentry syncs with non-empty and empty results, recovery after a 5001-record pagination failure, partial page recovery, retrying only failed records, and race checks for the related completion flow pass; the 10 frontend tests, the TypeScript check, and the 32-page static export pass.
- Page-number pagination on the source does not guarantee a fixed snapshot during the scan; being able to recover downloaded pages does not mean source data not yet read will not change. Sync history is kept by default, and redundant temporary payloads are cleared from completed pages; screenshots or body files that the source endpoint does not provide are not marked as synced. Long planner/worker flows with real models still need further acceptance testing.

## [0.3.18] - 2026-10-10

### Fixed

- Fixed the YAML metadata header of the bundled ScopeSentry skill, restoring correct loading of its description, name, and MCP association.
- Saving skill metadata now uses YAML parsing and encoding, keeping multi-line fields, lists, nested metadata, and the original instruction body; content containing colons, hash signs, and newlines is handled correctly, and invalid formats are rejected while the original file is kept.
- Removing an MCP association also clears the old `mcp` field, so the old association does not come back; fields that were not submitted are kept.

### Release and verification

- Added regressions for skill metadata saving, runtime loading, list readback, and loading the bundled ScopeSentry skill; GitHub Actions runs these tests and the existing skill upload regressions, with race checks.
- Locally, the new metadata and upload regressions on Windows, the metadata and upload race regressions on Linux, and the 4 packaging contract checks pass. The existing `TestSkillRelPath` assertion on Windows always uses `/`, which does not match the path separator returned on Windows; that test and the path handling logic were not changed this time.
- Provides amd64 and arm64 packages for Linux/macOS, a Windows amd64 package, a SHA256 checksum file, and the Linux dual-arch Docker image.

## [0.3.17] - 2026-10-10

### Fixed

- Task conversations show the configuration source of the next model call, and the task configuration panel explains the precedence of role bindings, the task chain, and the global default; adds an entry point to filter task LLM recordings and to download a single full JSON record. Switching globally does not override existing task or role configuration.
- When an entire agent run produces no usable text or tool calls, an empty output error is recorded explicitly; the normal behavior of ending silently after tool write-backs is kept.
- The planner tracks independent candidates by asset, entry point/parameter, and vulnerability mechanism, and workers register independent findings one by one; untested, blocked, and tested-without-finding are distinguished, so the first high-severity finding is not treated as a completed audit of the whole system. The runtime guidance also applies to existing custom prompts.
- When loading the intercept policy or reading the approval configuration fails, calls are blocked conservatively, and the cache is published only after a full load; each call records only one final allow/block audit action.
- The workspace file manager uses Go `os.Root` to confine reads, writes, uploads, downloads, and deletes, blocking symlink and Windows junction escapes.
- noa learns the explicit actual window from context overflow errors and uses it for later turns of the current session; output budget fields are excluded, and the recovery threshold for small-window hints and stale suppression state after the window shrinks were fixed.

### Release and verification

- Releases automatically include the notes for the version from CHANGELOG, and keep the commit and PR links generated by GitHub; amd64 and arm64 packages for Linux/macOS, a Windows amd64 package, and a SHA256 checksum file are provided.
- Local agent offline regressions, guard/intercept, workspace boundary tests, noa/noaadapter regressions, the related Go vet, the frontend TypeScript check, and the production build pass; GitHub checks pass for the fix PR #8 and for the main branch after merging.
- The two local agent approval integration tests that depend on PostgreSQL were not verified, and no real external model requests were made. The multi-finding guidance does not guarantee the model never misses a finding; the learned window is only valid for the current noa session.

## [0.3.16] - 2026-10-10

### Finding merging and report organization (2026-10-10)

#### Added

- **Same-vulnerability folders and unified reports**: adds a merge layer, member relations, and audit history on top of the original findings; distinct vulnerability counts are based on the actual defect, while original records, ratings, statuses, reports, and traffic evidence are all kept. Supports paginated candidate lookup, viewing members, removing members, dissolving folders, and regenerating the unified report.
- **Agent-driven organization without an extra manual approval gate**: clear duplicates are merged automatically by the reporter, which generates a unified report; uncertain records get their own report first, and suspected-duplicate suggestions are only for optional later review. After a user correction confirms a merge, the existing reporter queue is reused to generate the unified report automatically, with no need to click generate again; when the model or reporter is unavailable, the committed merge is kept and the report is shown as pending.
- **Version protection and real completion checks**: changes to members, reports, or evidence make the unified report stale; writes must match the version read. "Organize selected" and run records after corrections are marked completed only after the separate reports and related unified reports are actually saved with matching versions; missing, stale, stopped, or invalidated source tasks show a failure reason, without waiting for suggestions pending review.
- **Compatible with the existing views and exports**: the findings page can be viewed by vulnerability, task, asset, and original record; exports default to unified reports plus unmerged records, and can include original sub-reports too. When a unified report is missing or stale, this is stated clearly and the original material is attached; inherited tasks keep the source finding groups read-only.

#### Development and maintenance

- Keeps the idempotent PostgreSQL migration at startup and the existing reporter trigger settings; upgrades keep custom prompts; disabled or deleted reporters are not re-enabled, history is not scanned proactively, and targets are not probed again.
- Added regression tests for agent guidance, database versions and completion checks, server-side scope limits/exports/correction queuing, and trigger queue isolation, following this project's `tests/` overlay layout; added a GitHub Actions regression job with isolated PostgreSQL and a frontend production build; see the README for how to run them in development.
- The feature is based on [RuoJi6/ARTEX PR #2](https://github.com/RuoJi6/ARTEX/pull/2); the pinned commit `24d4726aeea638ef5ae3bd01d288a4ffc49187ce` was reviewed and integrated, with additional changes to the non-blocking report flow, automatic queuing after corrections, and checks on the actual report artifacts; the main branch was not replaced directly.

#### Verification record

- On Linux (Go 1.26.3, isolated PostgreSQL 18.6), the targeted regressions **all 81 passed** (including subtests), actually run with `-race` and with no skips; they cover the existing report/retest paths, merging/export, automatic queuing after corrections, queue scope isolation, and report artifact checks. CI runs the same targeted regressions with isolated PostgreSQL 17.
- Linux main module full run: 945 passed, 3 failed, 1 skipped. The failing `TestGraphOverviewExpandsAssociatedCompanyScope`, `TestCoreTaskLifecyclePG`, and `TestTaskMetadataPatchReturnsRenameAndPin` all reproduce on the unmodified baseline `cdcabb4`, involving task scope, initial nodes, and asynchronous temp directory cleanup respectively; the original test assertions were not changed.
- Windows (isolated PostgreSQL 17) targeted regressions: 78 passed, 3 failed. The existing traffic persistence path returns `Access is denied` on directory `Sync`, affecting two existing report tests and the new merged traffic export test; on Linux all three pass the race check. The existing path/directory cleanup failures in the Windows full run were also compared against the baseline, so a full pass on all platforms cannot be claimed from this.
- `norma` module full run on Windows: 710 passed, 12 failed, 8 skipped; all 12 failures reproduce on the unmodified baseline. These counts include subtests and fuzz seeds and do not represent the number of top-level test functions.
- All overlay tests of the main module and `norma` compile; the overlay runner self-tests and the 4 release build contract tests pass. The 7 frontend unit tests and the production build pass, and the format check of changed files passes.
- CodeGraph was synced and used to check impact and call relationships. Merge judgment and report quality were not accepted against real external models; the final GitHub Actions results are those on the check page of the corresponding commit.

## [0.3.15] - 2026-10-07

### Intercept

#### Added

- **New built-in intercept rule "Delete-style endpoint paths"**: the built-in destructive HTTP rule used to recognize only the DELETE **method**, but most applications' delete endpoints can be triggered with GET/POST, and calls like `curl 'http://t/api/user/delete?id=1'` really delete target data. A new `deny` rule now covers `/delete /del /remove /unlink /erase /destroy` (the verb must be followed by a separator, so `/delivery`, `/details`, `/delta` are not intercepted by mistake). Existing instances get it after upgrading too, and it can be disabled or deleted under "System → Command intercept".
- **Token usage of model fallback approval can be viewed separately**: approval calls used to have no metering attribution of their own, so their cost could only be guessed. They are now metered separately, and a usage card was added under the "Model fallback approval" switch in "System → Command intercept" — cumulative statistics for call count, input, output, cache reads, and cache writes, plus a mini bar chart of daily usage over the last 30 days.

### Finding notifications

#### Added

- **Findings can be pushed to IM**: a new "System → Notifications" page can push scan findings to six channels: **DingTalk, Feishu, WeCom, generic webhook, Telegram, email**. Any number of instances can be configured per type, and each instance has its own enable/disable, rate limit, and filter rules.
- **Two delivery modes, real-time and digest**: real-time mode sends each finding as soon as it matches; digest mode merges them into one message per global period (default 30 minutes), starting with the number of new findings and their severity distribution. For "high severity in real time, the rest in a digest", create two channels and configure each.
- **Filter rules support four dimensions**: minimum severity, restriction to a task / asset scope, finding type keyword include and exclude (exclude wins), and whether to receive handling status changes (off by default). Messages carry a "View details" button whose target is set by the global "Link-back URL" configuration; if empty, no button is shown.
- **Delivery history and manual resend**: lists each delivery's status, attempt count, failure reason, and channel, filterable by channel and status; failed items can be resent with one click (resending resets the retry count).
- **Masked channel credentials**: webhook URLs, signing secrets, bot tokens, SMTP passwords, etc. are echoed back only as masked values with a trailing hint; submitting them unchanged means "do not modify", and clearing the input deletes the field.

#### Fixed

> This section is the result of a security audit after the first version of the feature was done; each item was reproduced first, then fixed, then covered by a regression test.

- **Fixed masks being bypassable by "changing the target URL while keeping the credentials" (critical)**: configuration merging always kept the stored value for "keys not mentioned", so changing only the URL and staying silent about the credentials made the server send the real stored credentials to an arbitrary address, completely silently. Now, whenever the target URL changes, every credential field must be stated explicitly — either a new value, or explicitly empty to mean it is no longer needed.
- **Fixed zero escaping of untrusted content in markdown-based channels**: finding titles and summaries come from model output and asset URLs come from scan results, so a crafted title would render as a clickable external link in DingTalk / WeCom / Feishu, and image syntax would be fetched by the client on render, leaking the reader's IP. Content is now flattened to a single line and markdown metacharacters are escaped, with the escaping done at each channel's rendering exit rather than in the shared title function.
- **Fixed the SSRF surface of delivery addresses**: loopback, internal, and cloud metadata addresses used to be deliverable, and the first 200 bytes of a failed response body went into `last_error` and were echoed back by the delivery history, forming a semi-blind read primitive. The defense is now applied at the **dial stage**, covering DNS rebinding, and cross-host redirects are rejected; loopback and link-local addresses require explicitly setting `ARTEX_NOTIFY_ALLOW_LOCAL=1`, while RFC1918 private networks are still allowed (self-hosted internal SMTP relays are common).
- **Fixed notification credentials leaking through error messages**: DingTalk's `access_token`, WeCom's `key`, and Telegram's bot token are all in the URL, and on delivery failure the full URL flowed into four places: `last_error`, the delivery history, server logs, and frontend messages. They are now all redacted, keeping only `scheme://host` and the underlying cause.
- **Fixed silent loss when a truncated digest message marked the whole batch as delivered**: the truncated findings were neither in the message nor in the failure list, yet the delivery history showed success. Packing now works on **whole** entries, only the entries that fit into this message are marked, the rest go back to the queue for the next message without consuming retries, and the message header states how many will continue in the next one.
- **Fixed a typo in the minimum severity threshold silently disabling the filter**: with a typo like `hgih` in `min_severity`, the unknown severity's ordinal was 0 and the check degenerated to always true, so users thought they only received high severity while every finding was pushed to the group. Values are now validated on the write path and the valid options are listed.
- **Fixed `rate_per_min = 0` (no rate limit) being unreachable**: the database write layer silently turned an explicit 0 into the default value, so operators thought they had lifted the rate limit while it was still applied. The default is now filled in at the API layer when the field is absent.
- **Fixed digest channels completely bypassing the token bucket**: `rate_per_min` previously had no effect in digest mode. The number of entries claimed is now bounded both by this round's quota and by an in-memory cap.
- **Fixed failure handling being decided by the batch's maximum attempt count, letting old deliveries drag down new ones**: an old delivery that had already been retried twice would drag brand-new deliveries in the same batch into failed, permanently losing new findings without a single retry. Each entry is now decided individually.
- **Fixed deliveries with unparseable snapshots in a digest batch being silently marked successful**: such entries were skipped at render time but counted as successful along with the batch. They are now explicitly failed with a reason.
- **Fixed the number of deliveries per round on one channel possibly exceeding the lease duration**: in multi-instance deployments, rows whose lease expired were claimed again, sent twice, and had their attempt count incremented twice. The per-round cap is now derived from "lease / per-attempt timeout", and an assertion pins down the relationship between these constants.
- **Fixed Telegram truncation possibly cutting HTML entities**: truncation only avoided half tags, not entity fragments like `&amp`, and the parser might reject the **whole** message because of them. Both are now avoided.
- **Fixed the email channel treating transient SMTP failures as permanent**: 4xx codes such as greylisting `450` are temporary rejections that should be retried later, but were always treated as fatal, so servers with greylisting made every notification fail after the first attempt. The first digit of the reply code is now used: 4xx is retryable, 5xx is a permanent failure.
- **Fixed submitting nested structures actually writing the mask literal into the database**: object fields like `webhook.headers` can only be masked or submitted as a whole, and a mask sentinel placed inside the object was stored as a real value, silently breaking authentication. Such submissions are now explicitly rejected.

#### Design notes

- **The transaction that writes a finding does only one blind INSERT**: `notification_events` is written in the same transaction as the finding, so a commit guarantees both are atomically consistent. This INSERT deliberately does not read the channel table or run the user's filter rules, and is wrapped in a `SAVEPOINT` — otherwise one misconfigured filter condition could abort the transaction and keep a high-severity finding out of the database.
- **Deliveries are claimed with leases rather than long transactions**: after claiming with `FOR UPDATE SKIP LOCKED`, the row is set to `sending` and `next_attempt_at` is pushed into the future as a lease, so no database lock is held during delivery; rows left behind by a process crash are claimed again after the lease expires, self-healing without infinite retries.
- **Rate limiting does not consume the retry budget**: the round's quota is computed from the token bucket first, and then that many entries are claimed. The reverse order would charge an attempt to deliveries held back by the rate limit, and the three-attempt budget would be used up by pure waiting before falling into failure.

### Test infrastructure

#### Fixed

- **Fixed permanently leftover assets caused by the cleanup order of the company ICP ownership test**: `defer d.Close()` ran first when the function returned and `t.Cleanup` ran after it, so all cleanup statements hit an already closed connection, the errors were discarded, the test assets and companies stayed in the database forever, and other tests asserting on asset counts failed mysteriously. Closing the connection now also goes through `t.Cleanup` and is registered first, and cleanup failures are surfaced.

### Traffic

#### Added

- **The traffic list supports content search, path / status code / length filters, and column sorting**: the filter row can filter by **response content** (using the full-text index), **path**, **status code** (exact value or buckets like `2xx`), and **response length range**; the header can sort by time, status code, and response length, and the sort preference is stored locally. Two new indexes take effect automatically on existing instances after upgrading; the query contract of the agent tool is unchanged.
- **"Clear all" in the traffic list**: deletes all traffic records at once (ignoring the current filter) and cleans up historical host directories no longer recorded in the index; after clearing, a full compaction runs and the actual space freed is shown. Traffic evidence bound to findings is stored in a separate evidence store and is not affected.

#### Fixed

- **Fixed disk space not being released after deleting traffic**: the index database was created without `auto_vacuum`, so the file never shrank; the full-text index is also `contentless_delete`, where `DELETE` only writes tombstones without reclaiming the original postings — deleting traffic actually made the index bigger (measured: 6MB of captured bodies → 16MB index, still 16MB after deleting everything). Newly created index databases now enable `auto_vacuum=incremental`, and after each delete, full-text index merging and incremental vacuum run in the background in chunks, gradually returning space to the system (the same scenario drops back to 104KB); the write lock is released between chunks, so traffic recording is not blocked.

> Upgrade note: `auto_vacuum` can only be set when the database is created, so **index databases of existing instances stay in the old mode**, where incremental vacuum is a no-op (a one-line hint is printed at startup). On these databases tombstone growth stops after the upgrade, and the space already used can be reclaimed once with "Clear all" — that step converts the database to incremental vacuum mode, after which everyday deletes take effect on their own.

### LLM

#### Fixed

- **Fixed custom session headers not applying on the "one-shot LLM call" path**: goal decomposition (round 0) and cold node compaction are both one-shot calls without a transcript store, so the context had no session id and the gateway could not read the header — for endpoints like opencode zen that return 400 when the header is missing, this showed up as "round 0 fails, but later planner rounds work fine". Both paths now explicitly attach a session id that is stable per exploration, which also lets token usage be attributed to the right exploration (previously it was not recorded at all).

### Tools

#### Fixed

- **Fixed output truncation not applying to MCP and custom tools, letting large results flood the context**: MCP tool results were never truncated, and HTTP-based custom tools always used a hard-coded 6000 characters instead of the session's configured limit because they received an empty tool context. Both paths now go through `actool.Capture`: truncated to the session's `MaxOutputChars` (default 30000), and when `ToolOutputDir` is configured the full output is spilled to disk, leaving only a leading snippet and a file pointer in the message.
- **Upgraded norma to v0.4.3**: the harness layer now runs `CaptureOnce` uniformly, so no tool (including ones added later) can flood the context by missing truncation.

### Deployment

#### Fixed

- **Fixed the activity feed failing to connect behind a reverse proxy**: the SSE address used to be built unconditionally as `${hostname}:8787`, so in deployments proxied to 443 the page loaded from `https://domain/` while SSE pointed at port 8787, which was not exposed publicly, and the activity feed kept reconnecting. It now depends on `NODE_ENV`: production defaults to the same origin, and only `next dev` keeps `:8787` to bypass Next.js buffering of SSE; the `NEXT_PUBLIC_SSE_BASE` override is kept. The README gained reverse proxy deployment notes.

### Findings

#### Fixed

- **Fixed the asset list in the "By asset" view having no scrollbar when it overflows**: the outer card only had a `max-height` without a definite height, so the scroll viewport's percentage height could not be resolved, and with many assets the list either burst out of the card or was cut off without scrolling. The height limit is now applied directly to the asset tree's native scroll container and adapts to the window height.

### Database

#### Fixed

- **Set a limit on the connection pool**: `database/sql` by default does not limit how many connections the application itself can open, and when the pool has no idle connection it creates a new one unconditionally, all the way up to PostgreSQL's `max_connections` (default 100) before being refused. It is now capped at 32, and excess queries wait for an idle connection, so the same load shows up as slower instead of erroring; if `max_connections` has been lowered, lower this too.
- **Improved connection reuse**: the default `MaxIdleConns` is 2, so with more than 2 concurrent connections, connections were closed after use and the next request had to redo the TCP and PostgreSQL authentication handshake. The idle limit now equals the total limit, with a 30-minute connection lifetime and 5-minute idle eviction added, so the pool naturally replaces long-lived bad connections.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.14] - 2026-09-24

### Task list

#### Added

- **New "Findings" column in the task list**: shows the counts for C / H / M / L by severity, with non-zero buckets colored by severity, so each task's finding volume and distribution is visible at a glance.

#### Changed

- **Narrower "Description / Goal" columns**: overly long content is truncated with an ellipsis and the full text shows on hover, so long text takes less horizontal space in the list.

### Exploration graph and planning overview

#### Changed

- **Trimmed the planning overview (graph_overview) and capped every list**: recent facts, finished intents, pending intents, cold-zone summaries, and confirmed finding details all switch to "latest window + count fallback", and anything not shown can be queried on demand; this significantly reduces the LLM context size per round and avoids context bloat in long tasks (the cold zone of related task overviews is limited too).

#### Fixed

- **Removed reverse duplicate edges between digest and finding in the exploration graph after collapsing cold nodes**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset intercept

#### Added

- **Global asset intercept rules (blocklist)**: supports exact and fuzzy domain / IP / URL entries and CIDR ranges, with create/read/update/delete and enable/disable for rules; the management page is under "System → Asset intercept"; government (`.gov` / `.gov.cn`) and education (`.edu` / `.edu.cn`) sites are blocked by fuzzy match by default.
- **Asset intercept wired into the execution chain**: before dispatching an intent (`add_intent`) or inserting assets (`insert_assets`), the agent checks the target assets against intercept rules — intercepted intents are not dispatched, intercepted assets are not inserted, and the asset information and intercept reason are returned to the agent.
- **Task-level intercept / allow (allowlist) rules**: independent of global rules and effective only for that task, evaluated "intercept first, then allow" — a matching intercept rule forbids it; if no intercept rule matches but the task has allow rules and none match, it is "not allowed to test"; when no allow rules are configured, the allowlist is not enabled. Rules can be entered when creating a task, and added, edited, deleted, enabled, or disabled under "Overview" in task details.
- **Task templates support preset categories and task-level intercept/allow rules**: a template can save a task category and a set of task-level rules, which are brought into the new task form when the template is applied.

### Operation review

#### Fixed

- **Tightened the model judge output protocol to reduce wrong approvals caused by truncation**: the verdict comment limit was lowered from 500 to 120 Chinese characters and "output only JSON, no preamble or code block" is enforced, so verdicts truncated by `MaxTokens` no longer fail to parse and get approved under the model failure policy (fail-open).

### Task archive

#### Fixed

- **Archiving skips symlinks instead of failing the whole package**: the archive format only supports regular files and directories end to end, and previously any symlink in the working directory made the whole task archive fail; such symlinks are now skipped and logged, and the remaining files are archived normally (links are not followed and nothing escapes the directory tree).

### Account and compliance

#### Added

- **New "Terms of use and disclaimer" dialog before login**: you must tick the agreement before logging in.

### License and dependencies

#### Changed

- **The project adopts the AGPL-3.0 license**, and the license and disclaimer sections of the README were improved.
- **Upgraded norma to v0.4.1**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Exploration chain broadcast board

#### Added

- **New "Exploration chain broadcast board" on the task details page** (#144): shows the task's exploration nodes (start/goal/intent/fact/finding/hint/compaction) as a timeline feed, with type filters, keyword search, ascending/descending order, pagination, and auto-refresh; on the "newest-first first page" it is the live position and refreshes every round, and when you leave that position it only accumulates an unread count without disturbing your reading, with "N new broadcasts · back to latest" to return in one click. Grouped by day, so even when paging through long tasks you can tell which day something happened.
- **Each board row shows the node id** (#145), making it easy to cross-reference the exploration chain graph and locate specific nodes.
- **Expanding a board node shows upstream/downstream nodes and anchored assets** (#147): expanding a broadcast shows the node's upstream (where it came from) / downstream (what it produced) relations, and hovering over a related entry pops up that node's card (type/status/source/time/summary/payload snippet); the assets anchored to the node are listed too (type tag + recognizable text). The related data is delivered with the board page, so expanding sends no extra requests.
- **Board search can filter by node id** (#150): besides "content / source", the search box also matches node ids; enter a plain number or the "#41" form shown in the UI to locate the exact node.

### Intent management

#### Added

- **Intent deletion supports "soft delete / hard delete"** (#149): pending, running, and paused intents can all be deleted, a reason is required, and the mode is chosen in the confirmation dialog.
  - **Soft delete (default)**: the intent is set to "Deleted" and the reason is recorded in a separate field, keeping the intent node and all of its outputs and lineage.
  - **Hard delete**: physically removes the intent and the descendant nodes "supported only by it" (cascading along output / intent chains to the leaves), so no orphaned data is left; the deleted intent's token metering is archived under its original dates; shared nodes (still referenced by other intents), goals, and the task's root facts are always kept, and the hard delete dialog shows the expected number of cascaded nodes.

  Both modes notify the planner that "this intent was deleted by the user + reason", and it replans accordingly.

### Operation review

#### Added

- **Approval records can be filtered by status and verdict source** (#139): filter approval records by approval status and verdict source to find the record you need quickly.

#### Fixed

- **Pending approval requests load independently of history pagination** (#133): pending requests are no longer affected by the history list's pagination and stay fully visible while browsing history.

### Agent

#### Changed

- **The worker role description now uses generic cybersecurity platform wording** (#138).

#### Fixed

- **Fixed cold node compaction never triggering**: the Compactor is now wired into the per-task planner; cold node compaction (cold-digest) never ran before because it was not wired in, and is now restored.

### Chat

#### Added

- **Chat supports @ references to several record types with scroll pagination** (#135): several types of records can be @-referenced in chat, and reference candidates load with scroll pagination.

#### Fixed

- **Long message bubbles are constrained to the conversation panel** (#137): overly long message bubbles no longer overflow the conversation area.
- **Fixed uploading a file before a new conversation is created reporting "missing upload file"**: the composer's file picker now snapshots the `FileList` into an array before clearing the input and invoking the callback; previously the draft state first had to create the conversation asynchronously, and by the time execution resumed the `FileList` live-bound to the input had been cleared, so the upload lacked the file field and the backend returned 400.

### Traffic

#### Fixed

- **The traffic recording proxy listens only on 127.0.0.1 by default** (#129, #130): avoids exposing an open proxy on other network interfaces with the default configuration.

### Web

#### Fixed

- **Fixed the task list page of the static export losing the global header**.
- **Added the missing related-traffic mock for the demo finding details page, fixing a blank screen**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation review

#### Added

- **Approval records can locate their source** (#125): clicking an approval record's "Source" jumps back to the tool execution that triggered the approval — it opens the original full conversation, pages automatically to the target position, expands the command and result in place, and centers and highlights it in the scroll area, keeping the surrounding messages readable; automatic correction stops when the user scrolls manually. Across normal conversations, task workers, the planner, and main agent conversation segments, the location is resolved precisely through the persisted `tool_use_id` and task mapping (with a new scoped call ID index); when it is missing, duplicated, or ambiguous, an explicit message is shown instead of jumping to another execution. A clear message is also shown when the conversation was deleted or the task was archived / the record is missing.
- **Approval records support pagination** (#115).

#### Changed

- **Trimmed the model review input** (#124, #125): the model review (the LLM judge after no rule matched) input is narrowed to a versioned JSON of "the current complete tool call + explicitly selected short background + the local working directory". The background only uses real user messages (the current user message of chat / the task main agent); workers no longer attach the intent summary and also clear background inherited from the parent agent; the planner and auto-triggered conversations do not fabricate user messages. The task description, goal, operating constraints, global exploration state, call history, and the worker's full intent are no longer attached — they are still used as before for agent execution and independent conversation audits, just not in the operation review. Every verdict must output JSON `decision`/`comment`, and the comment must include "the actual operation, the consequence if it succeeds, the matched rule"; instructions in arguments or background cannot change the review policy. The input snapshot and fingerprint actually sent to the model are saved; old snapshots are kept and labeled with their version, and old inputs are never reconstructed from current data.

#### Fixed

- **Model verdicts wrapped in code blocks are no longer silently approved** (#126): if the verdict JSON returned by the model was wrapped in a ``` code block, parsing used to fail and the call was silently approved under the model failure policy; the code fence is now stripped before parsing, and only if parsing still fails is the configured failure policy applied.

### Agent

#### Added

- **Experimental noa context compaction** (norma upgraded to v0.4.0): an experimental feature that can be enabled in system settings, off by default. When enabled, noa (model-driven context compaction) takes over context compaction for four kinds of agents — main agent, planner, worker, and chat — replacing the built-in compaction; the original compacted text is persisted to an archive, collected under `<workDir>/noa/<conversation ID>/` (not scattered across task directories), with one globally unique directory per conversation ID. If wiring fails it falls back to the built-in compaction automatically without interrupting real tasks; the switch is read once per run, so toggling only affects runs started afterwards.

#### Fixed

- **Exploration graph tools reject instead of crashing with nil when there is no task context**: calling exploration graph tools outside a task context now returns a clear error instead of crashing on a nil store dereference.

### MCP

#### Added

- **Support for legacy SSE MCP servers** (#117): compatible with MCP servers that only offer the legacy SSE transport.

### Traffic

#### Fixed

- **Traffic search matches record hosts with port awareness** (#114): host matching in `traffic_search` now includes the port, so records for the same host on different ports no longer interfere with each other.
- **A failing `traffic_search` description migration no longer interrupts later reporter migrations**: a single migration failure is isolated and does not affect later migrations.

### Web

#### Changed

- **Dividers between finding options in task retest** (#122): separators were added between the finding options for retest, for clearer visuals.

#### Fixed

- **Fixed the demo task details page crashing entirely**: in mock mode, the "Conversations" tab of task details fetches `GET /api/tasks/<id>/side-questions` (side question history), but the mock handler had no such route, and the read fallback returned `[]` on the rule "a path ending in s is a collection", so `data.items` was `undefined` and the side hook's `merge()` threw `TypeError: t is not iterable` while iterating it; the exception happened inside the `setItems` updater and was rethrown by React during render, so the caller's `catch` could not catch it, and the whole page was taken over by the error boundary showing "This page couldn't load". The mock handler now explicitly returns an empty side question history, and `sideAPI.history` also normalizes the return value (`items` that is not an array always falls back to `[]`) as defense in depth.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Network

#### Added

- **Official DeepSeek source for web search**: reuses the currently active LLM configuration directly. It differs in nature from the other three sources — DeepSeek has no directly callable search API; search only exists inside its Anthropic-compatible API (the `web_search_20250305` server-side tool) and is executed by DeepSeek's servers. Therefore it **only supports the official DeepSeek endpoint + the anthropic protocol** (OpenAI protocol endpoints reject server-side tools outright), every search **costs an extra model call**, requests **do not go through the search egress proxy** and are **not recorded in the traffic audit trail**, and results contain **only titles and links** (no summaries; use WebFetch when the body is needed). The settings page explains these limits but **does not validate or block**; it is up to the user to confirm they are met, and the "Test search" button can run a real search to verify.

### Agent

#### Added

- **Workers can look back across work items**: adds `search_all_worker_traces` (no need to know the intent_id first; searches the execution processes of all work items in this task by keyword for matching steps) and `get_worker_trace` (once a work item is selected, lists its step stream, searches it in place by keyword, and fetches full content by step_id), for reusing observations other work items saw but did not write into facts, avoiding duplicated work.
- **`node_detail` made available to workers**: together with the lookback tools above, a worker that has an intent_id / node id can look up that node's full details directly.
- **Planning rounds triggered by `add_hint` are announced explicitly**: previously a new hint was only folded into the state overview for the planner to discover by itself; now an `add_hint` records a trigger for the planner saying "a human added N strategic hints: …" (a batch counts as one, without spamming per hint), so the planner is told explicitly that "this round was triggered by new hints" and sees the hint content directly.

#### Changed

- **Global state hints use a "loosened" tone**: encourage divergent exploration and prompt reporting of cross-intent leads, instead of converging too early.
- **Trimmed the worker boundary wording**: an initial block does not mean the direction is exhausted; the focus is "finish the bypass techniques within this intent before concluding".
- Removed the per-asset `related` argument from `insert_assets`: its only effect was deciding whether to add the asset to this task's scope, and the value was not stored (it was voided whenever the same asset was registered again, and the UI could not show which ones were judged unrelated), so in practice it only made the model make one more judgment that could not be kept.
- **The test scope (`task_scope`) is no longer affected by the asset coverage switch**: automatic scope insertion by `insert_assets` (`source='auto'`) runs whether coverage is on or off, and `add_task_scope` is always provided to the planner / in-task main agent / goal decomposition agent. The scope is the task's authorization boundary and the filter baseline for asset queries; the coverage switch only decides whether to use it as the denominator for metrics, not whether the scope itself accumulates. Previously turning coverage off disabled both the `auto` and `agent` write paths, leaving `task_scope` with only rows added manually in the UI. `list_untested_assets` is still hidden when coverage is off (it is purely a coverage view).

#### Fixed

- **Fixed assets leaking between tasks** (#59): `list_assets` used to hard-code the task id as 0 and query the entire shared asset store, and the model had no way to write a scope filter, so it took other tasks' assets (especially IPs) as this task's targets and the testing direction drifted. It now only returns assets within the test scope (`task_scope`) of [this task and directly related tasks], matched by **ownership** rather than literal value: a root domain in scope makes all its subdomains/services/endpoints queryable, and a network range makes the hosts and services within it queryable; fetching an out-of-scope asset by id does not work either. Non-task contexts (Auto/pentest) have no scope to rely on and still fall back to the whole store. This also fixes the ownership blind spot where direct-IP hosts (e.g. `http://1.2.3.4/api`) could not be matched by a network range scope. The UI's "Test assets" view (filtered by task producer) is not affected.
- **Fixed the worker cross-work lookback tools being removed by mistake**: after `search_all_worker_traces` / `get_worker_trace` (and `node_detail`) were added to the worker's default tool set, an old migration that "narrows the worker tool surface" unbound them again at startup, so workers never actually got these tools. They were removed from that unbind list, and databases that already ran the old migration get them rebound to the worker once.
- **Fixed occasional storage failures for `/btw` side questions**: NUL (`\u0000`) escapes, which JSONB does not support, are stripped before storing the side_question checkpoint, avoiding write errors for content containing that character.

### Triggers

#### Fixed

- **Merged trigger conversations deduplicate task descriptions/goals per task**: when several tasks are merged into one trigger, the description/goal of the same task is no longer appended to the message repeatedly, preventing long goals from stacking up and blowing up the context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### Added

- The in-task main agent supports **multiple conversations**: create, switch, and reset context independently, with every conversation interactive.
- Persistent `/btw` **side questions**: ask follow-ups without interrupting the main thread; questions and answers are stored and survive restarts.
- `spawn_task` adds `source_task_ids`, so a new task can **inherit read-only** the assets and findings of source tasks.

#### Changed

- Disabled cross-engagement memory for workers and narrowed their default tool surface: reading context and cross-work review are planning responsibilities, and a worker only handles execution and write-back of a single intent.
- Trimmed the worker default prompt; removed the `terminated` and `worker_name` fields from `get_worker_output`; trimmed the descriptions of `insert_assets` / `list_assets`.

#### Fixed

- Fixed work items stopping early because of **idle turns**: the model sometimes spends a whole turn outputting only thinking, with neither text nor tool calls, and the harness sees a natural end (`end_turn` with no `tool_use`), finishing directly with `completed` + an empty summary — an intent that was not done yet was cut off halfway, showing up as "the task ended normally but there is no text summary at all". None of the five LLM retry layers could reach it: it is not an error; the entry to same-provider safe-window retries is "stream failure", the circuit breaker treats `err == nil` as success, and intent reruns only recognize `model_error`; the SDK's empty response retry cannot reach it either, because that layer decides emptiness by "whether any event was yielded", and thinking deltas are events themselves. The work Stop hook now recognizes such turns and injects a continue instruction so the model carries on with the next step using the thinking it already produced — deliberately not "resend as-is", because such idle turns are usually determined by the shape of the prompt and context, a stable behavior rather than a random glitch, and resending would only make the model think it over again.
- The count reuses the **empty response retry count** from "Retry and backoff" on the LLM page (both answer "the model finished but produced nothing substantial", just with different criteria and means), default 2; `-1` turns it off and falls back to the old "idle turn ends the run" behavior. It is a **total** cap per intent rather than a consecutive count: the harness itself already limits consecutive idle turns to one push, and the quota only refreshes after a real tool turn, so this number guards against loops like "tool → idle → push → tool → idle" exhausting the intent budget. When triggered, the log records `[work <worker> · #<intent>] idle turn… injecting continue instruction (n/N)`. Design in the LLM retry design doc §1.1.
- Fixed the context budget and input layout of `/btw` in long conversations; non-secure contexts (non-HTTPS access) fall back to a degraded side request ID.

### Traffic

#### Added

- Findings can **link multiple traffic evidence items**, which can be reordered, annotated, and distinguished by role (request/response/supporting).
- The reporter agent **automatically links** related traffic evidence before writing the report.
- Added an agent **traffic binding switch**, and completed evidence handoff between agents.

### Tasks

#### Added

- **Conversation-level finding retest**: retests run in a separate agent conversation, and the list and details show the run status.

### Intercept

#### Added

- Approval records gain **details and execution audit**: view the tool request context, the initial model/rule verdict, the execution output, and the argument fingerprint.

### Assets

#### Added

- Task test assets support **DSL search**.

### LLM

#### Fixed

- Fixed the connection test not sending the custom session header, which made opencode zen return 400.

### Network

#### Added

- The MCP HTTP transport can **skip TLS certificate verification**, making it easier to connect services with self-signed certificates.

### UI

#### Added

- The conversation list is grouped by agent, with expand/collapse and independent pinning; conversations can be filtered by agent.
- The attack chain graph renders compaction (digest) nodes and collapses their members.

#### Fixed

- Fixed a blank screen caused by out-of-sync login credentials.
- Intercept messages now explicitly say "platform control", so they are not misread as defenses on the target side.

### Deployment and updates

#### Added

- **One-click update in the UI**: the system settings page gains a "Version and updates" card, and the top bar shows a notice when a new version is available. The flow is: download the release package → verify `SHA256SUMS` → smoke test → stage → exit and let the supervisor script restart it to complete the swap; the page refreshes automatically.
- New supervisor start scripts `start.sh` / `start.bat` as the official entry point (included in both release packages and the Docker image; `install.sh` is unchanged); they decide from the exit code whether to restart, and forward SIGTERM to artex. Verification and swap logic live in Go, keeping the scripts dead simple.
- Automatic fallback on failure: if verification or the smoke test fails, the update is discarded and the current version keeps running; if the new version fails to start 3 times in a row, it rolls back to the previous version. The settings page also offers a manual rollback (note that the database schema is not rolled back).
- Updates only trust GitHub domains and enforce HTTPS, and the release source is not configurable; one-click update is disabled for development builds. GitHub query results are cached for 30 minutes so the top bar notice does not exhaust the API quota.

#### Known limitations

- In Docker only the program is replaced, not the image: toolchains are not upgraded with it, and recreating the container falls back to the version bundled with the image; use `docker compose pull artex` when needed.
- `skills/` from the release package is not synced, so new bundled skills in a new version do not take effect automatically.
- An update is a restart and interrupts running tasks.

### Dependencies

#### Changed

- Upgraded norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### Added

- New "Retry and backoff" tab on the LLM page: the **count** and **interval** of all five retry layers are configurable. A failed model call passes from the inside out through connection retries (SDK; connection resets / timeouts / 429 / 5xx before the stream starts), empty response retries (SDK; finished normally without any content, openai format only), same-provider safe-window retries (replaying a dropped stream before any output was delivered to the caller), round-robin circuit breaking (cooldown and skip after consecutive failures reach the threshold), and intent reruns (rerunning the whole intent after a worker ends with model_error), and an outer layer only gets its turn once the inner one is exhausted. Each layer has two knobs with uniform semantics: empty = use the original default count and exponential backoff; a count uses that count; an interval replaces exponential backoff with a fixed interval; -1 = turn this layer's retries off. The first three layers follow the endpoint and can be overridden field by field in each model configuration (an override that only pins the interval still inherits the global count); circuit breaking and intent reruns are process-level, with only one global setting. Changes take effect hot after saving, with no restart needed. All empty is the current behavior, and old databases are byte-for-byte unchanged after upgrading (new columns default to 0, and a missing settings key means all defaults). The connection test deliberately does not use these parameters — it has a 30s hard timeout, and stacking user-configured retries on top would only make a working endpoint fail with a timeout. Design in the LLM retry design doc.
- Each LLM configuration supports a custom **session header** (`session_header_key`): when non-empty, every LLM request carries that HTTP header, whose value is the current run's session id (`conv-<id>` for chat conversations, `exp<x>-worker-i<intent>` for workers, etc.), for gateways that do prompt caching / sticky routing by a session-id header. It is implemented by reading the session id from the request context and injecting it, without changing norma, so the same shared provider can send different header values per conversation. Old databases get a migration, and the frontend LLM configuration dialog gains an input field.

#### Fixed

- Fixed saving an LLM configuration failing with `23502` when the session header field is empty: the column is `NOT NULL DEFAULT ''`, but `NULLIF($n,'')` was applied by mistake, writing an unfilled session header as NULL and violating the not-null constraint; it is now passed directly with empty string semantics.

### Agent

#### Added

- Wall-clock timeouts now **wrap up in place**: relying on norma v0.3.6, when `MaxDuration` is reached the running tool is interrupted and the run continues on a live ctx for the wrap-up rounds (writing back what has been identified + a summary, with final state timeout), instead of relying on the worker/planner's own external hard ctx of `maxDur+90s` to kill a stuck run outright as `aborted_tools`. Chat uses the same harness and gets the same behavior automatically; the mainagent has no `MaxDuration` and is not affected.
- Stall fallback: when the planner is woken by a heartbeat / no-change wake-up and the whole graph has no open or running intents left, the opening message becomes a stall alert, stating explicitly that no worker is running and no direction is queued, and this round must produce one or more distinct new intents (producing 0 intents is not allowed).

#### Changed

- Restructured the worker prompt: the intent / start instructions / raw JSON of anchored assets moved into the system prompt, rebuilt every round and never compacted away, so continued runs do not depend on the first transcript message surviving; the starting user message is slimmed down to just the global state overview (degradable, tolerating staleness). The cost is that per-intent data is mixed into the system prompt, losing cross-intent cache reuse — a deliberate tradeoff for "never lose the intent".
- Trimmed the planner / worker default bodies and fixed several real-world problems: the planner distinguishes `recent_done` by status (for blocked/exhausted, check the trace before deciding, neither treating it as a dead end nor blindly rerunning), negative conclusions become "observation / doubtful, not final" and the evidence is checked before accepting them, if the goal is not achieved and there are no open/running intents the planner must produce some (hard floor), and depth takes priority over coverage; workers write negative conclusions only as "observation + tentative reading", with the verdict belonging to the planner, and cross-intent leads go into the fact summary for the planner rather than being chased by the worker. Reseeding appends the new default as a new version and switches to it, while user customizations / old versions stay in the history and can be rolled back.
- Narrowed the work agent's default tool set and asset write-back guidance: a worker is only responsible for execution and write-back of a single intent, while reading context / cross-work review are planning responsibilities, so `list_facts` / `node_detail` / `list_companies` and cross-work search (`search_all_worker_traces` / `list_worker_traces` / `get_worker_trace`) were removed from its default tools, leaving only `list_findings` (dedup check before reporting a finding) + `add_finding` / `record_fact` + `insert_assets` / `list_assets`; stale field descriptions in the prompt that conflicted with the `insert_assets` schema (`type=tech`/`on_url`/`props`) were deleted. Old databases get a one-time migration that strips the corresponding bindings; the planner/main bindings with the same names are untouched.

### Exploration graph

#### Added

- **Cold node compaction for the exploration graph (cold-digest)**: old intents / facts that have been inactive for a long time are folded into digest nodes shown in `graph_overview`, while the original nodes are kept forever and can be fully restored by id (lossless storage, only the presentation is compacted, folding is reversible). Cold/hot is decided by reverse reachability, where any live branch makes it hot, with an R=6-round debounce and connected-component grouping; background compaction (minor folds uncovered cold blocks, major recompacts from the source to merge fragments) has activity rechecks and cooldown mutual exclusion, never runs on the hot path, and never overwrites revived nodes. The overview provides `cold_digests` and an asset-indexed `cold_index`, and `expand_digest` / `expand_index` handle restoration. Overviews of related / inherited tasks reuse their own folded views as well, and `expand_digest` supports read-only cross-task restoration. `expand_digest` / `expand_index` are only given to the planner and main agent, not to workers. All with migrations for old databases.
- `graph_overview` outputs the full `finding_list`: findings are a task's most valuable output and there are usually not many per task, so they are now included in full in the overview (unlike facts, which only get a recent window), letting the planner/worker see all confirmed findings at a glance every round without calling `list_findings` again. Each entry is trimmed to `{id, summary, evidence?, from_intent?, assets?}`, where affected assets are given as readable content (url / domain / ip:port) rather than bare ids.

#### Changed

- `graph_overview` no longer lists `hosts` flat: the host list was removed from the coverage block (in large-scope tasks it carried up to 500 host strings per round, with limited value for planning decisions), keeping only `host_count`; specific hosts can be queried with `list_assets` on demand. A new top-level `done_intents_total` (total number of finished intents) parallels `recent_done_intents`, which is truncated to ≤15, so the planner knows whether anything was cut off when deduplicating.

### Tools

#### Changed

- The default binding of `add_company_scope` moved from worker to planner: defining a company's asset scope is the job of planning / main control / Auto, while workers only execute exploration. New databases seed the default binding as mainagent/planner/auto, and old databases get a one-time migration.
- The planner is bound to `list_assets` by default: it now has both `list_assets` (DSL search across the whole store) and `list_untested_assets` (untested within scope); old databases get a one-time backfill that does not override user unbindings.

### Tasks

#### Added

- New "Running workers" column in the task list: counts the intent nodes with `state='running'` under the task, exactly matching "Running workers" in the task details overview, and shows 0 when no worker is running.

### Network

#### Added

- New **global egress proxy** configuration: all target traffic can go out through one global proxy (http/https/socks5, with `user:pass` support). With traffic capture on, it is the upstream of the MITM recording proxy (traffic is still recorded and then goes out through the proxy; both the intercept and pass-through paths use the upstream, so the source IP is not leaked); with capture off, it is injected directly into the agent's bash environment and WebFetch (`proxyEnv` adds `ALL_PROXY` to support socks5). The configuration is stored in the settings KV table (no migration needed), and the frontend system settings page gains a "Global proxy" card, independent of the web search proxy and the LLM proxy.

### UI

#### Fixed

- Fixed semantic errors in intent status labels: `exhausted` "Exhausted" → "Budget exhausted" (it actually means the step / time budget cut it off midway with only partial results written back, not that the direction was fully explored), `blocked` "Intercepted" → "Execution error" (it actually means model / API / network failures exhausted their retries and the intent was barely explored, not that the target / WAF blocked it), and added the previously missing `stopped` "Stopped" (work stopped manually by the user).

### Dependencies

#### Changed

- Upgraded norma to v0.3.4: MCP tool output gains truncation and spilling to disk (see `651b961`); the later v0.3.6 supports in-place wall-clock wrap-up.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### Added

- Model configurations gain an "Output limit", with a choice of which request field name carries it: the limit caps how many tokens a single reply may generate and is sent with every request; 0 (default) = the field is not sent and the server default applies; it is different from the "Context window" — the latter is the model's total capacity, used only locally to compute the compaction threshold, and never appears in requests. The field name switch only matters for the `openai` (Chat Completions) format: empty (default) sends `max_tokens`, which almost all compatible gateways only accept; OpenAI's official reasoning models (o series / GPT-5) conversely only accept `max_completion_tokens` and reject `max_tokens` with `unsupported_parameter`, so such endpoints need to be switched to the new field by hand. The two other formats each have a fixed field name (`max_tokens` for Anthropic, `max_output_tokens` for the Responses API), so for non-openai formats the switch is greyed out and cleared on save. It also wires up a path that had never been connected: none of the five places — planner / worker / chat / main agent / goal decomposition — had ever set an output limit, so the openai family never sent it and Anthropic used the SDK's 8192 fallback; adding only the setting without wiring this path would mean a filled-in number never took effect. The value is now resolved from the configuration every round, just like "Streaming output", so it takes effect on the next round after a failover switch. Old databases behave exactly the same after upgrading (new columns default to 0 and an empty field name).
- When "Test" is clicked in the console, the status code of each HTTP attempt and the gateway's raw response body are written to the server log (the body is trimmed to 4K), making it easier to diagnose 401s, quota messages, empty frames, HTML pages being returned, and so on, instead of only seeing the ok / err collapsed in the UI. This log does not depend on the "LLM recording" switch.

#### Changed

- The task LLM configuration chain can now be edited in any state, no longer limited to "running / paused / configuration chain exhausted": after a task ends (done / failed / timeout), the main agent conversation still uses this chain, and when a model on the chain had problems it could neither be changed nor could the interaction continue. The terminal-state checks in both the backend HTTP layer and the DB transaction were removed, and the frontend dialog also allows editing and saving for terminal tasks. Saving for a terminal task no longer reopens quota-blocked intents (those intents would be moved to open, with no worker to execute them and no longer meeting the conditions for "Rerun intent"); to keep running, still use rerun intent / add goal, which bring the task back into the running state.

### Agent

#### Changed

- "Send a message to a running worker to adjust direction" now reuses the existing pause / resume + transcript resume mechanism, behaving the same as main agent conversations: previously it used a custom intervention persistence protocol that touched the scheduling barrier, the recovery flow, and over a dozen activity query filters. Messages are now injected through the next round's input, and the intent runs directly in its own goroutine, not limited by the 3-slot worker pool, running as soon as it is sent; the frontend keeps the message box and restores the "Just continue" button, and sending is now carried by SSE. No DB schema changes. Tradeoff: messages are only kept in memory with no crash recovery, and when a message is sent the task may briefly exceed its concurrency by one worker (a low-frequency case, acceptable).

### Tasks

#### Fixed

- Fixed out-of-memory (OOM) crashes when archiving large tasks: archiving now streams snapshots to disk instead of reading the entire task into memory at once; it also closes three recovery gaps on the cold archive path — in modern installations traffic only lives in SQLite, and a crash between the PostgreSQL commit and the SQLite commit used to leave an archived task's traffic stranded in hot storage with no way to recover it; now a staging journal is always written whether or not a history directory exists, and a missing `journal.json` is treated as discardable.
- Fixed the system lagging with many tasks: loading conversations and loading pages had noticeable delays. Queries for the task list, task context, and exploration records were optimized together, and the frontend dashboard, conversation page, and task details page now make fewer duplicate requests.

### Assets

#### Changed

- Removed the "at most 256 rules" limit on company asset scopes: companies that enter their scope IP by IP / domain by domain easily hit this cap, after which they could only be split into several companies, while the scope itself is not slower because of more entries. The limit was removed in all three places (frontend, backend, and demo mock); a single rule is still limited to 1024 characters, and the 2 MiB request body limit stays as a fallback (roughly forty to fifty thousand rules).

### Skills

#### Fixed

- Fixed uploading a skill archive failing with "Upload failed: zip: unsupported compression": the Go standard library only ships Store / Deflate decompressors, so bzip2 and Zstandard archives written by compression tools at non-default levels could not be extracted. These two decompressors were added (pure Go implementations, no new external dependencies); for methods that really cannot be extracted, such as Deflate64 / LZMA / XZ / PPMd, and for encrypted archives, a localized message is now reported before extraction, naming exactly which file uses which compression method and how to repack it, instead of throwing the low-level English error at the user.
- Fixed skill file name validation rejecting Chinese file names: path validation used to be an ASCII allowlist `[A-Za-z0-9-_./]`, so a single file with a Chinese name in the archive (something like `参考/说明.md`) made the whole upload fail with "Archive contains an invalid path". It is now a Unicode denylist: file names and spaces in any language are allowed, while control characters, invalid UTF-8, zero-width and bidirectional control characters (RLO file name spoofing), `\ % # ? * : " < > |`, and `..` / absolute paths / empty path segments are still rejected, so directory traversal protection is unchanged. Skill names were relaxed the same way — ASCII is still limited to lowercase letters, digits, and hyphens (the agentskills.io spec), non-ASCII letters such as Chinese can be used directly in skill names, but spaces, dots, and path separators are not accepted.
- Fixed Chinese archives created by Windows compression tools having garbled file names or being rejected entirely after extraction: such zips do not set the UTF-8 flag and write file names in GBK; they are now decoded with GBK as a fallback before path validation. Quoted frontmatter such as `name: "中文技能"` also yields the skill name correctly.

### UI

#### Added

- The findings page gains an "All findings" flat view, set as the default, switchable with the original "Grouped by task" view through header tabs: the grouped view needs each task expanded to see its findings, which is a detour when you just want to scan the list across tasks. The flat view is one big cross-task table (10/20/50/100 per page) that behaves exactly like the grouped view — tick to export, expand inline to see evidence and the detailed report, edit name/category/severity inline, change handling status, deepen, delete. Both views share the statistic cards and the filter bar, switching keeps the filters, and the current view and filters are stored locally; polling only hits the current view, and inline edits update both caches, so switching never shows stale data.
- The findings page gains a "By asset" view: an asset tree on the left (company → root domain / IP → subdomain → service → endpoint, with the company level only shown when assets actually belong to one), and on the right the findings under the selected node's whole subtree, sharing the same table and filters with the other two views. The tree only includes assets that have findings, with ancestor chains filled in as needed (when a finding is only attached to the deepest endpoint, the levels above are still assembled), and node counts are subtree aggregates deduplicated by finding; findings whose asset was deleted or that never had a linked asset go into the "No linked asset" bucket. Unlike the other two views, the asset view does not poll — it queries only when entering the view, changing filters, adding/editing/deleting findings on the page, or clicking the refresh button on the tree, since the left tree is a navigation structure and does not need recomputing every 5 seconds. Each tree level only shows the increment relative to the level above (subdomains drop the root domain suffix, services show `https :443`, endpoints show only the path), with the full value in the hover tooltip and the breadcrumbs; when there are too many nodes, the endpoint/service levels are dropped for the whole level with a notice, and counts still roll up to the upper level.

#### Fixed

- Fixed the conversation record being squeezed and partly hidden in conversation details on mobile: on narrow screens the conversation list collapses, giving the width to the record itself.

### Dependencies

#### Changed

- Upgraded norma v0.3.2 → v0.3.3: adds `Config.MaxTokensField`, so the output limit for OpenAI Chat Completions can be sent as `max_completion_tokens` instead (reasoning models only accept that key). The two keys are mutually exclusive and only one is sent; the default is still `max_tokens`.
- Upgraded `golang.org/x/mod` v0.37.0 → v0.40.0, fixing two dependency security alerts.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed

- Fixed the "Non-streaming" switch reverting to "Streaming" when reopened after saving (#69): the configuration list endpoint's DTO was missing the `streaming` field, so the response never returned it and the frontend fell back to the default streaming whenever it read `undefined`; the actual write and DB storage were fine, it just could not be read back. The field was added back to the DTO (without `omitempty`, since `false` must appear in the response too).
- The connection test now verifies that the model actually replies, and tests with the configuration's real send/receive mode (#65): it used to check only whether HTTP succeeded, so a request that went through but got zero reply from the model (thinking burned the budget / the body was swallowed by a safety policy / a compatibility layer dropped `content`) still reported "Connection successful", inconsistent with "no reply" in conversations; and the test always used streaming, so for a non-streaming configuration it actually tested a different channel. Now an empty reply fails outright, a success shows the model's reply in the message, and the current streaming switch is passed into the test, so "test passed" matches "conversations work".

### Agent

#### Changed

- `list_facts` now uses pagination + keyword filtering, so a single call no longer blows up the context when there are many facts (#74): returns the latest 20 by default, supports `limit` (max 100) / a `before` cursor / a `q` summary keyword, and returns `{facts, total, has_more, next_before}`; overly long summaries are truncated by length (the full text is still available through `node_detail`). The worker / planner prompts were updated to the pagination semantics. Old databases get a one-time migration that writes the new parameter schema into the tool catalog table (`SeedTool` only inserts the first time; otherwise the tool management page shows "No parameters").

### Tools

#### Added

- Tool call count statistics on the "Tool executions" page (#72): the "Statistics" button in the toolbar opens a dialog showing call count, share, and failure count per tool (sorted by count, descending); it uses the list's task / keyword filters, covers the whole result set rather than the current page, and fetches only when the dialog opens.

### Dependencies

#### Changed

- Upgraded norma v0.3.1 → v0.3.2: fixes reasoning/refusal being silently dropped from OpenAI-family responses (Responses adds `reasoning_text`), dedupes the three reasoning field names, adds bounded retries for empty responses (zero content blocks), and fixes gateway 400s caused by compaction boundaries splitting tool pairs (also fixing existing orphans already baked into transcripts).

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### Added

- Each LLM configuration can switch between streaming and non-streaming (streaming by default): on uses streaming SSE; off uses true non-streaming (`stream:false`, returning the full JSON at once), which works around some gateways' poor SSE implementations (empty frames, dropped thinking fields), at the cost of losing live progress and live token counts while running. worker / planner / mainagent / chat / goals all read the value dynamically from the currently active configuration; the three provider wrapper layers `llmpool` / `llmrec` / task runtime are all compatible with non-streaming; `llm_profiles` gains a `streaming` column, added to old databases with `ALTER` (default `true`, transparent for old configurations).
- LLM configurations in the OpenAI Responses API format: each configuration gains a third format `openai-responses` (calling `POST /v1/responses`), alongside Chat Completions / Anthropic; `BaseURL` normalization, default model `gpt-5`; the `format` constraint of `llm_profiles` adds `openai-responses` with an idempotent migration for old databases; the frontend format drop-down gains "OpenAI (Responses API)". Dependency upgraded to norma v0.3.1 (including the `reasoning_content` echo fix).
- Recording raw LLM request/response HTTP: the real wire body is captured at the HTTP transport layer, keeping tool schemas, `tool_use` blocks, and raw SSE frames that the normalized view does not show; multiple attempts from norma's internal retries are each kept; `llm_records` gains `raw_request` / `raw_response` columns, added to old databases with `ALTER`; the recording page's detail panel adds a "Raw" view toggle and request/response copy buttons (with an `execCommand` fallback for non-secure contexts).

### Conversations

#### Changed

- Multi-select in the conversation list is now a "Multi-select" mode toggle: the default list no longer shows a checkbox on every row (cleaner look), and the header becomes "N total + Multi-select" button; clicking "Multi-select" enters selection mode (checkboxes, select all, bulk delete), "Done" exits and clears the selection, and after a fully successful bulk delete it exits automatically (if anything failed it stays in selection mode for retrying); single rename / pin / delete still go through each row's ⋯ menu.

### UI

#### Changed

- Improved task management, conversation actions, and traffic viewing (#57): the task list's column sort preference is persisted, inline rename is triggered directly by an icon (no longer through a menu), and sheet interactions and traffic viewing details were refined.

#### Fixed

- Worker asset tags show only domains and IPs, and handle empty tags correctly.

### Agent

#### Added

- The planner adds a "quantitative acceptance check" when judging goals: when a goal has a quantifiable acceptance condition (asset test coverage reaches X%, obtain N flags, gain some privilege), the measured values in `graph_overview` (`coverage.pct`, finding counts, etc.) must be checked before `prove_goal`; if the measurement falls short, `prove_goal` is forbidden and intents must be dispatched to close the gap instead, and the goal must not be marked met early on the grounds that "the main part is done / it is mostly achieved". Fixes goals that required 100% coverage being judged complete at a measured 40%.

#### Changed

- Rewrote the criteria for "0 intents this round" in the planner prompt: the original text described 0 intents as "the most common and most important principle", which made the planner stop too early while the goal was not achieved and untested surface remained in scope. Now 0 intents should only be produced in two specific cases — ① every direction considered is already covered by open/running/recent_done intents; ② the next step depends on the output of a currently running work item that has not appeared yet (wait for it to finish and plan on the next wake-up after the graph updates). A reverse constraint was added too: when there really is a new uncovered direction that does not depend on running work, or the goal is not achieved and untested surface remains, do not stop because "0 intents is common".
- Strengthened the "evidence threshold for negative conclusions" in the worker prompt: for negative conclusions such as "not injectable / port closed / no login entry" that could make the planner abandon a whole direction, the reasonable techniques within the intent (other encodings/parameters/paths/methods) must be exhausted before concluding; if the techniques were not exhausted or the evidence is weak, always mark `confidence=inferred`, avoiding a hasty `observed` negative sealing off a whole route (wrong negatives early in a task especially derail the direction and rarely self-heal).
- The planner / worker default prompt changes above are appended as new versions and made current through one-time migrations (`reseedPlannerPrompt` / `reseedWorkerPrompt`, each guarded by a settings flag); old versions stay in the version history, and users with customized prompts can restore them from the version records.

### Operations

#### Added

- New `reset-password.sh` to reset the admin password (the username is fixed as `ARTEX`): supports both local and docker deployments, with connection info specified explicitly or read automatically from `--dsn`/`$ARTEX_PG_DSN`/`config.json`; it uses `pgcrypto` inside the database to generate a bcrypt hash compatible with backend login and writes it back to `settings.auth.password_hash`, with no service restart needed after the reset. The password is passed through an environment variable, never appears in the process argv, and is escaped to prevent injection.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### Added

- After ticking several tasks in the task list, their category can be changed in bulk; the target category supports "Uncategorized" to move tasks out of their current category; the whole batch is written in one transaction, and the only possible failures are tasks deleted after being ticked.

#### Changed

- "Task category" when creating a task changes from a drop-down to a searchable input: typing filters existing categories, a name not in the database is created and selected immediately on Enter (or by clicking "Create" in the drop-down), and the selection is shown as a removable tag; still limited to a single category.

### Conversations

#### Fixed

- Fixed sending a message being blocked by "LLM not configured, cannot chat" when the conversation had an LLM configuration selected but there was no globally active configuration. The pre-send check only looked at the globally active configuration, while actual execution prefers the configuration selected for the conversation, so the two resolution paths were inconsistent; they now use the same logic (conversation/agent binding first, global fallback).
- The message when chatting is unavailable is now specific to the state: with no configuration at all it says "Add a configuration", with configurations but none active it says "Activate one or assign a configuration to this conversation", instead of always showing "Not configured", making the cause easier to locate.

### Agent

#### Added

- For tasks whose goals are all achieved, intents dispatched by the main agent through `add_intent` can be claimed and executed directly by workers and stop when done: the planner does not run in this case (a task with no open goals does not enter planning), so it cannot re-judge the goals as achieved and cancel the freshly dispatched intent; once the frontier drains, the task returns to completed. Before dispatching an intent, the main agent asks the user, based on the intent's content, whether to register it as a formal goal; if registered, the task resumes normal autonomous planning.

#### Changed

- When `step_ids` of `get_worker_trace` / `get_task_worker_trace` exceeds the per-call limit (5), it no longer errors out but returns the full content of the first 5 steps, and the result reports through `returned_step_ids` / `omitted_step_ids` / `notice` which ones were fetched and which were not, with the hint "no need to fetch more if this is enough". Duplicate and invalid ids are deduplicated and removed before counting.

#### Fixed

- The task list is now sorted by task id descending (newest first), replacing the previous sort by creation time. Several tasks created at the same moment had identical timestamps and an unstable order, which, combined with polling every 10 seconds and random iteration of an in-memory map, made them swap places in the list frequently.

### Assets

#### Fixed

- Fixed company ownership recomputation being broken by a single dirty row: when `assets.ip` held a hostname (written by an agent or the asset API), `a.ip::inet` threw `22P02`, making adding/editing company asset scopes and deleting companies all fail and roll back. The safe conversion `try_inet()` is now used, skipping invalid values instead of aborting the whole statement.
- Unparseable `ip` values are no longer skipped silently: after a company scope is saved, the UI states clearly how many assets have an `ip` that is not a valid IP (with the specific ids and values), warning that these assets will not be attributed by IP/CIDR rules; the same information is also written to the server log, covering paths with no frontend response (deleting a company, scopesentry sync, agent writes).
- IP/CIDR matching for the task test scope also switched to `try_inet()`, replacing the previous regex guard that only checked the character set (hostnames made entirely of hex letters, like `abc.def`, still slipped through and triggered the same error).

#### Changed

- The `ip` field of `ip`, `service`, and `endpoint` assets no longer accepts hostnames: `insert_assets` and the asset API return an error per entry with its `index`, giving the fix directly (use `type=subdomain` with `domain`, or resolve the A/AAAA record first), so the agent can correct and reinsert by itself. Other assets in the same batch are not affected and are stored as usual.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### Added

- Running workers can be paused, resumed, and cancelled individually; pausing keeps the intent, facts, and findings, and cancelling transactionally cleans up the current intent and its direct outputs after execution exits.
- Added the `paused` intent state, an execution fence, and named termination reasons, preventing late blackboard writes after a pause, cancellation, or task deletion.
- Added an optional per-task concurrency limit; creation, resume, finding deepening, and queue backfill all go through one persistent FIFO admission path.

#### Changed

- The default wall-clock duration of a single worker run changed from 600 seconds to 1200 seconds; after a timed-out task is revived, the clock restarts on the next real execution.
- The real execution state of workers, the planner, and the main agent now drives the task status badge, fixing workers still showing "Idle" while running.
- Worker conversations keep per-item controls and the current conversation's token summary, and the model name moved to the right of the current conversation title.

#### Removed

- Removed the worker multi-select, select-all, and bulk pause/continue UI, along with the worker bulk control API and mock contract.
- Removed the token badge and tooltip from worker list rows; the underlying full token ledger and task aggregation endpoints remain.

### LLM

#### Added

- Tasks support an ordered LLM configuration chain; when a provider quota shortage is clearly identified, it switches to the next configuration automatically and persists the current configuration, exhaustion state, and error summary.
- Running and paused tasks can edit the full configuration chain, reorder it, and switch the current configuration manually, effective from the next LLM call.
- Automatic switches, manual switches, and full chain exhaustion are written as structured system activity and pop up deduplicated alerts through the task activity feed.
- Added a task role model resolution endpoint that resolves the configuration and model used by the next call of the main agent, planner, and worker in one place.
- Added an optional global LLM pool, with configurable call order, fallback on specified model failure, health status, cooldown recovery, and manual reset.
- Added an always-on LLM usage ledger that aggregates input, output, and cache tokens by task, conversation, model, and configuration.

#### Changed

- Goal decomposition, the planner, workers, and the main agent share a task-level LLM runtime (resolution precedence below).
- Failover only responds to clear quota, balance, or billing errors; ordinary rate limits, authentication, network, server-side, and context errors do not trigger a wrong switch.
- The LLM configuration page became a list of configuration cards with editing in a right-side drawer, and provides the pool round-robin order, priority, exclusions, health status, and recovery actions.
- Configuration chain exhaustion information wraps on narrow screens; the current model changed from an icon in the conversation list to a text label next to the current conversation title with a full tooltip.
- The order in which each role in a task resolves its LLM changed to "agent binding → task configuration chain → global": a role with an explicitly bound model always runs on that model, only unbound roles fall back to the task chain, and if the task chain is empty they fall back to the global configuration.
- An empty outbound proxy means a direct connection, no longer falling back to the `HTTP_PROXY`/`HTTPS_PROXY` environment variables (an explicit `ARTEX_LLM_PROXY` is unaffected); proxy input supports `socks5://user:pass@host:port` with credentials.

#### Fixed

- Transient streaming failures before commit (before any output was produced) are retried safely with backoff on the same provider, significantly reducing cases of "stopped after one or two tools, no summary, final state `model_error`"; quota exhaustion, context too long, and deterministic 4xx errors are not retried and are still handled by failover and compaction recovery respectively.

#### Removed

- Removed the LLM icon marker from the conversation list, avoiding confusion with worker status icons.

### UI

#### Added

- Tasks support creating, renaming, deleting, and filtering global categories; a category can be chosen directly when creating a task.
- Added global task template CRUD and a right-side management drawer; creating a task can load a preset description and goal, or save the current content as a template.
- Creating a task supports linking multiple source tasks and multiple company asset scopes; company scopes automatically recognize domains, URLs, IPs, CIDRs, ICP numbers, and company keywords from a single multi-line text box.
- Task test assets can be added and removed in real time, recording their source (manual, company, inherited, or discovered by an agent); the current test assets and a source summary are shown next to the worker conversation title.
- The findings page groups by task with independent pagination within groups, and supports writing a description for a finding and creating a high-priority deepening intent.
- Conversations can be renamed, pinned, unpinned, and deleted; the task list supports bulk pause and continue for the current page.
- Traffic details and tool execution details moved into a right-side drawer; HTTP messages fill in `Host` and highlight the request line, status code, headers, JSON, and markup bodies.
- The task list's action column gains "Details" and "Pause/Continue" buttons; the conversation send key is configurable in system settings (`Enter` / `Cmd+Enter`, etc.), and the web search section gains a proxy input.
- The badge to the right of the conversation title now shows the LLM configuration name, with the model ID moved to a hover tooltip; tasks can have an optional name, and the task list gains a name column and search (falling back to the description when empty).
- The skills page adds usage counts, last used time, and missing dependency statistics, making it easier to find skills that are not taking effect or not installed.

#### Changed

- Task titles became focusable details links; task assets, company assets, finding task groups, and findings within groups use real server-side pagination, stable ordering, and exact totals.
- Creating a company now uses a right-side drawer, and scope entry is unified as multi-line text with real-time recognition, validation, and type preview.
- The main agent input box grows automatically, sends with `Enter`, inserts a newline with `Shift+Enter`, and avoids accidental sends during Chinese IME composition.
- Agent previews, task reports, and related details all use the shared Markdown rendering; delete confirmations, long errors, and mobile drawer widths were fixed consistently.
- The task template picker displays and searches by template name and submits the template ID; the system's original font size was restored, and the app version is shown uniformly as `0.3.3`.
- Task totals and current conversation tokens only highlight the input, cache read, and output numbers; the send button uses a simpler up-arrow icon.
- The test scope in the task overview shows company names instead of IDs.

#### Fixed

- Plain text containing dots is no longer misidentified as an ICP registration number.
- Fixed name collisions in the variable catalog with global runtime variables (such as `{{.Now}}`), which made the agent editor's variable list render duplicate keys.

#### Removed

- Removed the separate "Enter" button on task cards; clicking the task title opens details everywhere.
- Removed the dashboard "New task" button, the Logo URL field when creating a company, and the finding count badges at the top of the findings page and in task groups.
- Reverted the style that enlarged the global font by 10%, and removed the worker checkbox, model icon, and per-row token statistics from the conversation list.

### Agent

#### Added

- New tasks can link several existing tasks directly, inheriting in real time and read-only the goals, facts, findings, completed intents, asset scope, and blackboard context of the direct source tasks.
- Blackboard read tools support on-demand queries of source task nodes, facts, findings, and execution traces; inherited nodes carry a source marker and all write tools refuse to modify them.
- The company scope tool supports domains, URLs, IPs, CIDRs, ICP numbers, and company keywords; keywords only serve as scope hints for the agent and do not take part in automatic asset ownership.
- Finding deepening creates a high-priority manual worker intent in the original task with asset anchors and a `derived_from` edge.
- The overview gains a "Goal management" card for viewing, adding, editing, and deleting goals manually; adding or editing notifies the planner and revives the task, while deleting hard-deletes the goal node (cascading to edges and anchors) without reviving it.
- The main agent gets the `steer_work` tool by default, which injects course-correction instructions into a running worker in real time without interrupting it or losing progress (still checking that the intent belongs to this task).

#### Changed

- Source task intents do not enter the new task's frontier; the new task keeps using its own exploration, execution queue, working directory, and conversation history.
- Deleting a running intent no longer destroys data: it is stopped as `stopped`, a deletion reason is required, the reason is attached to the intent as a fact and written into the payload, and the planner is made aware through a `cancelled` trigger (keeping the intent content and reason).
- Main agent conversations are now driven by the server activity feed, and the input can be recovered when sending fails; opening a conversation sticks to the bottom automatically and keeps the last reply visible after details lazy-load.
- Pausing a task terminates the current main agent, planner, and worker calls, but does not prevent the user from starting a new main agent orchestration conversation while paused.
- Cancellation, shutdown, and stream interruption all keep the real termination reason, generated content, run rounds, duration, tokens, and tool calls that did not return.

#### Removed

- Removed the main agent client's optimistic message echo, avoiding duplicate messages and cross-conversation streaming under pauses, failures, or concurrent activity.

### Constraints

#### Added

- The goal decomposition phase first extracts operating constraints (allow/deny) and then decomposes goals, and the main agent can add more at runtime; constraints are injected into the planner and worker system prompts with the highest priority, and after debiasing, diversity and breadth exploration explicitly yield to the constraints.
- The overview gains a "Constraint management" card and create/update/delete endpoints; constraint injection can be toggled separately for planner / worker (both on by default, read every round, effective immediately). The new table `task_constraints` uses `CREATE TABLE IF NOT EXISTS`, so old databases get it created automatically.

### Intercept

#### Added

- Beyond regex/string rules, command intercept adds model fallback approval: when no rule matches, the model makes an `ALLOW`/`ASK`/`DENY` semantic verdict, and the fallback action on failure and the action on approval timeout are configurable; the intercept page is split into "Intercept rules / Model configuration" tabs, and model verdicts are labeled with a `[model]` prefix and a reason.

#### Fixed

- Hardened parsing of the judge output so a correct `DENY` verdict is not mistaken for approval.

### Traffic

#### Changed

- Traffic recording now stores each whole exchange in SQLite (large bodies overflow into hash-deduplicated blob buckets), with a trigram full-text index on text bodies supporting arbitrary substring and Chinese search; deletion becomes a single SQL transaction, going from hours to milliseconds without pausing recording in the meantime. Adds streaming download of large bodies and a full-text parameter for `traffic_search`; old file tree data needs no migration and can still be read, searched, and deleted.

### Tasks and assets

#### Added

- Creating a task gains an "Asset coverage" switch (on by default): when off, coverage is neither computed nor shown, the state graph only shows assets, `task_scope` does not accumulate automatically, and the related tools are removed from the planner / main agent; company linking is not affected by this switch.
- Each asset in `insert_assets` gains a `related` flag (default `true`): it only takes effect when coverage is on, and `false` stores the asset only in the shared asset store without counting it toward this task's coverage (e.g. side sites or unrelated assets found along the way).
- Company scopes and task test assets uniformly support recognizing domains, URLs, IPs, CIDRs, ICP numbers, and keyword text; domains/IPs can create or reuse global assets, and CIDRs/ICP numbers/keywords are kept as task scope context.

### Build

- `build.sh --release` can build Linux amd64/arm64, macOS amd64/arm64, and Windows amd64 in one go, producing zip release packages with `skills/`, the example configuration, and the README.
- Uses the Go linker to strip debug information and produces zip release packages; UPX is now explicitly opt-in, avoiding segmentation faults at startup of its self-extracting ELF in some Linux environments.
- The release workflow runs a startup smoke test on the Linux amd64 binary and ships `SHA256SUMS` with the release packages.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- Creating a task supports linking multiple existing tasks, inheriting in real time and read-only the facts, findings, completed intents, asset scope, and blackboard context of the direct source tasks; the new task still uses its own execution queue, working directory, and conversation history.
- Tasks support an ordered LLM configuration chain. When a provider quota shortage is clearly identified, it switches to the next configuration automatically and persists the current configuration, exhaustion state, and structured audit activity.
- Running and paused tasks can edit the LLM configuration chain, reorder it, and switch the current configuration manually; automatic switches, manual switches, and full chain exhaustion are announced on the task details page.
- Running workers can be paused, resumed, and cancelled. Pausing keeps blackboard data, and cancelling transactionally cleans up the intent and the facts, findings, and execution records it directly produced after the worker stops writing.
- Deleting a task can optionally also clean up the related assets, traffic, findings, and task files on disk, with a deletion barrier, concurrency protection, and auditable deletion statistics.
- Added an optional per-task concurrency limit; new tasks beyond the limit queue in FIFO order and start automatically when a run slot frees up.
- Added task asset pagination, company asset pagination, task category statistics, and the paginated intent mock contract.
- Added the `build.sh` single-binary build script, supporting static frontend export, resource embedding, cross-platform targets, and build version injection.

### Changed

- The explicit task-level LLM configuration chain coexists with the existing global LLM pool; an explicit chain keeps strict quota failover semantics, and without an explicit chain the agent binding and global configuration rules still apply.
- The main agent input box supports auto-growing multi-line input, sends with `Enter`, inserts a newline with `Shift+Enter`, and avoids accidental sends during Chinese IME composition.
- The planner, workers, and the main agent share a task-level LLM runtime; the task chain uses the smallest context window among candidate models as the safe compaction threshold.
- The task details page shows running, idle, and paused states based on real LLM call state; facts, findings, intents, asset references, and graph nodes from source tasks are uniformly marked with their source and kept read-only.
- Agent previews, task reports, and related detail views all use the shared Markdown rendering component.
- LLM model configuration uses cards and drawer interactions, and the model list scrolls independently inside the drawer.
- Pausing a task no longer blocks main agent conversations: main agent orchestration conversations are independent of task pausing, and new messages can still be sent while paused (pausing only terminates the round currently in progress).
- The default wall-clock duration of a single worker run changed from 600 seconds to 1200 seconds.

### Fixed

- Removed the optimistic echo in the main agent console in favor of rendering purely from server data, fixing messages getting jumbled or mixing in content from other conversations when pausing or when sending fails.
- Opening a main agent conversation scrolls to the bottom by default and shows the last reply in full: after the full content of the last reply lazy-loads and expands, it sticks to the bottom automatically instead of being pushed off screen.
- Fixed main agent conversations continuing to run after the task was paused, and the main agent not being stopped when the orchestration agent paused the task.
- Fixed the task status badge and action buttons getting out of sync when the task completed or the planner, workers, or main agent were actually running.
- Fixed possible late blackboard writes or leftover files between worker cancellation, task deletion, and concurrent writes.
- Fixed broken Markdown previews, long text overflowing delete confirmations, mobile widths, and misaligned buttons in some task details.
- Added named termination reasons to all agent cancellation paths; activity details can show who cancelled, the final state, run rounds, duration, token usage, and tool calls that did not return.
- Fixed a race where the parent context could override the named `shutdown` reason first when the backend shut down, and garbled Chinese activity summaries caused by byte-based truncation.
- Fixed cancellation events with partial streaming output losing the real termination reason, while keeping the content generated before cancellation.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/Autumn-27/ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/Autumn-27/ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/Autumn-27/ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/Autumn-27/ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/Autumn-27/ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Autumn-27/ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/Autumn-27/ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/Autumn-27/ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Autumn-27/ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/Autumn-27/ARTEX/compare/v0.3.1...v0.3.2
