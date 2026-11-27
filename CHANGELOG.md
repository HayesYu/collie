# Changelog

All notable changes to Collie are documented in this file. Each release includes a dedicated **Client / API Changes** section for downstream web, desktop, and mobile client developers.

---

## v0.3.11 (2026-11-28)

### Highlights

- **Unified Mobile & Tablet Worker Drawer (`web/{app.css,app.js}`, `docs/demo/`)**:
  - Removed the `<= 1000px` / `> 1000px` drawer bypass gates across the sidebar (`#live-list`, `#attention`), 54px icon rail (`#rail-live`), chat/event/approval/thread links (`openWorkerFromLink`), Memory `.mem-crew` chips, and `#/w/` deep links so mobile (`<= 760px`) consistently slides up the bottom sheet and tablet (`<= 1000px`) opens the right slide-over drawer while preserving `#wd-popout` for explicit full-screen inspection.
  - Added `openWorkerFromAnywhere` so tapping a worker from the mobile sidebar (`#/`) cleanly transitions to `#/collie` before opening `#worker-drawer`, and clicking worker chips inside Memory (`#/auto`) opens `#worker-drawer` directly over Memory for both live and ended workers.
  - Eliminated double scrim overlaps during notebook/drawer handoffs, added `overscroll-behavior: contain` scroll isolation on scrims and drawer panes, anchored `#wd-thread` / `#wd-log` and refit `wdTerm` on mobile keyboard resize (`setVH`), blurred focused inputs on drawer open/close, and aligned mobile composer controls and touch targets with safe-area insets.
