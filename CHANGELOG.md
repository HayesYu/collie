# Changelog

All notable changes to Collie are documented in this file. Each release includes a dedicated **Client / API Changes** section for downstream web, desktop, and mobile client developers.

---

## v0.2.94 (2026-11-27)

### Highlights

- **Feishu Group Chat Automatic Recent Context on Direct `@Collie` (`internal/collie/feishu_ws.go`, `web/app.js`, `README.md`, `README.zh-CN.md`)**:
  - When directly `@`-mentioned in a Feishu group chat without quoting a parent message (`parentID == ""`) — across both allowlisted groups and read-only feedback groups (`feedback_chat_ids`) — Collie now fetches up to the last 10 messages via `im.v1.message.list` (`container_id_type=chat`, `sort_type=ByCreateTimeDesc`, `page_size=10`), excludes the trigger message itself plus deleted/empty messages, sorts chronologically, resolves sender names via existing caches, extracts readable text/summaries (`text`, `post`, `interactive`, `image`, `file`, `merge_forward`), enforces per-message (`<= 300` runes) and total (`<= 2500` runes, retaining newest) caps, and attaches a `[群聊最近上下文]` block to the Router prompt (keeping the Web UI user bubble concise for allowlisted groups, and embedding the context inside read-only feedback events).
  - Gracefully degrades without blocking delivery when the optional `im:message.group_msg` scope is absent or the OpenAPI request times out/errors, and skips history fetches whenever a parent message is explicitly quoted (`parentID != ""`).
  - Documented the optional `im:message.group_msg` scope in README and the Feishu integration setup modal.
- **Floating「小本本」Visual Centering, Click-to-Toggle & Top-Right Overlay Mutual Exclusion (`web/index.html`, `web/app.css`, `web/app.js`)**:
  - Positioned the desktop (`>= 1360px`) floating notebook optically within the right empty channel (55% across the free space between the main chat column and viewport edge, clamped cleanly against both edges) for balanced breathing room on `1440px` and `1728px+` displays.
  - Removed the redundant top-right `展开 / 收起` pill button from the notebook card (leaving only the minimal `×` close button); clicking the card header, empty area, or `+N` pills toggles compact/expanded directly while preserving direct task jump and artifact preview clicks.
  - Added automatic mutual exclusion between the desktop floating notebook and all top-right popovers/overlays (threads list `#collie-threads-pop`, search bar `#super-search`, and more menu `#collie-menu`): opening any overlay yields the notebook card without shifting the conversation column, and closing restores it automatically.
- **`#super-working` 0px Alignment, Scroll-Triggered 1px Top Hairline & Tiered `#crew` Compacting (`web/app.css`, `web/app.js`)**:
  - Aligned `#super-working` ("Collie 在想…") with the exact horizontal padding of `#super-thread` in both wide notebook mode (`#v-collie.has-notes`) and mobile (`<= 760px`), eliminating horizontal avatar drift (`0px` delta).
  - Replaced the 22px bottom gradient fog veil under `.collie-top` with a crisp `1px` low-opacity hairline (`rgba(0,0,0,0.06)` light / `rgba(255,255,255,0.07)` dark) that appears only once the chat stream scrolls (`scrollTop > 4`).
  - Added content-aware tiered compaction for `#crew` worker capsules (`.is-no-kind` hides secondary agent kind labels first, then `.is-tight` dynamically computes `--crew-chip-max` so overflowing strips stay crisp without slicing pills in half).
