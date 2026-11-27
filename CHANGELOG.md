# Changelog

All notable changes to Collie are documented in this file. Each release includes a dedicated **Client / API Changes** section for downstream web, desktop, and mobile client developers.

---

## v0.2.87 (2026-11-27)

### Highlights

- **Main-Chat Unread Anchor Reliability (3 P0 + 2 P1 + P2 fixes)**:
  - Fixed stale non-bottom `scrollByRoute` snapshots hijacking navigation back to `#/collie`: leaving `#/collie` now clears `activeCollieRestore`, and forward clicks into `#/collie` with unread messages (or snapshots older than the latest `readCursorId`) always prioritize `unreadAnchor: true`.
  - Fixed background tabs/apps silently jumping past unread replies and auto-marking them read on return: hidden tabs no longer force-scroll to bottom on incoming SSE unreads (including inside the initial 2.5s `ResizeObserver` window), arming `pendingUnreadAnchorOnVisible` to re-run standard unread anchoring on foregrounding.
  - Added natural read-cursor progression: replies sent from the topic focus drawer advance the read cursor when main chat is near bottom (or has zero unreads), and manually scrolling substantive unread messages completely past the viewport top (`rect.bottom <= top + 24px`) debounce-advances `readCursorId` to the last scrolled-past unread.
  - Kept `#unread-sep` (`sessionUnreadMarkerId`) fresh by automatically migrating stale markers (`<= readCursorId`) forward when new unread batches arrive.
  - Prevented delayed image loads from locking `fitsAtBottom` one-way to `"bottom"`: `"unread-auto"` re-evaluates `placeUnreadAnchor` as thumbnails (`img:not([src])` / async `<img>`) load within the 2.5s anchor window, flipping cleanly back to `14px` above `#unread-sep` and cancelling deferred read commits if unreads grow past one viewport; preserved full `activeCollieRestore.until` duration across SSE updates and widened `placeUnreadAnchor` programmatic-scroll protection to `240ms`.
  - Expanded `/api/super` entry window dynamically (from 200 up to 500 entries) when `last_read_entry_id` precedes the trailing 200-entry window.
- **Wave 1 Reliability & Input Hardening**:
  - **macOS `launchd` installer (`docs/install.sh`)**: Switched `ProcessType` from `Background` to `Interactive` to avoid macOS App Nap throttling; added `launchctl print` teardown polling (up to 5s) after `bootout` plus 4-retry `bootstrap`.
  - **Nightly Dream loop guard & restart recovery (`internal/collie/dream.go`)**: Nightly dream runs now advance `last_dream` even on failure to prevent 2-minute spin loops during the 3-hour window, and persist an `active_dream` SQLite KV marker so orphan `collie · dream` workspaces are automatically cleaned up if the Hub restarts mid-dream.
  - **Safari post-IME Enter guard (`web/app.js`)**: Added a 30ms `compositionend` timestamp guard across chat composers and custom Enter-submit inputs so Safari's confirming IME Enter keydown never submits prematurely.
  - **Hub one-click update idle guard (`internal/collie/updates.go`)**: Hub self-update now waits briefly (up to 8s) and claims an exclusive restart guard (`hubUpdating`) before restarting, preventing interruption of an active Dream, Handover, or Router turn.

### Client / API Changes

- **Dynamic unread lookback on `GET /api/super` and SSE `super` events**:
  - `entries` continues to return the trailing 200 entries by default, and now expands backward up to 500 entries (including up to 10 context entries before `last_read_entry_id`) whenever `last_read_entry_id` is older than the trailing 200 entries. Existing payload schema is unchanged.
- **Hub self-update busy guard on `POST /api/update` (local Hub without `?machine=`)**:
  - When an upgrade is available and Hub is busy with an active Dream, Handover, Router turn, or queued unpaused Router trigger for more than ~8 seconds, `POST /api/update` returns HTTP `409 Conflict` with `{ "error": "<localized reason>" }` instead of interrupting in-flight work. Clients should surface the error message and allow retrying once idle. Remote node updates (`?machine=<id>`) and up-to-date checks (`latest: true`) are unaffected.
- **Breaking changes**: None.

---

## v0.2.86 (2026-11-27)

### Highlights