- **Redesigned Peer Hub Modal & Elevated Popovers (`web/{index.html,app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Replaced nested Peer Hub sub-tabs with a clean single-level 3-tab layout (`好友 / 我的名片 / 添加好友`), surfacing a restrained amber pending indicator while hiding zero counts.
  - Decluttered the modal header and footer by moving raw direct addresses, invite codes, hub IDs, and two-step card reset confirmation into a collapsed native `<details>` disclosure (`连接与高级详情`).
  - Rendered real `/api/qr` PNGs on a warm-gray dark-mode QR plate (`#e5e5e1` with multiply blend) paired with click-to-edit inline nickname auto-save and a primary copy-invite CTA, and introduced custom top-layer popover pickers inside `#int-modal` (`#222229` elevated dark surface, mobile/pending trigger width-matching, narrow-row alignment, and full keyboard navigation).
- **Antigravity Subagent Isolation & Structured Subagent/Tool Rendering (`internal/collie/{agy.go,chat.go,desktop.go,external.go,fleet.go}`, `web/{app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Eliminated Antigravity subagent session cross-talk across two layers: SQLite metadata filtering (`nesting_depth=0`, `killed=0`, `parent_conversation_id=''`, `agent_name=''`, guarded via `PRAGMA table_info` for older schemas) and transcript line-0 `SYSTEM_MESSAGE` detection across `guessAgy`, `recentAgyCLI`, `agyDesktopRows`, and `locateExternal`.
  - Read immutable line-0 `created_at` from `transcript.jsonl` in `guessAgy` to eliminate Linux `brain/<id>` directory `mtime` drift, and guarded concurrent same-directory Collie workers/router using remembered pane names (`Herdr.Name`) and line-0 identity markers.
  - Parsed Antigravity double-encoded tool arguments into structured `SubagentCall` metadata (`invoke_subagent`, Claude `Agent`/`Task`, Codex `spawn_agent`, `swarm(add)`) plus readable summaries and expandable `detail` parameter blocks for generic tools (`generate_image`, `send_message`, `update_steps`, `schedule`, `define_subagent`, `manage_task`, `manage_subagents`, and arbitrary JSON tools), rendered as restrained monochrome collapsible cards (`details.tool.subagent`) with legacy raw-JSON fallback.
- **Sidebar/Rail Polish, Tablet HTML Drawer Stability, Ended-Worker Auto-Close & 3s Interrupt Confirm (`web/{index.html,app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Restored「自动化」directly beneath「和 Collie 对话」in expanded `.side-nav` and placed `#rail-auto` + a dedicated `#rail-collie` home icon in the top navigation section of the 54px rail; restored the expanded `.side-foot` device status bar alongside compact Peer Collies / Preferences buttons and fixed `setConnDotState()` SSE indicator updates.
  - Eliminated `#notes-pane` horizontal and vertical drift across left-sidebar collapse/expand (`0.00px` max delta via viewport-right `--notes-right` anchoring), and added a 3-second two-click safety arming confirmation before sending `Ctrl-C` from `#wd-interrupt` and `#more-menu [data-act="interrupt"]`.
  - Stabilized desktop and wide-tablet HTML artifact drawer glides (`#v-collie.has-drawer-col-cap`, gutter-free `composerMetrics` vs gutter-aware `threadMetrics`, `minmax(0, 1fr)` on tablet/mobile `.app`, and `isDesktopDrawerViewport()` resize/rotation cleanup); auto-closed `#worker-drawer` with a subtle completion toast when a live worker finishes, hid the redundant `#wd-gone` banner on read-only ended workers, and always cleared live spinners (`#wd-live` / `wdLiveCache` / `#live`) when a pane ends.
- **Minimalist Pinned Message Bar & Activity Feed Infinite Scroll (`web/{app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Refined the main-chat pinned/pending floating strip with a unified 14px linear pushpin SVG (dropping the leading dot and redundant `已钉住 ·` prefix in pin-only mode), 10px elevated hairline framing, hover/focus-revealed desktop unpin button (with 32px coarse touch targets on mobile), and a compact multi-pin selector with selection memory and `Esc` focus return.
  - Replaced machine-translated「线程」copy across the Activity Feed and Forge settings with natural Chinese（「事件来源」/「来源」/「主会话」）, merged the source picker and「订阅路由」toggle into a single-row Linear-style toolbar, and upgraded manual 40-item pagination to native `IntersectionObserver` infinite scroll.
- **25× Faster Race-Enabled Test Suite & Database Flush Cache (`internal/collie/{db.go,super.go,chat.go,gitforge_cli.go,*_test.go}`)**:
  - Reduced full `go test -race -count=1 ./...` execution time from ~350s to ~15s via `TestMain` host side-effect isolation, lazy prepared statements with an exact-JSON `flushCache` in `Super.flush()` / `saveState`, a 512 KiB initial `earlierMessages()` window, deterministic virtual timers in JS VM tests, `zip.CreateRaw` fixture reuse, and `t.Parallel()` across isolated test suites (plus `WaitDelay = 1s` hardening in `CLIProvider.runCmdWithInput`).

### Client / API Changes

- **Chat `Message` JSON (`GET /api/panes/{id}/chat`, `GET /api/external/{id}/chat`, `GET /api/panes/{id}/chat/earlier`, `GET /api/panes/{id}/snapshot`, and SSE `chat` streams)** (backward compatible):
  - Added two optional `omitempty` fields on `kind="tool"` messages:
    - `detail` (`string`, optional): formatted full tool parameters for expanded view.
    - `subagent` (`object`, optional): `{ type, role, prompt, model, id }` structured metadata for subagent invocation tools.
- **Breaking changes**: None.

---

## v0.3.10 (2026-11-28)

### Highlights

- **Zero-Flicker Sidebar/Worker Switching, Dark-Mode Anti-White-Flash & 60 FPS GPU Glides (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - **Worker Switching & Spinner Stability**: Eliminated `#worker-drawer` blank opacity blink on in-drawer worker switches (removed `sd-swap` fade when switching between workers, kept the previous message list visible with a 140ms delayed skeleton fallback, made re-clicking the active worker a no-op, and upgraded `#live-list` / `#rail-live` / `#attention` / `#shells` to keyed in-place DOM patching so `.active` highlights and spinning indicators never reset). Removed main-chat scroll-pause freezing (`is-scroll-paused`) so working spinners glide continuously during scrolling while keeping background-tab power-saving (`html.is-bg`).
  - **Dark-Mode Attached Image Anti-White-Flash**: Replaced all hardcoded light `#f6f6f4` image-container fills with theme-adaptive `--img-bg` (`#1b1b1f` in dark mode / `#f4f4f2` in light mode) and added a `decodedThumbs` cache with `decode()`/`load` reveal guards so dark-mode thumbnails never flash white and cached redraws appear instantaneously.
  - **60 FPS Left/Right Drawer Glides & Zero Rail Disappearance**: Added scrollbar-gutter-aware `threadMetrics` to eliminate Frame-0 reverse jumps and Frame-N double reflows across desktop drawer open/widen/narrow/close transitions, added GPU-composited `.is-drawer-narrowing` staging for returning from 50/50 split to default width, suppressed mid-glide `ResizeObserver` thrashing via `isLayoutGliding()`, and kept the 54px `.side-rail` dual-mounted and counter-translated throughout collapse/expand so rail icons stay visible on every frame.
  - **Merged Top-Left Collie Toggle, Unified 4-Icon Footer & Restrained Polish**: Merged the desktop sidebar collapse/expand control directly into the top-left Collie icon (`#side-toggle-btn` / `#side-expand-btn`, `0.00px` coordinate delta across expanded ↔ collapsed, cross-fading to a linear sidebar icon on hover); unified the expanded `.side-foot` into the exact same 4-icon strip as the 54px rail (`Automation / Peer Collies / Devices / Preferences`) and dropped the redundant sidebar Automation text row; choreographed soft GPU handoff between the floating notebook (`#notes-pane`) and right drawers with top-bar「小本本」pill smoothly fading out while the card is open (`0px` header icon shift); removed the redundant clap button inside `#thread-drawer` while keeping main-chat claps intact.
  - **Desktop Worker Drawer Routing, macOS Clipboard Image Paste, File-Preview & Standalone PWA Link Actions**: Routed all desktop main-chat and thread-drawer worker links (including inline backtick worker mentions) directly into the right `#worker-drawer` without jumping full-screen; hardened macOS clipboard image paste (inspecting `file._itemType`, synchronous `arrayBuffer()` read initiation, UUID/hash cache matching, TIFF sniffing with optional canvas PNG transcoding, `text/html` `data:image/` fallback in `navigator.clipboard.read()`, and actionable non-HTTPS hints); fixed `#file-view` / `#rv-copy` copy buttons in HTML sandbox and CSV table modes (binding `commandfor` to persistent `fv-body`/`rv-body` with iframe-focus and plain-HTTP `execCommand` fallbacks + toast confirmation), fixed `#fv-wide` new-tab opening across regular browsers, Chrome PWA standalone windows, and remote hubs, and explicitly routed external `http(s)://` link clicks via `window.open` (with opener severing and clipboard-copy fallback if popup-blocked) in desktop standalone/app-window PWA mode so links in chat messages always open reliably.
- **Instant Memory & Automation Secondary Pages + Zero-Flash Integrations Switching (`internal/collie/{memapi.go,superapi.go,super.go,memory_io.go,peer_api.go}`, `web/{app.js,app.css}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - **Memory Page (`记忆`)**: Added lightweight summary mode (`GET /api/super/memory?summary=1&include_done=0`) with mtime/size-keyed 1-line preview caching, on-demand single-file loading (`GET /api/super/memory/file`), paginated archived tasks (`GET /api/super/memory/done`), and server-side full-text multi-term search (`GET /api/super/memory/search`). Deferred Markdown rendering, edit `<textarea>` creation, and O(|text|) worker-crew matching until cards are actually expanded or edited, cutting initial DOM nodes by >95% at 350–1,000+ archived files.
  - **Timers (`定时`) & Inbox (`收件箱`)**: Added optional server-side ended-timer pagination (`GET /api/super/timers?ended_limit=0` / `ended_only=1`), lazy-loaded ended timers, 40-row progressive Inbox pagination, and 2-phase read-then-write `syncInboxOverflow()` batching to eliminate O(N) layout thrashing.
  - **Zero-Flash Integrations (`接入`) & Instant Tab Switching**: Added per-tab in-memory stale-while-revalidate caching (`autoCache`), atomic `.is-integrations` width-class toggling coupled strictly to DOM commit (eliminating the 800px ↔ 940px cross-tab layout jolt), idle integrations pre-warming, HTTP `ETag` / `304 Not Modified` conditional polling across all automation endpoints, lazy `/api/super/lark` checks (removing the 6-second `?fresh` subprocess loop), and flicker-free Settings model selects.

### Client / API Changes

- **`GET /api/super/memory`** (backward compatible):
  - Default response (no query params) keeps full file bodies unchanged and adds optional `"done_count": number`, `"version": string`, and HTTP `ETag` / `If-None-Match` (`304 Not Modified`).
  - New optional query params: `summary=1` (returns full `text` only for up-front files `instructions.md`, `notes/index.md`, open `tasks/*.md`, `messages.md`, legacy files, and latest `dreams[0]`; deferred notes/dreams return `"summary_only": true` + `"preview": string` with empty `"text"`) and `include_done=0` (omits `tasks/done/*.md` from `files` while preserving accurate `"done_count"`).
- **New HTTP endpoints (standard `s.auth`)**:
  - `GET /api/super/memory/file?path=<relpath>`: reads a single `.md` file under `memory/`, `dreams/`, or legacy `memory.md` / `memory-archive.md`; returns `200 { file }` (`400` on invalid/traversal path, `404` if missing).
  - `GET /api/super/memory/done?offset=0&limit=60`: returns newest-first paginated `tasks/done/*.md` metadata (`summary_only: true`, `preview`) with `{ files, total, offset, limit, has_more }`.
  - `GET /api/super/memory/search?q=<query>&limit=60`: full-text multi-term AND search across `memory/**/*.md`, legacy files, and `dreams/*.md`; returns `{ query, files, dreams, total_files, total_hits, truncated }`.
- **`GET /api/super/timers`** (backward compatible):
  - Default response (no query params) unchanged; adds optional `ended_limit`, `ended_offset`, and `ended_only=1` query params returning `active_count`, `ended_count`, `ended_has_more` (and `ended` slice when `ended_only=1`).
- **HTTP `ETag` / `304 Not Modified`**:
  - Added `ETag` + `If-None-Match` (`304 Not Modified`) support to `GET /api/super/memory`, `GET /api/super/timers`, `GET /api/super/triggers`, `GET /api/super/integrations`, `GET /api/super/peers`, and `GET /api/super/settings`.
- **Breaking changes**: None.

---

## v0.3.9 (2026-11-28)

### Highlights

- **Linear Deep Graphite Dark Theme (`web/{app.css,index.html,app.js,manifest.webmanifest}`, `docs/demo/`)**:
  - Upgraded both dark-mode entry points (`@media (prefers-color-scheme: dark)` and `:root[data-theme="dark"]`) to a warm-neutral Linear Deep Graphite palette (`--bg: #111113`, `--bg-2: #151518`, `--bg-3: #1b1b1f`, `--line: #26262b`, `--line-2: #34343b`, `--code-bg: #16161a`, `--term-bg: #131316`) with lifted WCAG AA secondary/tertiary text (`--fg-2: #a8a8a3`, `--fg-3: #858580`, 5.1:1 contrast) and eye-friendly status hues (`--attn: #dfa057`, `--ok: #58c285`).
  - Replaced glaring pure-white dark-mode user bubbles (`#f2f2f0`) with soft deep charcoal bubbles (`--human-bg: #25252a`, `--human-line: #ededeb`) accented by a subtle `1px` inner rim (`rgba(255,255,255,.07)`), and elevated the dark「小本本」floating card to `#1b1b1f` with a soft top hairline highlight while leaving light mode completely unchanged.
- **Restrained Inline `进行中 / 话题` Slash Switcher in「小本本」(`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Restored the minimalist sticky-note card header (`小本本  M月D日` + single `×`) by removing the top tab bar and ink slider, and tucked a lightweight frameless inline slash switcher (`进行中 / 话题`, with a subtle pending micro-dot next to `话题` and full keyboard arrow-key navigation) directly into the first section heading row alongside its `+N` expand pill.
  - Switching `进行中` ↔ `话题` replaces only the first section's list in place (with a soft 130ms compositor fade-in), keeping「今日产物」and「下一条定时」permanently anchored and untouched underneath; expanding `+N` in `话题` mode unfolds overflow active topics and the folded「历史话题」sub-section in place without rebuilding DOM.
- **Desktop 54px Minimalist Icon Rail & Flat Same-Plane Split Transitions (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Collapsing the desktop (`> 1000px`) left sidebar now docks into a clean 54px vertical Icon Rail (`#side-rail`) separated by a single `1px` hairline—keeping expand toggle, Collie chat (with unread/pending dot), quick New Agent `+`, running-agent monogram/status tiles (hover tooltip + click to open the right task drawer), and bottom Automation / Peer Collies / Devices / Preferences icons always one click away.
  - Unified the desktop default width (`--drawer-default-w: clamp(440px, 34vw, 540px)`), custom drag width, and 50/50 widened split across all three right drawers (`#file-view`, `#thread-drawer`, `#worker-drawer`) so switching between artifacts, topics, and tasks keeps a rock-steady width with zero jump.
  - Eliminated the floating drop-shadow elevation feel across desktop left-sidebar collapse/expand (`> 1000px`) and right three-in-one drawer open/widen/close (`>= 1181px`): removed transition `box-shadow` and high overlay `z-index` on desktop so all three columns stay flush on the same plane while keeping 240ms GPU-composited lockstep motion (overlay shadows remain scoped to tablet/mobile sheets); smoothed the handoff between any open right three-in-one drawer and the「小本本」floating card (removing the 1-frame hard cut when clicking top-bar「小本本」), replaced discrete desktop column/sidebar width breakpoints (`1680px` / `1100px`) with continuous linear `clamp(...)` formulas for jump-free window resizing, and hardened composer `+` file-picker triggers for desktop Chrome/PWA windows (rendered `.file-picker-input` + `showPicker()`/`click()`).
- **Left Sidebar「工作中」Click-to-Drawer, Slimmed Worker Drawer Header & Aligned Topic Icons (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Clicking a Collie-dispatched worker inside the desktop left sidebar「工作中」list while on the main conversation view now opens it directly in the right three-in-one task drawer (keeping the header popout link for full-screen navigation and highlighting the active worker in both the full sidebar and the 54px rail).
  - Slimmed and decluttered the right worker drawer's upper chrome to the essentials: replaced the heavy gray summary box with a single-line muted caption (full text on hover, click or Enter/Space to expand), tucked「关联话题」and「中断」into the drawer header's restrained linear icon action strip, and added a **Developer Mode** (`collie.devMode`, default off) switch in Preferences (`#/prefs`) — keeping both the right task drawer and standalone Agent views focused on clean conversation view by default (plain non-agent shells still open directly on terminal screen), and revealing frameless underlined `对话 / 屏幕 / 日志` tabs on demand when Developer Mode is enabled.
  - Unified all left-sidebar「工作中」cards into uniform equal-height minimalist 2-line rows (Line 1: display label + `Collie 派出` tag + top-right status dot; Line 2: single concise task summary or `kind · machine · dir` metadata, keeping full details in hover tooltips and the right drawer) with every「关联话题」chat-bubble icon aligned strictly on the same bottom-right vertical axis directly underneath its top-right status dot.

### Client / API Changes

- None (Web UI layout, styling, and interaction polish release only; no HTTP endpoint, SSE event, JSON schema, or CLI changes).

---

## v0.3.8 (2026-11-27)

### Highlights

- **Unified Right Three-in-One Drawer, Plan A In-Card Notebook/Threads, Centered Command Search & Smooth GPU Main-Chat Glide (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Unified Artifact Preview (`#file-view`), Topic Thread Drawer (`#thread-drawer`), and new Collie-Dispatched Live Worker Drawer (`#worker-drawer`, default read-only peek bar with on-demand「直接插话」and auto-exposed approval cards) into a single `.side-drawer` shell docked in desktop Grid Column 3 (`>= 1181px`), eliminating the `v0.3.5` file-preview left blank void (`.sd-inner { width: 100% }`) and 50/50 topic-drawer bubble overlap.
  - Replaced the dual top-bar「小本本 / 话题线」buttons with a single「小本本」button and a frameless sliding in-card two-tab switcher (`事项 / 话题`) inside `#notes-pane`, plus a Raycast/Linear-style viewport-centered command search card (`#super-search`).
  - Added desktop left-sidebar collapse/expand toggle (`#side-collapse-btn` / `#side-expand-btn` / `Cmd+\`) with lockstep 240ms `cubic-bezier(0.22, 1, 0.36, 1)` pure-GPU `translate3d` glide across left sidebar, `#notes-pane`, and three-in-one drawer transitions (zero per-frame text reflow), automatically hiding the duplicate top `#crew` capsule strip on desktop (`> 1000px`) while the left sidebar is expanded (and showing it on mobile, tablet, or when sidebar is collapsed, with chip clicks opening the right worker drawer).
  - Fixed Top-Layer toast display inside open `<dialog>` modals, fixed message hover action bar stacking/hit-gap (`#v-collie .mine .msg-acts`), added `ResizeObserver` bottom/anchor scroll lock across viewport width resizing (`#v-collie .viewer` & `#td-body`), and updated the monochrome clap SVG icon from a heart to a crisp fine-line thumbs-up (`1.75px` stroke, zero emoji).
- **Compact `v2` Peer Invite Codes, `web+collie://` PWA Launch & Redesigned `/join/` Card (`internal/collie/peer.go`, `web/{manifest.webmanifest,app.js}`, `docs/join/index.html`)**:
  - Switched `encodePeerInvite` default output to compact `collie-peer://v2/<base64url>` and `/join/#2.<base64url>` (~60% shorter QR payload using short JSON keys `h/n/a/t/k/e`, `http://`-stripped top-2 IPv4/Tailscale addresses, and 128-bit base64url card tokens) while maintaining full backward compatibility with legacy `v1` invites.
  - Registered `"protocol_handlers": [{ "protocol": "web+collie", "url": "/?peer_invite=%s" }]` and `"launch_handler": { "client_mode": ["focus-existing", "auto"] }` in `web/manifest.webmanifest` and added `web+collie://join/` prefix normalization in both Go and Web UI.
  - Redesigned the zero-server `docs/join/` landing page into a minimalist invitation card with one-click preset switching (Local Node Proxy, Local Hub, Installed App `web+collie://`, Custom URL) and full `v2` / `v1` client-side decoding.

### Client / API Changes

- **Compact `v2` Peer Invite Payload (`POST /api/super/peers/invite`, `POST /api/super/peers/pair`)**:
  - `POST /api/super/peers/invite` now returns compact `v2` codes (`invite_code: "collie-peer://v2/<base64url>"`, `join_url: "https://lihuanyu0w0.github.io/collie/join/#2.<base64url>"`) encoding short keys `{ h, n, a, t, k?, e? }` (`http://` omitted in `a`, `https://` preserved; `k` omitted for `"card"`, `"once"` for one-time invites; `t` uses 16-byte 128-bit base64url for permanent cards).
  - `POST /api/super/peers/pair`, `extractPeerInvite`, and `decodePeerInvite` accept both legacy `v1` (`collie-peer://v1/...`, `/join/#<v1b64>`) and new `v2` (`collie-peer://v2/...`, `/join/#2.<v2b64>`, `web+collie://join/2.<v2b64>`, `/?peer_invite=...`).
- **PWA Manifest (`GET /manifest.webmanifest`)**:
  - Added `protocol_handlers` (`web+collie` -> `/?peer_invite=%s`) and `launch_handler` (`["focus-existing", "auto"]`).
- **Breaking changes**: None (fully backward-compatible with `v1` peer invites).

---

## v0.3.7 (2026-11-27)

### Highlights

- **Lightweight Monochrome SVG Message Claps — Medium-Style Rapid Applause with Zero Token Cost (`internal/collie/{store.go,db.go,super.go,superapi.go,dream.go}`, `web/{app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Added an original monochrome fine-line SVG clap control into Collie's reply action bar (and the topic focus drawer), harmonizing cleanly with Collie's minimalist white-paper and matte-dark palette (strictly zero system emoji; hollow outline when unclapped, restrained monochrome fill with crisp `tabular-nums` count once clapped, persistently visible at rest while keeping sibling actions calm until hover or focus).
  - Supports smooth single and rapid multi-click accumulation up to `50` with 60 FPS GPU-composited (`transform` + `opacity` only, zero forced layout reflow) micro-motion: spring-eased icon pop, 6 monochrome radial hairline lines blooming over 300ms, and a floating `+1 / +2 / +3` burst pill — respecting `prefers-reduced-motion` cleanly and keeping DOM nodes in-place without rebuilding message rows mid-burst.
  - Supports right-click (`contextmenu`), mobile long-press (`520ms` with drag cancel), and keyboard `Backspace`/`Delete` to clear and undo claps with a subtle toast confirmation.
  - Aggregates rapid clicks client-side with a 350ms debounce (plus `keepalive` flush on tab hide/unload), persists directly to SQLite via a lossless `entries.claps` column migration (kept in sync with `entries.data`), broadcasts live across devices over existing SSE `event: super`, and strictly guarantees **zero router wakeups, zero triggers, and zero token consumption**.
  - Lightly surfaces `[claps:N]` positive-feedback tags and top applauded replies inside nightly `dream/input.md` to help Collie reinforce helpful response habits during memory consolidation; refreshed the interactive static demo under `docs/demo/`.

### Client / API Changes

- **New HTTP endpoint (`POST /api/chat/clap`, standard `s.auth`)**:
  - Request JSON: `{ entry_id: string, delta?: number, claps?: number, reset?: boolean }` (`id` accepted as alias for `entry_id`; defaults to `delta: 1` when none of `reset`/`claps`/`delta` is specified; values are clamped to `0..50`).
  - Response JSON (`200 OK`): `{ ok: true, entry_id: string, claps: number, max: 50, changed: boolean }`. Returns `400` for invalid `entry_id`, negative `claps`, or non-`collie` entries; `404` if the entry does not exist.
  - Strictly zero-token: never enqueues a trigger, never wakes the router, and never emits notifications.
- **`Entry.claps` (`GET /api/super`, SSE `event: super`, `GET /api/super/earlier`)**:
  - Added optional integer field `claps?: number` (`omitempty`, omitted when `0`, range `1..50`) on `Entry` objects.
  - SQLite `entries` table adds lossless column `claps INTEGER NOT NULL DEFAULT 0` with partial index `entries_claps`.
- **Breaking changes**: None.

---

## v0.3.6 (2026-11-27)

### Highlights

- **Worker Short Display Labels (`collie spawn --label`) — Zero Extra Model Calls (`internal/collie/{store.go,db.go,cli.go,super.go,superapi.go,server.go}`, `web/{index.html,app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Added optional `--label` flag to `collie spawn` (e.g. `collie spawn --name fix-pagination --label "修分页单测" "..."` or `--label "Fix Auth"`), persisted in SQLite `workers.label` with automatic lossless migration for existing databases and seamless fallback to ASCII `name` when empty.
  - Preserved `label` automatically across `collie spawn --resume <worker>` and allowed updating it when a new `--label` is provided; kept all underlying `herdr` / tmux / log / `collie send|stop|show` identifiers strictly on ASCII `name` with zero breaking changes.
  - Exposed `label` in `GET /api/state`, `GET /api/super`, `collie list`, and `collie show`, and updated the default `ROUTER.md` template to guide Collie to attach a concise 4–6 character (or ~2 English word) `--label` in the human's language on every `collie spawn`.
  - Updated Web UI and `docs/demo/` to prioritize `label` in the top active-agent crew strip, left sidebar worker list, thread drawer worker pills, and worker detail header (displaying `label` with a subtle monospace ASCII `name` subtitle, full ASCII `name` + task summary on native `title` hover, and seamless fallback to ASCII `name` when unlabeled).

### Client / API Changes

- **`POST /api/super/spawn` & CLI `collie spawn`**:
  - Request body adds optional `label?: string` (CLI: `--label <text>`, whitespace-normalized and bounded to 64 runes; omitted or empty falls back to ASCII `name`; on `--resume`, retains the previous `label` unless a new non-empty `label` is passed).
  - Response adds optional `label?: string` (omitted when empty).
- **`GET /api/state` & SSE `event: state` (`AgentView`)**:
  - Active worker entries in `agents[]` add optional `label?: string` (omitted when empty; `name` remains strictly ASCII).
- **`GET /api/super` & SSE `event: super` (`Worker`)**:
  - Entries in `workers[]` add optional `label?: string` (omitted when empty).
- **SQLite `workers` table**:
  - Added `label TEXT NOT NULL DEFAULT ''` column via automatic lossless `ALTER TABLE` migration on DB open.
- **Breaking changes**: None.

---

## v0.3.5 (2026-11-27)

### Highlights

- **60 FPS Heavy-Load Smoothness Sprint Across Desktop, iPad & Mobile (`web/{app.css,app.js,index.html}`, `docs/demo/`)**:
  - **Compositor-only topic drawer & 50/50 widen**: Registered `--thread-w` / `--file-w` as non-inheriting CSS `@property` values and scoped `--thread-w` exclusively to `#thread-drawer` (never mutating `<html>`); replaced `.app` `grid-template-columns` / `.td-inner` `width` animations and 45-frame `beginCollieLayoutLock` polling with a fixed-position GPU `translate3d` slide and raised the tablet overlay breakpoint from `1000px` to `1180px`, lifting heavy-load drawer open/widen from ~27 FPS (167ms stalls) to ~59–60 FPS with zero LongTasks.
  - **Zero-jank sidebar & mobile route switching**: Upgraded parked `#v-collie.is-parked` from inherited `visibility: hidden` (plus descendant dot-pause selectors) to `content-visibility: hidden` (with `@supports not` fallback and never setting `inert`), and replaced mobile `<= 760px` `.main { display: none; }` tree destruction with a 1×1 stacked grid `content-visibility` switch — eliminating 167–183ms style-recalc stalls across desktop, iPad, and mobile.
  - **In-place DOM patching for SSE pushes & drawer replies**: Extended `patchSuperThread` to update middle `.event` status dots/`wstate`, `.say` pins/`.te-count`, and `.mine` `.te-count` in place with zero `replaceChild` churn, lazily deferred `ensureLiveAnchor`, and moved `renderPendingBar()` after anchor reads to prevent layout thrashing during rapid drawer sends and SSE updates.
  - **Zero-PrePaint floating side card & unified left-bias desktop reading preference**: Fixed the wide-screen (`>= 1664px`, e.g., `1920×1080`) `--notes-shift` regression so opening either floating card (`小本本` / `话题线`) consistently centers the main chat column in the remaining left space; replaced CSS `transition: transform` on the tall `.thread` container with a lightweight 60 FPS `requestAnimationFrame` matrix glide over persistent `will-change: transform`, eliminating Blink's 116ms PrePaint block.
  - **Route-return side-card stability**: Removed the always-replaying `np-swap-in` keyframe and kept wide-desktop `#notes-pane` state frozen inside parked `#v-collie` when visiting other views, so returning to the main chat reveals open side cards in place with zero blink or jitter.
  - **Removed expensive scrim blurs & layout transitions**: Dropped `backdrop-filter: blur(...)` from `.notes-scrim`, `.file-scrim`, `.thread-scrim`, `.super-jump`, and `dialog::backdrop` transitions, and eliminated layout/paint transitions (`box-shadow`, `padding`, `margin-top`) on `.notepane`.
- **Symmetrical Collapsible「话题线」Side Card & De-Collided Labels (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Aligned「话题线」with「小本本」's minimalist white-paper collapse/expand pattern: wide desktop defaults to compact single-line「活跃话题」rows with a `+N 历史` pill; clicking unfolds 2nd-line nowrap metadata and the lower「历史话题」section via isolated `0fr -> 1fr` `.np-fold` grid rows without rebuilding DOM (`Esc` collapses before closing; narrow/mobile drawers open directly expanded).
  - Renamed the left sidebar's running-agent group from「进行中」to「工作中」and the right thread card sections to「活跃话题」/「历史话题」, removing duplicate「进行中」labels on the same screen.
- **Chat Stream Readability & Crew Strip Polish (`web/{app.css,app.js}`, `docs/demo/`)**:
  - **External user quote bar**: Moved the `.re` quote strip outside the dark user bubble (directly above `.mine-media` and `.msg.user`, right-aligned, single-line ellipsis, `5px` gap) for all user message types (text, image+text, image-only) so short replies stay naturally compact while keeping click-to-jump intact.
  - **Readable horizontal crew strip**: Removed `.is-tight` / `--crew-chip-max` over-truncation and hover width-expansion animations on `#crew` agent chips (`flex: 0 0 auto`, `max-inline-size: 184px` desktop / `164px` mobile), preserving full readable names with smooth horizontal scrolling and subtle fade cue on overflow.
  - **Smarter consecutive Collie message grouping**: Tightened `.say.cont` merging so consecutive Collie replies break grouping (showing an independent Collie avatar and normal paragraph spacing) whenever the previous message carries a `↳ N 条回复` thread entry or the new message quotes a different topic root.
- **Verification**: Added comprehensive unit & static contract tests (`internal/collie/v035_smoothness_test.go`), updated synced static demo (`docs/demo/`), and verified across Desktop (`1440×900` / `1920×1080`), iPad (`1024×768`), and Mobile (`390×844`) in both light and dark themes.

### Client / API Changes

- None (Web UI performance, layout, and interaction release only; no HTTP endpoint, SSE event, JSON schema, or CLI behavior changes).

---
## v0.3.4 (2026-11-27)

### Highlights

- **Native `collie update` (Alias: `collie upgrade`) CLI Self-Update (`internal/collie/{update_cli.go,updates.go,cli.go,main.go,server.go}`)**:
  - Added native `collie update` (and friendly alias `collie upgrade`) reusing Collie's hardened self-update pipeline (`selfUpdateMu`, 3x retry with IPv4 fallback, automatic proxy-failure direct IPv4 fallback, `checksums.txt` SHA-256 verification, `<tmp> version` executability check, and atomic `os.Rename` binary replacement).
  - **Local update (`collie update` / `collie update --force`)**: Compares current version against GitHub `releases/latest`, prints a friendly message when already up to date (`--force` reinstalls or switches a `dev` source build to the latest official release), atomically replaces the running binary, and automatically detects and smoothly restarts active managed services pointing to that binary (Linux `systemd --user` `collie-hub` / `collie-node`; macOS `launchd` `com.collie.node` / `com.collie.hub` with built-in `LWCR / EX_CONFIG` `bootout + bootstrap` fallback; or prints a clean manual restart hint when unmanaged).
  - **Read-only version check (`collie update --check`)**: Prints a clear current-vs-latest comparison (and supports `--all --check` to inspect the entire connected fleet) with exit code `0` and zero downloads or file changes.
  - **One-command fleet rolling update (`collie update --all`)**: Talks to the connected Hub's `/api/version` and `/api/update`, upgrading all outdated online remote nodes first (automatically leveraging Hub `yamux` relay fallback when remote nodes cannot reach GitHub directly), skipping offline/up-to-date nodes cleanly, upgrading the Hub last, and printing a structured per-machine summary.
  - Added comprehensive race-checked unit tests (`internal/collie/update_cli_test.go`) covering mock GitHub releases, checksum & broken-binary blocking, proxy-to-direct IPv4 fallback, Linux/macOS service restarts, fleet update ordering, and machine-token auth; updated `collie --help`, bilingual READMEs, and the homepage Quick Start & Upgrade docs.

### Client / API Changes

- **Paired machine token auth scope (`GET /api/version`, `POST /api/update`)**:
  - Paired computer (`kind: "machine"`) Bearer tokens are now permitted to call `GET /api/version` and `POST /api/update` so `collie update --all` works from joined nodes as well as the Hub machine. Web-only routes (`POST /api/login`, `GET /api/devices`, etc.) remain restricted (`401 Unauthorized`).
- **Breaking changes**: None.

---

## v0.3.3 (2026-11-27)

### Highlights

- **Zero-Server HTTPS Click-to-Add Landing Page (`docs/join/index.html`)**:
  - Added bilingual (EN / 中文), light/dark adaptive static landing page at `https://lihuanyu0w0.github.io/collie/join/#<payload>` that parses the friend's Collie business card 100% client-side from `location.hash` with zero outbound network requests (`no-referrer`, never leaves the browser).
  - Remembers the visitor's own Collie Hub URL in `localStorage` (with one-tap local presets `http://127.0.0.1:17788` / `http://127.0.0.1:7788` and custom LAN / Tailscale input validation), jumping directly to `<myHub>/?peer_invite=...` with a fallback invite-code copy button and a restrained new-user onboarding card below.
- **Permanent Personal Business Cards & Direct `/join/#` QR Codes (`internal/collie/{peer.go,peer_api.go,hub.go,cli.go}`, `web/app.js`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Replaced the 10-minute one-time peer invite with a permanent, reusable personal business card secret persisted in SQLite (`peer_card_secret`) — safe to drop into group chats or scan hours later, paired with a manual「重置名片」action (`reset: true`) that immediately invalidates old links and QR codes without affecting already-paired friends.
  - Encoded `https://lihuanyu0w0.github.io/collie/join/#<payload>` directly inside the「我的边牧名片」QR code (and lowered QR low-EC threshold to `> 180` bytes for crisper mobile scanning), so system cameras, WeChat, and Feishu open the `/join/#` page immediately; updated the primary action to「一键复制邀请链接」while keeping the raw `collie-peer://v1/...` line and backwards-compatible scanning/pasting of share copy, `/join/#` links, `?peer_invite=`, and legacy expiring `collie-peer://v1/...` codes.
- **Approval-First Bidirectional Friend Requests & `?peer_invite=` Auto-Open (`internal/collie/{peer.go,peer_api.go,cli.go}`, `web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Opening Collie Web UI with `?peer_invite=...` (or `#peer_invite=...`) immediately strips the parameter via `history.replaceState`, auto-opens「好友边牧 → 扫码 / 粘贴加好友」with the card prefilled and previewed, and requires just one tap on「发送好友申请」.
  - Submitting a friend request now creates `pending_incoming` on the inviter (zero inbound inbox credentials granted; all `/api/peer/inbox` calls rejected until approved) and `pending_outgoing` on the requester; the inviter receives a clickable main-chat notification bar (`#peer-req-bar`), a clickable main-chat event, and a top-pinned「待审批好友申请」section in the peer modal with inline reception-mode selection and one-tap「通过 / 拒绝 / 拒绝并拉黑」actions. Approving activates both Hubs atomically via `POST /api/peer/pair/activate` (with seamless fallback for `v0.3.0–v0.3.2` Hubs).
- **Threads Side Card Visual Alignment with「小本本」(`web/app.css`, `docs/demo/`)**:
  - Unified the「话题线」side card interior inside `#notes-pane` with「小本本」's calm white-paper checklist language: matching 13px inset, delicate `10.5px` filter tabs, borderless rows with soft hover/active fill, calm 5px status dot without glow halo, and airy 2-line title + faint metadata layout.

### Client / API Changes

- **`POST /api/super/peers/invite`**:
  - Request body adds optional `reset?: boolean` (defaults to `false`; when `true`, rotates the permanent business card secret and invalidates previously shared links/QR codes).
  - Response adds `join_url: string` (`https://lihuanyu0w0.github.io/collie/join/#<payload>`) and `permanent: boolean` (`true`); `expires_at` is now optional/omitted for permanent cards (kept only if an expiring invite is returned).
- **`POST /api/super/peers/pair`**:
  - `invite_code` now accepts raw `collie-peer://v1/...`, `/join/#<payload>` URLs, `?peer_invite=...` URLs, or multi-line share text.
  - Response adds `pending: boolean` (`true` when remote Hub requires approval and local peer enters `status: "pending_outgoing"`; `false` on legacy `v0.3.0–v0.3.2` immediate pairing).
- **New owner peer-approval HTTP endpoints (standard `s.auth`)**:
  - `POST /api/super/peers/{id}/approve`: Accepts optional `{ name?, mode? }`, activates a `pending_incoming` friend request on both Hubs, and returns `{ ok: true, peer }`.
  - `POST /api/super/peers/{id}/reject`: Accepts optional `{ block?: boolean }` (or `?block=1`), permanently deletes the `pending_incoming` request without issuing credentials, optionally adds `hub_id` to `peer_blocklist`, notifies the requester, and returns `{ ok: true, blocked }`.
- **New narrow peer-to-peer HTTP endpoint (`peerAuth`, Bearer `clp_` token)**:
  - `POST /api/peer/pair/activate`: Called by the inviter upon approval to deliver `outbound_token_for_b` and transition the requester's `pending_outgoing` peer record to `active`.
  - `POST /api/peer/pair/accept`: Request adds optional `request_approval?: boolean`; response adds `status?: "pending"` and omits `outbound_token_for_b` when `request_approval: true`.
- **`PeerView.status` & `PeersSummary` (`GET /api/super`, SSE `event: super`, `GET /api/super/peers`)**:
  - `PeerView.status` adds `"pending_incoming"` and `"pending_outgoing"` (in addition to `"active"`, `"paused"`, `"revoked_by_peer"`).
  - `PeersSummary` adds optional `pending_incoming?: number` and `pending_outgoing?: number` (omitted when zero; backwards-compatible).
- **`GET /api/qr`**: Lowered EC Low threshold from `> 256` to `> 180` bytes for higher scannability on `/join/#` business card QR codes.
- **Breaking changes**: None (`v0.3.0–v0.3.2` Hubs remain bidirectionally compatible).

---

## v0.3.2 (2026-11-27)

### Highlights

- **Unified Right Minimalist Side Card for「小本本」&「话题线」+ Symmetrical Top Bar (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Replaced the standalone threads dropdown popover (`#collie-threads-pop`) with the exact same minimalist white-paper right floating card used by「小本本」— sharing identical placement, typography, shadows, mobile/tablet drawers, and GPU `translate3d` synchronized main-chat yield.
  - Kept two symmetrical pure-text top-bar toggles (`话题线` and `小本本`, removing the trailing count badge from `话题线` while keeping only a subtle dot when unresolved items exist): defaults to「小本本」, switches smoothly in-place between「小本本」and「话题线」, collapses cleanly to neither on second click or `×`, and persists the user's last choice (`collie.sidePane`: `notes` / `threads` / `none`).
- **Peer Hub Modal Anti-Clipping Layout & Plain-Language Reception Modes (`web/{app.css,app.js}`, `internal/collie/peer.go`, `docs/demo/`)**:
  - Fixed bottom-bar clipping on shorter screens (such as iPad landscape `1024×768`) by constraining `dialog.int-sheet[data-modal-kind="peers"]` to `max-height: min(620px, calc(100dvh - 32px))` with a fixed header/tabs/footer flex column and scrollable `flex: 1; min-height: 0; overflow-y: auto` body—guaranteeing zero footer truncation and zero tab-switch height flicker across desktop, iPad, and mobile.
  - Replaced engineering jargon with plain-language copy in the modal subtitle (`内网 / Tailscale 直连 · 令牌仅存本机`), invitation notes, and the three reception modes (`先帮我查清，草稿给我审`, `只留个便签，不自动回复`, `查完给方案，批准再干活`).
- **Calm Left Main Stream During Desktop Side-by-Side Thread Focus (`web/{app.css,app.js}`)**:
  - When replying inside the side-by-side desktop thread drawer (`> 1000px`), froze the left main conversation's scroll position, suppressed mirrored pop-in and `Collie 在想…` thinking-row animations in the left stream, and softly receded the left stream into the background (restoring full clarity immediately on hover, focus, click, or drawer close).
- **Modern Detached Rounded User Image Cards (`web/{app.css,app.js}`, `docs/demo/`)**:
  - Separated attached images out of the dark user text bubble into right-aligned rounded media cards (subtle 1px border + soft shadow, 6px gap above the compact text bubble, compact 2/3-column grid for multiple images, and pure standalone rounded cards with zero dark background wrapper for image-only messages), preserving click-to-zoom lightbox gallery navigation.

### Client / API Changes

- None.

---

## v0.3.1 (2026-11-27)

### Highlights

- **Independent Minimalist「小本本」&「话题线」with Dual Silky Motion (`web/{index.html,app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Split「小本本」and「话题线」back into two independent, harmonized plain-text top-bar triggers (zero SVG icons, subtle pending dot/count on `话题线`, automatic mutual exclusion between `#notes-pane`, `#collie-threads-pop`, search, and menu, and removed the redundant `← 话题线` back button from the thread focus drawer header).
  - Restored the pure v0.2.94 minimalist「小本本」floating card (compact header `小本本` + date + `×`, no dual tabs and no bottom `展开详情` bar) with two isolated compositor-friendly motions:
    1. **Open / close**: Smooth GPU-composited card entrance (`240ms` open / `180ms` close, `translate3d(12px, 0, 0) scale(0.985)` + `opacity`) synchronized with pure `transform: translate3d(var(--notes-shift), 0, 0)` main-chat yield so opening/closing the notebook never abruptly resizes the chat column or rewraps message text (and applies `0px` shift on ultra-wide screens where right-side breathing room already fits).
    2. **Compact ↔ expanded**: Clicking the card header, blank space, or `+N` overflow pill smoothly unfolds extra rows in-place via isolated `contain: layout style paint` CSS Grid `0fr → 1fr` disclosure without rebuilding DOM, with fast fade-out before collapse and `Esc` stepwise collapse-then-close.
- **Seamless Top Bar, Cleaner Sidebar & Integrations Polish (`internal/collie/superapi.go`, `web/{index.html,app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Removed the `.collie-top` 1px scrolled bottom hairline so the top bar blends seamlessly into the chat stream whether static or scrolled.
  - Replaced the sidebar「自动化」subtitle with a compact global overview (`N 接入 · M 定时`, prioritizing `N 接入异常 · M 定时` on errors, or `接入与定时` when empty), backed by an optional `timers_count` field on `superView`.
  - Removed the redundant `+ 添加好友边牧` button from the「自动化 → 接入」top status row, keeping friend management cleanly inside the「好友边牧」card.
- **Zero-Flicker Peer Modal, Image Paste & Sticky-Bottom Reliability (`web/{app.css,app.js}`)**:
  - **Zero-flicker Peer Modal**: Mounted the「好友边牧 · 受控收件箱」dialog structure once, switched main tabs / sub-tabs / default handling modes via in-place visibility & class updates, cached generated invites per mode, preloaded updated QR images before swapping `src`, and stabilized QR card & modal body heights so tab/mode switches never flicker or collapse into spinners.
  - **Desktop Messaging App Image Paste Recovery**: Removed the 5-second `lastModified` cutoff that split delayed clipboard copies into separate failing entries, grouped hash-named sandbox `.jpg` references with memory bitmap candidates synchronously inside `paste`, and enforced strict layered fallback (`clipboardData` file items → `navigator.clipboard.read()` across all `image/*` types → `text/html` `data:image/`), eliminating false `剪贴板里的图片已失效` errors on macOS desktop pastes.
  - **Image Attachment Send Auto-Scroll**: Added sticky-bottom tracking with capture `load`/`error` listeners on `#super-thread` and blob-URL thumbnail pre-seeding so sending messages with image attachments scrolls cleanly below the rendered image while immediately yielding if the user scrolls upward.
- **View-Switch & Long-Distance Jump Performance Overhaul (`web/{app.css,app.js}`)**:
  - **Zero-Flash, Zero-Layout-Rebuild Sidebar Navigation**: Parked `#v-collie` in a strict-contained offscreen layer (`.is-parked` with `contain: strict`, `inert`, and paused animations) instead of `display: none` so switching away from and back to main chat preserves the entire message layout tree and exact `scrollTop` with zero flash; replaced full sidebar `innerHTML` rebuilds on click with O(1) `syncSideActive()`; added signature caching and next-frame background refreshes for「自动化」,「设备」(including in-place pairing countdown patching),「偏好设置」, and Agent terminal view entry.
  - **Distance-Aware Pin / Quote / Jump-to-Bottom Scroll**: Replaced long-distance `behavior: "smooth"` scrolling across hundreds of rich bubbles with `smartScrollCollie()`: nearby targets (`<= 1.5×` viewport and `<= 1400px`) glide smoothly directly, while distant jumps teleport instantaneously (`behavior: "auto"`) to `~180px` before the target and perform a short smooth glide on the next frame followed by target highlight.

### Client / API Changes

- **`GET /api/super` & SSE `event: super` (`superView`)**: Added optional field `timers_count?: number` (count of enabled timers; backwards-compatible, omitted when zero, older clients ignore safely).
- **Breaking changes**: None.

---

## v0.3.0 (2026-11-27)

### Highlights

- **Peer Hub Controlled Inbox & Physical Auth Isolation (`internal/collie/db.go`, `peer.go`, `peer_api.go`, `server.go`)**:
  - Introduced zero-relay peer-to-peer collaboration between private Collie Hubs on the same LAN or Tailscale network while preserving strict per-owner Hub, node, and memory sovereignty.
  - Added isolated SQLite tables (`peers`, `peer_messages`, `peer_blocklist`); minted 256-bit `clp_` inbound peer tokens stored exclusively as SHA-256 hashes in SQLite, while storing outbound peer tokens solely in `SecretStore` (`~/.collie/secrets.json`, `0600`) — never mixing peer credentials into `Auth.devices` or `hubs.json`.
  - Added a dedicated narrow `peerAuth` middleware (Bearer header only, no cookies, no query params, no CORS) strictly scoped to `/api/peer/{ping,inbox,revoke}` (0 terminal / 0 file / 0 memory / 0 direct worker-spawn access), guarded unmatched `/api/*` routes through `s.auth`, and added instant one-sided pause, revoke, and hub blocklist controls.
- **Zero-Relay Bidirectional Pairing & Built-In Local QR Code Friend Adding (`internal/collie/peer.go`, `hub.go`, `web/{index.html,app.css,app.js}`, `scripts/demo/demo.js`, `docs/demo/`)**:
  - Reused `lanAddresses()` and Collie's built-in local `go-qrcode` generator (zero third-party CDN) to issue 10-minute one-time `collie-peer://v1/...` invites and high-contrast white-surface QR business cards; handshake executes atomic bidirectional token exchange with reverse `/api/peer/ping` reachability checks and HTTP redirects disabled (`CheckRedirect`).
  - Added the「好友边牧」card and paired-peer overview inside「自动化 → 接入」, reusing `<dialog class="sheet int-sheet">` for managing paired peers (per-peer pause/resume, handling mode, live latency ping, two-step revoke/block, and unblock) and adding friends via「我的边牧名片」QR card + one-click copy or「扫码 / 粘贴加好友」(browser `BarcodeDetector` live camera scan with graceful degradation to QR image upload or invite-code paste with real-time UTF-8 preview).
- **Async Investigation & Handoff Loop + `collie peer` CLI (`internal/collie/peer.go`, `super.go`, `superapi.go`, `cli.go`, `main.go`)**:
  - Routed peer exchanges to stable threads (`peer:<peer_id>:<topic_id>`) with `source="peer:<peer_id>"` so incoming peer messages never interrupt the owner's active human `chat` turn, backed by durable `integration_dedup` idempotency, dual-layer token-bucket rate limiting (per-peer 6/min burst 3, global 20/min burst 8, queue backpressure 10), L0 `notify` (0 Router wake), L1 `advise` (default read-only investigation wrapped with explicit anti-prompt-injection guardrails), and L3 `propose` readiness.
  - Added `collie peer ls|list|invite|pair|ping|send --confirm|revoke [--block]` CLI subcommands with double-latch confirmation (`--confirm` + `confirm: true`), outbound `SecretStore` / master token / private-key / common-token leak blocking, and updated `ROUTER.md` guidance.
  - Added end-to-end dual-Hub HTTP loopback race tests (`internal/collie/peer_test.go`) and regenerated `docs/demo/` via `scripts/demo.sh`.
- **Unified Dock & Thread Drawer Navigation Polish (`web/{index.html,app.css,app.js}`, `docs/demo/`)**:
  - Replaced the top-bar `panel-right` icon with an ultra-light plain-text `小本本` button aligned with the `派活 · 默认` aesthetic, showing a subtle unread dot and count badge when collapsed with pending threads.
  - Added a lightweight `← 话题线` back button at the top-left of the thread focus drawer header (`.td-head`) that closes the thread drawer and returns directly to the `话题线` tab in the right dock (and the mobile dual-tab bottom drawer), unifying the thread-list → thread-focus → back-to-list loop.

### Client / API Changes

- **`GET /api/super` & SSE `event: super` (`superView`)**: Added optional field `peers_summary?: { total, active, online, paused, enabled }` (backwards-compatible; older clients ignore it safely).
- **New narrow peer-to-peer HTTP endpoints (`/api/peer/*`, authenticated strictly by `peerAuth` Bearer `clp_` token or one-time invite code; CORS disabled)**:
  - `POST /api/peer/pair/accept`: Redeems a 10-minute one-time invite code with reverse `/api/peer/ping` verification and exchanges independent 256-bit tokens.
  - `GET /api/peer/ping`: Returns `{ ok, hub_name, version, protocol_ver, paused }`.
  - `POST /api/peer/inbox`: Delivers a structured message (`msg_id`, `topic_id`, `kind`: `ask|handoff|reply|ack`, `in_reply_to`, `hop`, `subject`, `body`, `sent_at`; max 32 KB request / 16 KB body) into the controlled inbox with idempotent dedup and rate limiting.
  - `POST /api/peer/revoke`: Notifies the remote hub of unilateral unpairing.
- **New owner peer-management HTTP endpoints (`/api/super/peers*`, standard `s.auth`)**:
  - `GET /api/super/peers`: Returns `{ hub_id, hub_name, enabled, addrs, peers, blocklist, blocked_count, summary }`.
  - `POST /api/super/peers/settings`: Updates `{ enabled?, hub_name? }`.
  - `POST /api/super/peers/invite`: Issues a 10-minute one-time invite `{ invite_code, code, hub_id, hub_name, mode, addrs, expires_at }`.
  - `POST /api/super/peers/pair`: Initiates bidirectional handshake from `{ invite_code, name?, mode? }`.
  - `PATCH /api/super/peers/{id}`: Updates `{ name?, mode?, status? }` (`active | paused | blocked`).
  - `POST /api/super/peers/{id}/ping`: Probes remote reachability and round-trip latency (`{ ok, peer_id, addr, latency_ms, remote_name, remote_version, protocol_ver, paused, error? }`).
  - `DELETE /api/super/peers/{id}[?block=1]`: Immediately revokes local hash/secret and optionally adds `hub_id` to `peer_blocklist`.
  - `POST /api/super/peers/{id}/send`: Sends a human-confirmed message (`{ topic_id?, kind?, subject?, body, in_reply_to?, hop?, confirm: true }`) with outbound secret-leak blocking.
  - `GET /api/super/peers/{id}/messages`: Lists recent peer message records.
  - `DELETE /api/super/peers/blocklist/{hub_id}`: Removes a hub from the blocklist.
- **Existing endpoint adjustments**:
  - `GET /api/qr`: Raised `text` parameter limit from 512 to 1024 bytes (auto EC Low above 256 bytes) to accommodate `collie-peer://v1/...` business card QR codes.
  - Unmatched `/api/*` paths now pass through `s.auth` before returning `404` so unauthenticated or narrow peer tokens receive `401`.
- **Breaking changes**: None.

---

## v0.2.95 (2026-11-27)

### Highlights

- **Unified Right Floating Dock「小本本 | 话题线」(`web/index.html`, `web/app.css`, `web/app.js`)**:
  - Merged the floating notebook（「小本本」）and the thread list popover（「话题线」）into a single right floating card with a minimalist dual-tab header (`小本本 | 话题线`, anchor-positioned sliding underline, unread dot + count badge) and a single `×` button.
  - Consolidated the top-bar threads and notebook controls into a single clean `panel-right` linear toggle icon (`#open-notes-btn`) matching top-bar stroke weight (`1.75px`), with subtle active-pane shading when open and an unread dot when collapsed.
  - Simplified the card to a clean single scrollable state (removed compact/expanded mode switching), preserved the wide-desktop (`>= 1360px`) left-biased main chat column (`--notes-reserve`), and upgraded open/close motion to pure compositor-layer `transform + opacity` animations (top-right origin pop on wide desktop, slide-in sheet on `< 1360px`, and unified dual-tab bottom drawer on mobile `<= 760px`).
- **Backend SSE Payload Slimming & Residual Main-Chat Jank / Idle Power-Saving Fixes (`internal/collie/superapi.go`, `web/app.css`, `web/app.js`)**:
  - Slimmed `superState()` (`GET /api/super` and SSE `event: super`) for ended (`ended` / `gone` / `stopped`) workers by rune-safely capping historical `task` (`<= 400` runes) and `last_report` (`<= 240` runes) on the response copy while leaving active workers, underlying store records, and `GET /api/super/show/{id}` / `collie show` completely lossless (~45% smaller `/api/super` payload and ~66% smaller `workers` payload in busy workspaces).
  - Eliminated 60Hz idle style/PrePaint wakeups: converted `.dot.idle` to a calm static indicator, guarded ambient animations with `@media (prefers-reduced-motion: no-preference)`, and added power-saving pause gates when the tab is hidden (`html.is-bg`), all workspaces are idle (`html.is-idle`), or the user is actively scrolling main chat (`.is-scroll-paused`).
  - Eliminated residual desktop main-chat scroll and message-insertion jank: replaced `.thread > * + *` (which caused Blink full-subtree style recalcs across 3,000+ elements on DOM updates) with flex `gap: var(--thread-gap)`, promoted `.viewer.chatbg` with `will-change: scroll-position`, removed hover `transition: opacity` layer thrashing on `.msg-acts` and message timestamps during scrolling, gated `#crew` / `.td-workers` ScrollTimeline fade masks behind `.is-overflow`, and decoupled `#super-thread` render fingerprinting from irrelevant worker `report_at`/`topic` churn (`superThreadWorkerFingerprint()`).
- **Memory Page「已结束」Progressive Time Bucketing (`web/app.js`, `web/app.css`)**:
  - Grouped `tasks/done/*.md` items chronologically descending: near 7 days by day (`今天`, `昨天`, `MM-DD 周X`, auto-expanding only the most recent day bucket), earlier in the current month by week (`M 月第 N 周`, default collapsed), and earlier months by month (`YYYY 年 M 月`, default collapsed), with per-group counts and automatic bucket expansion on search hits.
- **Decision Card Free-Text Reply Deduplication (`web/app.js`)**:
  - Kept pure option clicks as `✓ 已选：<option> · HH:MM` without emitting a duplicate user bubble, while simplifying free-text inline replies on the original decision card to a light `✓ 已回复 · HH:MM` pill instead of repeating the user's message text that already appears in the chat bubble.
- **`.filepane` Open/Close Scroll Anchor Lock (`web/app.js`)**:
  - Anchored `#super-thread` to the top visible message before opening, widening, or closing `.filepane` (such as `.csv` / `.html` / file previews) so the main chat viewport no longer drifts across layout transitions.
- **Homepage, Bilingual README & Static Demo Sync (`docs/index.html`, `README.md`, `README.zh-CN.md`, `docs/demo/`)**:
  - Highlighted Collie's Gemini / Antigravity multi-terminal command deck positioning (alongside Claude Code, Codex, and Cursor) in both English and Chinese, and regenerated `docs/demo/` via `scripts/demo.sh`.

### Client / API Changes

- **`GET /api/super` & SSE `event: super`**: Backward-compatible payload slimming — ended (`ended: true`, `status: "gone"`, or `status: "stopped"`) worker entries now cap `task` at 400 runes (`…`) and `last_report` at 240 runes (`…`) on the view snapshot. Active workers are unchanged, and full lossless worker/trigger records remain available via `GET /api/super/show/{id}` (`collie show <id>`).
- **Breaking changes**: None.

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