- **Whole-Card Clickable `.snap-card`, Jitter-Free `.reply-receipt` & Stale Composer `replyTo` Guards (`web/app.css`, `web/app.js`)**:
  - Converted `.snap-card` header rows into whole-card clickable buttons with subtle hover feedback and a quiet `›` chevron cue, removing the redundant `分屏预览` button box.
  - Refactored `.reply-receipt` (`<details>`) to a stable block wrapper with an `inline-flex` pill `<summary>` and a pure compositor-layer 140ms `opacity + translateY(-3px)` fade-in on `.reply-receipt[open] > .rr-body` (removing per-frame `::details-content` `block-size` reflows and `interpolate-size`), cutting open/close layout passes by ~81%, paint cost by ~66%, eliminating trailing-frame width bounce, and preventing mobile tap `.show-time` flicker.
  - Guarded `#super-composer` `replyTo` against phantom/stale quotes: auto-clears when the quoted entry is resolved/picked/dismissed or answered by a newer user message via SSE/another device, clears orphaned quotes when navigating away, opening the thread drawer, or picking another decision option while the main input is empty (preserving `replyTo` whenever a draft is typed), and supports `Escape` in `#super-input` to clear the quote.
- **Desktop Rendering & SSE Performance Overhaul (`web/app.css`, `web/app.js`)**:
  - Replaced top-level `body:has(#v-collie.has-notes) .toasts` with a lightweight `body.collie-has-notes` class toggled in `syncNotebookVisibility()`, cutting desktop SSE style-recalculation cost by 94%–96%.
  - Moved `--notes-reserve` / `--col-left` / `--col-right` statically onto `#v-collie`, scoped `--collie-top-h` writes to `#notes-pane` with an equality guard and `ResizeObserver` `borderBoxSize`, removed 260ms `.thread`/`.composer` `padding` transitions and the unused `.notes-expanded` class, and added `contain: layout style paint` on `.notepane` (cutting notebook toggle Long Tasks by ~90%).
  - Added `WeakMap` memoization for `inEntryReplyChain` / `resolveDecisionInfo` and `extractEntryArtifacts` / `renderEntryArtifacts`, hoisted `openFilePath` outside the `renderSuper` loop, skipped `/api/super/notebook` background refreshes when `!notesOpen`, added dirty-key dedup in `renderNotebook`, and excluded `#np-{tasks,files,timers}-list` from global `MutationObserver` scans.
- **Static Demo Sync (`docs/demo/`)**:
  - Regenerated `docs/demo/` via `scripts/demo.sh`.

### Client / API Changes

- **HTTP API / SSE / CLI / JSON schemas**: None.
- **Breaking changes**: None.

---

## v0.2.93 (2026-11-27)

### Highlights

- **Floating「小本本」Positioning, Fixed Width & Zero-Shift Vertical Expansion (`web/index.html`, `web/app.css`, `web/app.js`)**:
  - Anchored the desktop (`>= 1360px`) floating white-paper notebook directly alongside the right edge of the main conversation column (`32px` breathing gap, clamped against viewport edges) instead of pinning to the far-right window wall on ultrawide screens (`1600–1920px`).
  - Unified compact and expanded widths to a fixed `248px` (`0px` horizontal width jump) and removed expanded padding overrides so toggling compact/expanded causes **0px shift** on main chat bubbles and the composer.
  - Constrained expanded height to a restrained `max-height: min(48vh, 420px)` with smooth internal scrolling and a soft bottom fade, keeping the card in the upper-middle viewport well clear of the bottom composer.
  - Upgraded the tiny expand chevron into a clear `展开 ⌄` / `收起 ⌃` pill button (`>= 26px` height) and made the entire compact card body (title bar, whitespace, `+N`) click-to-expand while preserving direct artifact preview and `×` hide actions.
  - Replaced the header split-pane icon on `#open-notes-btn` with a crisp folded-corner lined notepad SVG matching the top-bar stroke style.
- **Cleaner Human-Readable Task Subtitles in `GET /api/super/notebook` (`internal/collie/notebook.go`)**:
  - Updated `memory/tasks/*.md` parsing to skip plumbing metadata lines (`Thread:`, `Workers:`, `Timer:`, etc.) and prioritize `Status` > `Next` > `Goal` (including multi-line indented items) > `Done`, stripping Markdown emphasis, backticks, and leading status emojis for clean one-line expanded subtitles.