- **Cursor CLI (`cursor-agent acp`) Worker Support (Phase 1, Worker-only)**:
  - Extracted the shared ACP v1 stdio bridge runtime (`internal/collie/acp_bridge.go`) from `doubao_bridge.go` (zero regression for Doubao) and added `collie _bridge cursor` (`internal/collie/cursor_bridge.go`).
  - Implemented protocol guardrails: skips `authenticate` and calls `session/new` or `session/load` directly (surfaces clear auth/credential errors on `-32000` / `-32603` without launching browser popups, sets `NO_OPEN_BROWSER=1`); places global flags (`--force`, `--approve-mcps`, `--model`) before `acp` without `--yolo` or `--effort`; extracts permission details from `toolCall.content` as well as `rawInput`; handles both hyphenated (`allow-once`/`reject-once`) and underscored (`allow_once`/`reject_once`) option IDs/kinds; detects `__ask_question_skip__` so interactive model questions still prompt the user even in bypass mode; consumes `session_info_update` for session titles; preserves `replaying`/`skipReplayLog` to prevent duplicate transcript entries on `session/load`.
  - Wired Cursor across agent detection (`cursor` → `cursor-agent`, never matching generic `agent`), `--list-models` ANSI-stripped parsing, launcher 3×2 grid, composer target selector, Worker settings, transcript streaming (`~/.collie/transcripts/cursor/<session_id>.jsonl`), session resume (`--resume`), and quota/auth error detection.
- **Homepage & Live Demo Polish**:
  - Revamped homepage (`docs/index.html`) and bilingual READMEs around Super Agent delegation with light/dark visuals.
  - Updated live demo (`scripts/demo/demo.js`, `docs/demo/`) to include Cursor models and remove the initial blocked terminal approval card on `codex-1` (`w3:p1`).

### Client / API Changes

- **New agent kind `cursor`** (input alias `cursor-agent`, display label `Cursor`, binary `cursor-agent`):
  - Appears in `machines[].agents` (`X-Collie-Agents`, `GET /api/state`, `GET /api/events` `state` SSE payload, `GET /api/devices`).
  - Accepted in `POST /api/agents` (`kind`), `POST /api/super/spawn` (`kind`), `POST /api/super/say` (`kind`), `POST /api/super/settings` (`worker_kind`), and `GET /api/transcript?kind=cursor&session=<id>`.
  - **Worker-only restriction**: `POST /api/super/settings` does not allow `router_kind: "cursor"` (or `"cursor-agent"`); it preserves the previous `router_kind` and returns HTTP `200` (same guard as `doubao`).
- **Models endpoint (`GET /api/models?kind=cursor`)**:
  - Returns parsed models from `cursor-agent --list-models` with explicit empty `efforts: []` on both the top-level payload and each `models[]` item, plus `default` when marked `(default)` / `(current)`. Clients should hide effort selectors whenever `efforts` is empty.
- **Parameterized model IDs accepted**:
  - Relaxed model name validation (`modelNameRe`) in `POST /api/agents`, `POST /api/super/spawn`, `POST /api/super/say`, and `POST /api/super/settings` to allow `=` and `,` (e.g. `claude-opus-4-8[context=1m,effort=high,fast=false]`).
- **Transcript & SSE streaming**:
  - `GET /api/panes/{id}/chat` streams standard `patch` (`user` / `assistant` / `tool`) messages from `~/.collie/transcripts/cursor/<session_id>.jsonl`; bridge reports `source: "collie:cursor"`, `agent: "cursor"`, and `resume_argv: ["collie", "_bridge", "cursor", ...]`.
- **Breaking changes**: None.

---

## v0.2.85 (2026-11-26)

### Highlights

- **Mobile Collie Upward-Scroll Smoothness**: Eliminated flicker, pagination rebounds, and image DOM rebuilds while scrolling up through long Collie conversations on mobile viewports.
- **macOS Bundled Codex Discovery (#5)**: Automatically discovers `codex` bundled inside `ChatGPT.app` on macOS nodes when no standalone CLI is installed and forwards `PATH` to `herdr`.
- **Recent Antigravity CLI Transcript Recovery (#6)**: Surfaces recent (last 7 days) exited Antigravity CLI conversations in the sidebar for read-only inspection and one-click resume in `herdr`.

### Client / API Changes

- **External sessions (`GET /api/external`)**: Now includes recent exited Antigravity CLI sessions with IDs prefixed `ag-cli-` (`live: false`).
- **External takeover (`POST /api/external/{id}/takeover`)**: Returns HTTP `409 Conflict` when a concurrent resume is already in flight for the same `ag-cli-*` session ID.
- **Breaking changes**: None.

---

## v0.2.84 (2026-11-26)

### Highlights

- **Faster Remote & External Codex Loading (#4)**: Made external session cache reads lock-free, added a 4-second snapshot fallback when remote SSE chat streams stall, and disambiguated multiple tmux Codex panes in the same directory.
- **Strict Default Worker Model Policy**: Tightened default `ROUTER.md` instructions so Collie strictly honors the user's configured default worker model unless explicitly overridden or unavailable.
- **Feishu Group Routing Guard**: Prevented non-group replies from falling back to group chats.

### Client / API Changes

- **New endpoint (`GET /api/external/{id}/snapshot`)**: Returns a point-in-time message snapshot for an external session (hub forwards requests for remote nodes), allowing clients to render immediately if the SSE stream stalls.
- **Breaking changes**: None.
