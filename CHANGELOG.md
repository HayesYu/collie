# Changelog

All notable changes to Collie are documented in this file. Each release includes a dedicated **Client / API Changes** section for downstream web, desktop, and mobile client developers.

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