- **Static Demo Sync (`scripts/demo/demo.js`, `docs/demo/`)**:
  - Regenerated `docs/demo/` with the updated notebook styling and interactions.

### Client / API Changes

- **`GET /api/super/notebook`**: Schema unchanged; `tasks[].note` now skips `Thread:`/`Workers:` metadata lines and surfaces clean `Status` / `Next` / `Goal` prose.
- **Breaking changes**: None.

---

## v0.2.92 (2026-11-27)

### Highlights

- **Seamless Borderless Top Bar, Transparent `#crew` & Ghost `派活 · 默认` (`web/index.html`, `web/app.css`, `web/app.js`)**:
  - Removed hard top/bottom divider rules from the Collie chat header, `#crew` strip, and thread drawer header, blending them seamlessly into the page surface with a gentle bottom fade veil.
  - Switched `#crew` worker capsules to a restrained transparent fill with a crisp 1px hairline border and container-query (`@container collie-top`) width adaptation when side drawers narrow the main column.
  - Lightened the bottom composer's default `派活 · 默认` trigger into borderless ghost gray text at rest, displaying a subtle neutral capsule only when a single-message override is active.
- **Top-Right Floating White-Paper「小本本」(`internal/collie/notebook.go`, `web/index.html`, `web/app.css`, `web/app.js`)**:
  - Added a read-only `GET /api/super/notebook` endpoint aggregating ongoing items from `memory/tasks/*.md` (falling back to live workers), today's generated deliverables (`.html`, `.csv`, `.tsv`, `.md`, etc.), and upcoming enabled timers.
  - On wide desktops (`>= 1360px`), floats a compact white-paper card (`~232px` wide, zero footer chrome) docked at the top-right showing 3 single-line ongoing items, 2 latest today's artifacts (clickable to preview directly), and 1 nearest timer with `+N` overflow hints; one-click expand unfolds all items with status notes, timestamps, and gentle internal scroll without overlapping the conversation.
  - Optically centers the chat column on wide viewports, automatically yields at `< 1360px` or when opening a right thread/file/HTML drawer (restoring cleanly on close), and stays completely absent from mobile top chrome (accessible on demand via the header icon as a bottom drawer).
- **Decision Card Silent Receipt & Inline「补充一句」(`internal/collie/store.go`, `super.go`, `superapi.go`, `web/app.js`, `web/app.css`)**:
  - Clicking an `--opt` choice now sends `pick: true` and collapses the decision card in place into a sleek collapsible `<details class="reply-receipt">` (`✓ 已选：… · HH:MM`) without cluttering the chat stream with a redundant black user bubble; Collie's follow-up reply carries a quiet quote strip (`你选择了「…」`) linking back to the decision.
  - Clicking「补充一句」opens an inline single-line input; only submitting custom text emits a normal user message bubble while recording `✓ 已补充：… · HH:MM` on the original card.
- **Light Deliverable Snapshot Cards, Sandboxed HTML Drawer & Native `.csv` / `.tsv` Table Preview (`internal/collie/cli.go`, `super.go`, `web/app.js`, `web/app.css`)**:
  - Added repeatable `collie reply --file <path>` support and light deliverable snapshot cards in Collie messages for `.html` / `.csv` / `.tsv` outputs.
  - Opening `.html` artifacts now launches an aligned (`60px` header, 0px step) desktop right split drawer or mobile `93dvh` bottom sheet hosting an opaque-origin sandboxed `<iframe sandbox="allow-scripts allow-forms allow-popups allow-modals allow-downloads" referrerpolicy="no-referrer">` (`srcdoc`), with one-click widen and open-in-new-tab controls.
  - Added native RFC 4180 `.csv` and `.tsv` table rendering in both the file preview drawer (`#file-view`) and wide reader (`#/file`), supporting quoted delimiters, escaped `""`, embedded multi-line cells, UTF-8 BOM stripping, sticky headers and row numbers, horizontal scrolling, safe 5,000-row cap with 100-row pagination, and a top-right `表格 / 原文` toggle alongside Download and exact raw Copy.
