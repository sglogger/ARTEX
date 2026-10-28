# `/btw` validation record

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

An isolated PostgreSQL test database and data directory were used; real model credentials were injected only into the isolated test environment, never written into code or this record, and the product's default model was not changed. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

The real model conversations, returned objects, engineering assertions and Qwen's raw review text are stored in [validation-2026-09-10.json](validation-2026-09-10.json), which contains no API credentials.

## Engineering checks

| Scope | Result | Evidence |
| --- | --- | --- |
| Deep copy of structured messages and tool arguments | Pass | `TestCheckpointDeepCopyAndBoundaries` |
| Summary / compaction requests do not overwrite, publishing on complete replies and terminal state, half replies excluded | Pass | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Identity of the actual model pool member | Pass | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20-pair replay, budget trimming and over-limit errors | Pass | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main and side running in parallel, cancellation isolated in both directions | Pass | Blocking Provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| No tool execution, streaming / non-streaming, usage already incurred on failure | Pass | `TestServiceNoToolsAndUsageOnFailure` |
| Real norma ChatAgent + local Read tool, main transcript / activity isolation | Pass | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, streaming and non-streaming subtests |
| Persistence, pagination, idempotency, partial answers kept across restart | Pass | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Clear vs. late write races, parent resource deletion, version comparison | Pass | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker archive and restore, v1/v2/v3 | Pass | `TestSideTaskArchiveVersions` |
| Three parent endpoints, authentication, resource ownership, Worker logical deletion | Pass | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Side questions on a busy main conversation, independent SSE reconnect / disconnect, cancel, clear | Pass | `TestSideHTTPBusyIsolationClearAndReconnect` |
| Concurrency of 1 per parent conversation / 4 globally | Pass | Two `TestSideHTTP…` tests |
| Snapshot stored before submission, follow-up after restart, old conversations cannot forge snapshots | Pass | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Refuse to continue after the cached config is deleted or the model changes | Pass | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Cancel before archiving and wait for the final answer and usage to be stored | Pass | `TestSideTaskDrainPersistsBeforeArchive` |
| Usage and side-question attribution recorded only once when a streaming consumer cancels early | Pass | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Worker / deadline run contexts recovered automatically after restart keep publishing new snapshots | Pass | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Race check of the affected packages | Pass | Commands below |
| TypeScript and production build | Pass | `npx tsc --noEmit`, `npm run build` |
| Biome on the new frontend modules | Pass | `biome check`, 3 new modules |

With `ARTEX_PG_DSN` pointing at a separate disposable database, the automated checks can be reproduced (never point it at a production database):

```sh
go run ./tests/run.go -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

The full Go regression is not all green: two existing tests in the `server` package fail during temporary directory cleanup, both reporting `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

After exporting the source of the unmodified baseline above and rerunning the `server` package in the same isolated environment, both cleanup failures reproduce as well. The baseline run also showed a goal node count assertion failure in `TestCoreTaskLifecyclePG`; the final modified `server` regression does not have that assertion failure. Other packages pass, and the side-question tests and race checks of this change pass. Baseline issues were not counted as passing acceptance, and no existing assertion was changed to hide a problem.

The Next.js build prints the existing multiple lockfile / workspace root inference warning; the build completes and all pages are generated successfully.

## Browser checks

Using the Codex In-app Browser connected to an isolated local Go service and the Next.js dev server, the following manual-automated operations were completed on desktop and on a 390 × 844 narrow screen, checking screenshots and browser logs:

- Typing `/btw` while an ordinary chat is running shows the main content and the side question together; the desktop side panel works.
- Consecutive follow-ups; stopping a side question keeps the part already generated; the main flow continues.
- Closing the panel keeps the request running, and reopening restores the finished answer; after reloading the page an empty `/btw` restores the history.
- Input, buttons, history and close in the narrow-screen Drawer work, with no horizontal overflow.
- Clear uses a confirmation dialog; after clearing the history is gone while the main transcript and snapshot remain.
- Questions asked of the task MainAgent and two Workers, switching between them, kept agent labels and histories separate.
- A blocking local model fixture kept the Worker running; submitting `/btw` from the Worker's main input box and stopping the side question left the Worker showing live running and its own pause button, and the side question saved the partial answer.
- Browser error / warning logs are empty.

A controllable fixture is used to verify concurrency timing precisely, independent of a real model's output speed. During debugging, two Worker runtime checks did not form a valid concurrency window (the task had ended / the answer ended early); they were redone and passed after fixing the fixture, and those initial runs are not counted as valid passes.

## Real model conversations

`grok-4.6` was probed first via the OpenAI-compatible endpoint `http://127.0.0.1:12580/tingly/openai`. The probe returned HTTP 200 with model name `grok-4.6` and `READY` in 2.82 seconds. The first choice was available, so the Tingly `glm` and Zhipu `glm-5.3` fallback chain was not enabled; those two fallback services were not verified this time.

| Scenario | Actual result |
| --- | --- |
| Asking about the asset, goal and marker while the main conversation is running | Returned `redhaze.top`, read and summarize the homepage, `BTW-REAL-0910`; side question completed in 16.97 seconds |
| Asking about tool evidence after the main conversation finished reading the homepage | Correctly cited WebFetch 200, the curl redirect chain 301 → 302 → 200 and the page title; 7.24 seconds |
| Side question asks Bash to create a test file | Refused, target file not created; 7.74 seconds |
| A finished side question does not change the main context | Main transcript SHA-256 and main activity records unchanged; side-question tool executions are 0 |
| Follow-up after really stopping / restarting the Go service | The previous 3 side-question entries were kept, and the asset, marker and title were answered directly from the persisted snapshot without rerunning the main agent |
| New conversation using the non-streaming Grok config | Correctly answered the asset and `ATOMIC-0910`; usage returned and saved: input 11734, output 138, cache_read 11520 |

The main conversation of the asset case used WebFetch and Bash/curl to read the public homepage; the landing page is `https://id.redhaze.top/home`, whose title is the site's own Chinese-language "RedHaze Group · global group portal" title. Bash staged the response in a local test file; nothing was written to the remote side. That fact is verified separately from "the side question executed no tools".

Main transcript checksum: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage limitation:** Tingly's Grok streaming responses return no usage. A separate direct request with `stream_options.include_usage=true` returned HTTP 200, 12 data frames and 0 usage frames. So the 0 in the streaming test means the endpoint provides no usage and cannot be read as no billing. Non-streaming usage and the fixture's failure / cancellation usage are all saved correctly.

## Qwen review

Review model `qwen-flash`, OpenAI-compatible endpoint `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. It was given the first three real side-question conversations, the main conversation's tool evidence and the engineering assertions; it returned `verdict: accept` and `concerns: []`, judging the answers consistent with the asset, marker and page-read evidence and the side question's tool refusal compliant with the constraints. Review usage: prompt 6625, completion 312, total 6937.

This Qwen review did not cover the service restart and non-streaming tests added later. Qwen's summary of "no writes" is too broad: the main conversation's curl did create a local temporary response file, as recorded explicitly above. Concurrency, zero tool executions and transcript isolation are judged by engineering assertions; the model review only helps assess answer quality.