- **Memory Tab Dream Summary Slimming (`web/app.js`, `web/app.css`)**:
  - Collapsed the top Dream status summary in `自动化 → 记忆` into a single truncated muted plain-text line (`最近做梦 · … · 展开详情`), keeping the full rendered dream journal one click away without pushing instructions below the fold.
- **Static Demo Sync (`scripts/demo/demo.js`, `docs/demo/`)**:
  - Added demo `/api/super/notebook`, RFC 4180 `.csv` preview fixture, and silent decision-card receipts, and regenerated `docs/demo/`.

### Client / API Changes

- **`GET /api/super/notebook` (new, read-only)**: Returns `{ date, tasks, artifacts, timers }` summarizing active `memory/tasks/*.md` items (or live workers), today's deliverable files, and sorted upcoming enabled timers.
- **`POST /api/super/say`**: Accepts optional `pick: boolean` (when `true` with `re` pointing to a Collie `--opt` entry, records `Entry.pick` in place without emitting a user bubble and still triggers the router with a `你选择了` quote context).
- **`POST /api/super/reply` & CLI `collie reply`**: Accepts optional `files: string[]` (repeatable `--file <path>` flag on `collie reply`) to attach deliverable file paths rendered as light snapshot cards.
- **`Entry` schema (`GET /api/super`, SSE `super`, `GET /api/super/earlier`)**: Adds optional `pick?: { state: "picked" | "custom" | "dismissed", text?: string, at?: string }` and `files?: string[]`.
- **Breaking changes**: None.

---

## v0.2.91 (2026-11-27)

### Highlights

- **Memory Tab Full-Text Search & Highlighting (`web/app.js`, `web/app.css`)**:
  - Added a minimalist search bar at the top of `自动化 → 记忆` with instant filtering, a one-click clear button, IME-safe `Enter`/`Escape` handling, and a live match summary (`N 项 · M 处`).
  - Searches across titles, paths, and full Markdown content for `你的约定`, `进行中的事`, `笔记`, `已结束`, `做梦日志`, and `最近说过的话`; auto-expands matching `<details>` sections (restoring prior open/closed state when cleared), displays per-card hit counts and highlighted context snippets, highlights matched ranges in place via the CSS Custom Highlight API (`::highlight(mem-search)`) with a `<mark>` fallback, and renders a clean empty state with a quick reset button when nothing matches.
- **Explicit vs. `(inferred)` Instructions Split & One-Click Correction**:
  - Parsed `instructions.md` in read-only preview into two calm monochrome groups — `你亲口说的` (explicit rules) and `Collie 从对话中推断的` (entries tagged `(inferred)`, supporting prefix/suffix ASCII and full-width variants and multi-line sub-bullets) — without modifying `instructions.md` format or Agent/Router behavior.
  - Added lightweight inline actions on every item (`编辑` jumps into the raw Markdown editor with the corresponding line pre-selected; `删掉` on inferred items removes only those exact lines and saves back to `instructions.md` immediately with a 12-second inline `撤销` recovery bar).
- **Top Dream Status Card Summary & Inline Expand**:
  - Surfaced a concise summary of the latest `dreams/*.md` journal (lead headline + up to 3 bullet points) inside the top Dream status card alongside last/next dream timestamps, with a one-click `查看完整日志` / `收起日志` toggle to inspect the full rendered dream journal inline.
- **Static Demo Sync (`scripts/demo/demo.js`, `docs/demo/`)**:
  - Updated demo memory fixtures with sample `(inferred)` instructions and multi-day dream logs and regenerated `docs/demo/`.

### Client / API Changes

- **HTTP API / SSE / CLI**: None.
- **Memory file format (`memory/`, `dreams/`)**: Unchanged; all enhancements are read-side UI rendering plus existing `POST /api/super/memory` writes.
- **Breaking changes**: None.

---

## v0.2.90 (2026-11-27)

### Highlights

- **Feishu `merge_forward` Image/File Auto-Download & Readable Sender Labels**:
  - Fixed `code=234003` ("File not in msg") failures when downloading images nested inside `merge_forward` messages (`internal/collie/feishu_ws.go`): sub-message image downloads now fall back across enclosing `merge_forward` container `message_id`s and then `im/v1/images/:image_key` so forwarded screenshots land cleanly under `附件：`.
  - Added safe automatic file attachment downloads (`case "file"`) for standalone messages, `merge_forward` sub-messages (with outer-container fallback), and quoted parent files, enforcing a 25 MiB per-file limit, per-forward count cap, and strict filename/path-traversal sanitization (`0600` temp-dir storage).
  - Replaced masked raw `ou_…` identifiers inside expanded `merge_forward` transcripts with readable labels: allowlisted owners render as `你`, resolved contacts use their display name, and remaining senders receive stable per-forward labels (`成员 A`, `成员 B`, …).
- **Feishu Configured-State「更换机器人」Light Entry**:
  - Added an inline「更换机器人」action in the configured Feishu modal's「应用凭据」section header (`web/app.js`, `web/app.css`), opening the unified one-click wizard (`新建应用` with Border Collie persona presets or `绑定已有应用`).
  - Swapping bots is strictly atomic (`internal/collie/feishu_onboard.go`, `internal/collie/feishu_ws.go`): credentials (`app_id`, `app_secret`, `region`) and the WebSocket session are replaced only after QR confirmation, preserving existing `allow_open_ids` and `feedback_chat_ids` (prepending the newly confirmed app-scoped owner `open_id` when switching apps), while cancelling, denying, or expiring leaves the active bot completely untouched.
- **Dispatch Default Clarity & Collapsible Bottom-Bar Override Capsule**:
  - Clarified `偏好设置 → 派活默认` copy to explicitly state it serves as the fallback when no task-specific routing preference is stored in memory.
  - Collapsed the main composer's bottom-bar dispatch chips (`web/index.html`, `web/app.css`, `web/app.js`) into a restrained `派活 · 默认` pill when no single-message override is active (with hover/ARIA description summarizing memory-first routing and current fallback), expanding on click to override machine/agent/model/effort and showing the active override summary alongside a one-click `恢复默认` button when overridden.
- **`herdr` Restart Self-Healing Without Manual Approval**:
  - Hardened `herdr` connection and supervisor recovery (`internal/collie/herdr.go`, `fleet.go`, `node.go`, `server.go`, `super.go`): concurrent callers share the per-socket boot-wait window, previously connected sockets wait briefly through daemon restarts, and idempotent read RPCs retry once across mid-flight EOF/reset while mutating RPCs never retry.
  - Guarded supervisor ticks and launch-metadata pruning with `MachineHerdrOK` so transient `herdr` outages never falsely mark healthy workers `gone` or clear metadata; upon local `herdr` recovery, Collie automatically wakes a previously alive router and auto-confirms trust dialogs for the router and bypass-enabled workers without manual intervention.
- **Unquoted New Topics Preserve Unanswered `--opt` Cards**:
  - Updated pending-item resolution (`internal/collie/super.go`, `web/app.js`): sending a plain unquoted message (from Web UI or Feishu) no longer auto-clears unanswered `--opt` decision cards; cards resolve only on explicit option selection, an explicit quote targeting the card or its reply chain, or manual dismiss.
- **Restrained Neutral Unread Divider**:
  - Restored `.unread-sep` (`以下为未读消息` text and flanking gradient rules) in `web/app.css` and `docs/demo/` back to restrained neutral gray instead of orange `--attn`.

### Client / API Changes

- **`GET /api/super` & SSE `super` (`pending`)**: Unquoted user messages (`POST /api/super/say` without `re` or unquoted Feishu messages) no longer clear unanswered `--opt` pending cards; cards now clear only on explicit option click, an explicit quote referencing the card or an entry in its reply chain, or `POST /api/super/dismiss`. Payload schema is unchanged.
- **`POST /api/super/integrations/feishu/onboard`**: Now supports initiating an atomic bot swap when Feishu is already configured; active credentials and WebSocket connections remain unchanged until QR confirmation, and existing `allow_open_ids` and `feedback_chat_ids` are preserved on swap.
- **Breaking changes**: None.

---

## v0.2.89 (2026-11-27)

### Highlights

- **Restrained Visual Accent Hierarchy**:
  - Tightened `--attn` semantics across the Web UI (`web/app.css`, `web/app.js`) so everyday navigation stays calm monochrome while reserving the accent strictly for high-signal attention anchors (unread badges & divider, pending indicator dot, `>= 75%` router context warning, and error/alert states).
  - Neutralized active thread chrome — header `话题线 N` button, inline `↳ N 条回复 >` entry pill, message focus button, sidebar topic icon button, and selected topic popover row — to crisp light/dark monochrome gray with gentle hover elevation.
  - Raised the header router context gauge (`#router-ctx`) warning threshold to `>= 75%` (or active `handover`), keeping `< 75%` quiet muted gray instead of lighting up early.
  - Neutralized the bottom `待处理 / 钉住` tray bar background, border, label, and dismiss button to clean light/dark surfaces with hairline neutral borders, keeping only the single leading `--attn` indicator dot when pending items exist (and a neutral dot when all items are pinned).
  - Unified unread anchors (`.unread-sep`, `.count-pill.unread`, `.super-jump .sj-badge`), `.toast.attn`, and destructive confirm buttons onto `--attn` / `--attn-solid` tokens with zero hardcoded amber hexes.
- **4 Selectable Restrained Accent Colors**:
  - Added a 4-option accent color picker (`amber` warm amber default, `cobalt`, `pine`, `ink` pure monochrome) in `偏好设置 → 外观` directly below the light/dark theme switcher (`web/index.html`, `web/app.css`, `web/app.js`).
  - Persists per browser in `localStorage` (`collie.accent`), applies pre-paint to prevent FOUC, syncs across tabs via `storage` events, supports `radiogroup` arrow-key navigation, fits cleanly on a single row at 390px mobile width, and tunes text/border/surface tokens for WCAG AA (`>= 4.5:1`) contrast across both light and dark themes.

### Client / API Changes

- **HTTP API / SSE / CLI**: None.
- **Web browser preference**: Stores optional `collie.accent` (`"amber"` | `"cobalt"` | `"pine"` | `"ink"`, default `"amber"`) in `localStorage` and reflects non-default presets on `<html data-accent="...">`.
- **Breaking changes**: None.

---

## v0.2.88 (2026-11-27)

### Highlights

- **Feishu One-Click Bot Default Persona & 4 Circular Border Collie Avatars**:
  - Updated one-click Feishu/Lark bot creation defaults (`internal/collie/feishu_onboard.go`, `web/app.js`, `web/app.css`) with default naming (`{user}的边牧 · Collie`, falling back to `我的边牧 · Collie` when owner name is unavailable), homepage-linked description (`帮你放牧一群 Agent 的边牧｜盯进度、收结论、只把关键决策端给你 · https://lihuanyu0w0.github.io/collie/`, 74 chars — cleanly within Feishu CN's 120-char and Lark Intl's 240-char limits), and 4 high-contrast circular Border Collie neckerchief avatars (`cobalt`, `terracotta`, `pine`, `plum`) bundled locally (`web/avatars/`) and on GitHub Pages (`docs/avatars/`) for CORS-safe launcher upload.
  - Added an inline persona preview card, 4-swatch radio picker + randomize button, and collapsible name/description customization with live character counters in the Feishu onboarding wizard.
- **Team Light Collaboration (Route A) Confirmation-Gated Outbound Closure**:
  - **Feishu Read-Only Feedback Group Controlled Reply**: Feedback group events (`[群只读反馈 · …]`) now carry original `message_id` and confirmation-only reply instructions; added `POST /api/super/integrations/feishu/reply` and `collie feishu reply --chat <chat_id> --reply-to <message_id> --confirm "<text>"` strictly enforcing `confirm: true`, `feedback_chat_ids` allowlist membership, mandatory `reply_to_message_id`, and cross-chat validation, sending plain-text quote replies with zero private Hub links.
  - **GitForge (`GitHub` & pluggable CLI providers) Controlled PR/MR Comment**: Added optional `ForgeCommenter` interface (`GitHubProvider.PostComment`, declarative `CLIProvider` `comment_cmd` with optional `stdin_body`), `POST /api/super/integrations/{kind}/comment`, and `collie forge comment --thread <provider>:<owner>/<repo>#<num> --confirm "<text>"` strictly gated on explicit human confirmation (`confirm: true`), plus updated `ROUTER.md` and `advise` event guardrails.

### Client / API Changes

- **Updated endpoint `POST /api/super/integrations/feishu/onboard`**:
  - Accepts optional persona fields in `create` mode: `name` (2–32 chars CN / 2–64 chars Intl), `desc` (≤120 chars CN / ≤240 chars Intl; defaults to Collie description with homepage link), and `avatar` (`"cobalt"` | `"terracotta"` | `"pine"` | `"plum"`, default `"cobalt"`). Out-of-range values return HTTP `400`.
  - Response (`FeishuOnboardView`, also returned by `GET /api/super/integrations/feishu/onboard`) now includes optional `name`, `desc`, and `avatar` echo fields.
- **Extended `GET /api/super/integrations` (`feishu.extra`)**:
  - Added `feishu.extra.onboard_presets` (`owner_name`, `default_name`, `name_template`, `fallback_name`, `default_desc`, `homepage_url`, `default_avatar`, `limits.cn`, `limits.intl`, `avatars[]`).
  - Added `supports_comment` boolean in GitForge `extra` (`true` for GitHub; reflects presence of `comment_cmd` for CLI providers).
- **New static embedded assets**:
  - Served at `/avatars/{cobalt,terracotta,pine,plum}.png` (and published at `https://lihuanyu0w0.github.io/collie/avatars/{cobalt,terracotta,pine,plum}.png`).
- **New endpoint `POST /api/super/integrations/feishu/reply`**:
  - Body: `{"chat_id": "oc_...", "reply_to_message_id": "om_...", "text": "...", "confirm": true}`.
  - Requires `confirm: true`, configured `feedback_chat_ids` membership, and non-empty `reply_to_message_id`; returns `200 {"ok": true, "kind": "feishu", "chat_id": "...", "reply_to_message_id": "...", "message_id": "..."}`.
- **New endpoint `POST /api/super/integrations/{kind}/comment`**:
  - Body: `{"thread": "<provider>:<owner>/<repo>#<num>", "repo": "...", "number": 1, "kind": "pr|mr|issue", "body": "...", "confirm": true}` (`thread` or `repo`+`number`).
  - Requires `confirm: true`; returns `200 {"ok": true, "provider": "...", "repo": "...", "number": 1, "comment_id": "...", "url": "..."}`.
- **New CLI commands**:
  - `collie feishu reply --chat <chat_id> --reply-to <message_id> --confirm <text|->`
  - `collie forge comment --thread <provider>:<owner>/<repo>#<num> [--provider/--repo/--number/--kind] --confirm <text|->`
- **Pluggable CLI provider manifest (`~/.collie/providers/*.json`)**:
  - Added optional `comment_cmd` object (`cmd`, `stdin_body`, `id_path`, `url_path`) supporting `{{repo}}`, `{{number}}`, `{{mr}}`, `{{pr}}`, `{{kind}}`, `{{body}}`, `{{user}}`.
- **Breaking changes**: None.

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
