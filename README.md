<p align="center"><img src="web/icon.svg" width="72" alt="Collie"></p>

<h1 align="center">Collie</h1>

<p align="center"><b>Herd your AI coding agents from your phone.</b><br>
Claude Code · Codex · Antigravity — one hub, all your machines, one super-agent that keeps work moving.</p>

<p align="center">
<a href="https://lihuanyu0w0.github.io/collie/demo/"><b>▶ Live Demo</b></a> ·
<a href="https://lihuanyu0w0.github.io/collie/">Homepage</a> ·
<a href="#quick-start">Quick Start</a> ·
<a href="https://github.com/lihuanyu0w0/collie/releases">Releases</a> ·
<b>English</b> | <a href="README.zh-CN.md">中文</a>
</p>

<p align="center"><img src="docs/img/demo.gif" alt="Send one message to Collie: it dispatches workers, summarizes reports, and turns approval prompts into one-tap cards" width="900"></p>

Your Claude Code, Codex, and Antigravity sessions are already running on your Mac, devbox, or cloud VM. **Collie** lets you pick them up from your phone: read real conversations, send follow-ups, and tap through approval prompts without walking back to your desk. When tasks pile up, hand them to Collie's built-in **super-agent**—it splits work across your machines in parallel, watches for stalls, and only pings you when a decision actually needs a human.

> Named after the border collie: [herdr](https://herdr.dev) manages the terminals; Collie herds the agents.

## Why Collie

- **One entry point, a fleet of agents** — Tell Collie what you need. It picks the right agent, model, and machine, dispatches workers in parallel, and rolls updates back into one conversation.
- **Approval prompts become one-tap cards** — When an agent pauses at *"Allow this command?"*, Collie parses the question and options into a mobile-friendly decision card. Tap once to answer.
- **All your machines, zero open ports** — Connect your Mac, Linux devboxes, and cloud VMs to one hub over outbound multiplexed tunnels (`yamux`). Works behind NAT with automatic IPv4 and pure-IPv6 dual-stack discovery—no SSH, no inbound ports.
- **Real conversations, reply threads & attachments** — Reads native agent transcripts (`JSONL`) instead of scraping screenshots, with raw `xterm.js` screen and wrapped log views one tap away. Deep quote-reply chains open in a resizable **reply-thread drawer** with direct image paste, drag-and-drop uploads, and live worker status pills, while main chat stays anchored to your unread boundary.
- **Always-on autonomy & integrations** — Wire in **GitHub** (PR reviews, `@mentions`, CI failures), pluggable local CLI providers, **Feishu / Lark** (zero-public-IP WebSocket two-way cards), generic webhooks, and cron timers. Collie retries or switches approaches on stalls and only escalates real decisions.
- **Portable long-term memory** — Remembers your preferences, ongoing tasks, and per-project notes, tidies them nightly ("dream" consolidation), and supports one-click **ZIP export/import** across machines.
- **Single binary, your data** — One Go binary + SQLite + embedded frontend (zero CDN). Per-device tokens you can revoke anytime, plus **one-click fleet updates** from the web UI without interrupting running agents.

<table>
<tr>
<td width="62%"><img src="docs/img/hero-light.png" alt="Chatting with Collie"><br><sub><b>Chat with Collie</b>: two workers dispatched, one reported back, and a blocked agent resolved inline</sub></td>
<td width="19%"><img src="docs/img/phone-list.png" alt="Mobile agent list"><br><sub><b>Mobile</b>: needs-you pinned on top</sub></td>
<td width="19%"><img src="docs/img/phone-ask.png" alt="Mobile decision card"><br><sub><b>Decision card</b>: one tap to reply</sub></td>
</tr>
<tr>
<td colspan="3"><img src="docs/img/agent.png" alt="Direct agent conversation view"><br><sub><b>Direct agent control</b>: structured plan and tool calls parsed from the native transcript, with active model in the header</sub></td>
</tr>
</table>

More screenshots: [Screen view](docs/img/screen.png) · [Automation & Memory](docs/img/memory.png) · [Devices](docs/img/devices.png) · [Dark theme](docs/img/hero-dark.png)

## Quick Start

macOS, Linux (`amd64` / `arm64`), and Windows via **WSL2**. The installer verifies SHA-256 checksums, installs `collie` to `~/.local/bin/collie`, registers a background service (`launchd` or `systemd --user`), and installs [herdr](https://herdr.dev) if missing. Re-running the same command upgrades in place.

> **Windows (WSL2):** Run the exact same one-line command below inside a **WSL2 (Ubuntu / Debian)** terminal—it uses the Linux binary out of the box. If you only need to view or control remote agents from Windows, simply open the hub URL in Chrome or Edge.

**1. Start a hub on an always-on machine**

```bash
curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh -s -- hub
```

Prints your auto-detected LAN addresses (IPv4 and pure IPv6) and a one-time QR code. Override the address with `--url http://<host>:7788` (or `http://[<ipv6>]:7788`). On Linux, run `loginctl enable-linger` once so the user service keeps running after logout.

**2. Pair your phone**

Scan the terminal QR code (or open *Devices → Connect phone or tablet*), then **Add to Home Screen** (use Safari on iOS for full PWA session persistence). You can also enter the 8-character pairing code manually.

**3. Join your other computers**

Get a pairing code from *Devices → Connect a computer* (or run `collie pair --computer` on the hub), then run on each machine:

```bash
curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh -s -- node --hub http://<hub>:7788 --code <code>
```

The node opens an outbound tunnel to the hub (no open ports, no SSH) and saves its per-device token in `~/.collie/node.json`. All machines merge into one fleet—remote panes carry a machine suffix like `w1:p1@mac`.

> **Integrations & security notes:** The installer automatically sets up missing herdr integrations for Claude Code and Antigravity without overwriting existing configs, and skips Codex global hooks by default (Collie discovers, chats with, and resumes Codex natively without modifying `~/.codex/hooks.json`; pass `COLLIE_INSTALL_CODEX_HOOK=1` if you explicitly want it). Hub traffic is plain HTTP on LAN—put it behind Tailscale/VPN or an HTTPS reverse proxy (`--url https://…`) for cross-network access.

**Keeping the fleet updated:** The hub periodically checks GitHub Releases. When any machine falls behind, tap **Update all** in the sidebar or *Devices*—Collie updates remote nodes first, then the hub itself, without touching running agents.

## Architecture

<p align="center"><img src="docs/img/architecture.svg" alt="Collie architecture" width="900"></p>

One **hub** runs on an always-on devbox; other computers run `collie node` to dial out to the hub; phones, tablets, and browsers talk only to the hub. Agents live inside [herdr](https://herdr.dev), so closing the browser never stops them.

## Two Ways to Work

- **Control agents directly** — Launch, inspect, or take over any Claude Code, Codex, or Antigravity session on any machine. Terminal output turns into structured chat; approval prompts turn into one-tap cards. Sessions started outside herdr (iTerm, VS Code, tmux, SSH, desktop apps) also appear in the sidebar for live read-only viewing and one-click takeover.
- **Talk to Collie (Super-agent)** — One persistent entry point. Collie's **Router** understands context, splits tasks across isolated **Workers**, tracks progress, and surfaces concise updates. Quote any message to branch a focused conversation—chains of 3+ messages collapse neatly into a **reply-thread drawer** (with direct image paste/upload and live status pills for linked workers), while the main feed stays anchored at your unread position.

### Super-agent Loop

Inspired by DeepSeek's super-agent pattern and Anthropic's *Building always-on agents*:

```text
          ┌──────── You (Web chat / Feishu·Lark / GitHub / Timers …)
          ▼
   Trigger Inbox ── Fast path: per-source policy (respond / candidate / ignore)
          │
          ├── Subscribed worker ◀── direct delivery, bypasses Router
          ▼
   Router (persistent session: triage, dispatch, concise replies)
          │  collie spawn / send / reply / answer / timer …
          ▼
   Worker × N (isolated herdr workspaces, report back via collie report)
```

- **Router / Worker split** — The Router only talks to you and dispatches work (concise by default; configurable reply budget, default 200 characters/words) so it runs cheaply on a fast model while Workers use your strongest coding models.
- **Inbox, thread subscriptions & batching** — Chats, GitHub events, Feishu/Lark messages, webhooks, timers, and worker reports all land as **Triggers**. Unsolicited external events default to `candidate` (acted on only when covered by your rules or memory). Workers subscribed to a PR/issue thread receive follow-ups directly. Busy sessions batch incoming triggers cleanly once idle.
- **Inline & Spill** — Payloads over 700 chars inline a preview and spill the rest to `collie show <id>` so every event stays traceable without bloating context.
- **Watchdog & stateless recovery** — Stalled workers trigger a watchdog alert to the Router; every trigger, thread, and timer lives in SQLite, while long-term knowledge lives in `~/.collie/router/memory/` (automatically tidied overnight and exportable/importable as a ZIP).

---

## Advanced & Reference

<details>
<summary><b>Multiple hubs, Mac desktop app &amp; install from source</b></summary>

### Multiple hubs across isolated networks

When machines sit in networks that cannot reach each other (office devbox, cloud VM, home Mac mini), run one hub per network. In your primary hub, open *Devices → Other hubs* and paste the pairing URL from another hub's *Connect phone or tablet*. Its sessions merge into the same sidebar (tagged by hub) and talk directly to that hub from your browser.

- Hubs do not need network connectivity to each other—only your browser needs to reach each hub.
- Remote hubs must be `v0.2.4+` (CORS token auth, no cookies). An HTTPS page cannot connect to a plain HTTP hub.

### Install as a desktop app on Mac

Any machine running `collie node` exposes the same web UI locally at `http://127.0.0.1:17788` (proxied to the hub). Because browsers treat `127.0.0.1` as a secure origin, open it in Chrome, pair once with an 8-character code, and click **Install Collie** in the address bar for a standalone Dock app with desktop notifications. Change the port with `collie node --ui 127.0.0.1:<port>`, or disable with `--ui off`.

### Manual upgrade &amp; install from source

Re-run the installer to upgrade manually (`… | sh -s -- node` needs no re-pairing; include any custom `--url` flags when upgrading `hub`).

To build and install from source (requires **Go 1.25+** and `git`):

```bash
git clone https://github.com/lihuanyu0w0/collie.git ~/collie && cd ~/collie
sh docs/install.sh hub                                         # run as hub
sh docs/install.sh node --hub http://<hub>:7788 --code <code>  # join as node
```

Binary only (no service): `curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh`. Pin a version or target directory with `COLLIE_VERSION=v0.2.68` or `COLLIE_BIN_DIR=…`. Full script: [docs/install.sh](docs/install.sh).

</details>

<details>
<summary><b>External integrations &amp; webhooks (GitHub, Feishu / Lark, CLI providers)</b></summary>

Configure integrations in **Automation → Integrations** (credentials are stored in `~/.collie/secrets.json` with `0600` permissions and never echoed back):

- **GitHub &amp; pluggable Git forges** — Built-in GitHub integration monitors PR reviews, `@mentions`, and CI failures in `notify`, `advise`, or `agent` mode with stable per-PR threads. You can also drop custom JSON definitions into `~/.collie/providers/*.json` to connect any CLI-backed code host.
- **Feishu / Lark (zero-public-IP WebSocket)** — Native long-connection client supports bidirectional chat and interactive decision cards with no public callback URL required; event webhooks (`/hooks/feishu?key=…`, with `url_verification`) are also supported.
- **GitHub &amp; Generic Webhooks** — Copy URLs from *Automation → Integrations*:
  - GitHub webhook: `/hooks/github?key=…` (`application/json`)
  - Generic webhook: `POST /hooks/<source>?key=…` with JSON `{"text","thread","from","id"}` and optional `X-Idempotency-Key` deduplication. Webhooks return `202 Accepted` immediately and route asynchronously through the inbox.
- **Outbox notifications** — Optional Feishu / Lark or Slack notifications batched every 20 seconds; quiet mode only notifies when human input is needed.

</details>

<details>
<summary><b>Direct agent control, external terminal takeover &amp; herdr compatibility</b></summary>

- **Native transcript parsing** — Reads Claude (`~/.claude/projects/*/<id>.jsonl`), Codex (`~/.codex/sessions/**/rollout-*.jsonl`), and Antigravity (`~/.gemini/antigravity-cli/brain/<id>/…/transcript.jsonl` and desktop stores). For agents that don't emit a session ID to herdr, Collie pairs sessions exclusively by working directory and launch timestamp—falling back to terminal output rather than crossing wires.
- **Decision cards** — Detects confirmation prompts and sends either the choice number (numbered menus) or arrow keys + Enter (cursor menus), with a toggle to inspect the raw terminal screen.
- **Sessions in other terminals** — Discovers Claude Code, Codex, and Antigravity processes running directly in iTerm, VS Code, tmux, SSH, or desktop apps on the hub and every connected node. View them live in read-only chat, or click **Take over in herdr** to gracefully interrupt and resume the exact session (`claude --resume`, `codex resume`, `agy --conversation`) under Collie's control.
- **Browser preferences** — Gear icon at the bottom of the sidebar (per-browser): theme (System / Light / Dark), font size, default agent view (Chat vs Screen), Enter vs ⌘Enter send, collapse consecutive tool steps, and desktop notifications.
- **herdr 0.8 &amp; 0.9 compatibility** — Supports both versions automatically, persisting agent names in `herd.db`, falling back to direct terminal paste when 0.8 stays in `launch_pending`, and inspecting the live screen for folder-trust prompts.

</details>

<details>
<summary><b>CLI reference</b></summary>

The Router, Workers, and you all use the same `collie` binary (on joined nodes, CLI commands automatically read `~/.collie/node.json` and talk to the hub):

```bash
collie reply [--re ID] [--opt "choice"]... [--check] "…"     # Router → you
collie spawn [-kind claude] [-machine mac] [-cwd ~/x] \
             [-model ID] [-effort E] [-name n] [-thread t] [-subscribe] "task"
collie send <worker> "follow-up"                             # message a running worker
collie report "summary or BLOCKED: question"                 # worker → Router (via HERDR_PANE_ID)
collie answer <worker> <number | label | esc>                # answer a worker's prompt
collie subscribe [--off] <worker> <thread>                   # route thread directly to worker
collie stop <worker>                                         # stop a worker
collie show <trigger-id | thread | worker>                   # fetch spilled content
collie list                                                  # list workers, threads, timers
collie timer add [--thread T] [--to worker] "<spec>" "text"  # daily/weekdays/weekly/every/in/at
collie timer ls | rm <id>
collie memory export [file.zip]                              # export router memory as ZIP
collie memory import [--replace] <file.zip>                  # merge or replace router memory
collie say "…"                                               # message Router as yourself
collie hook <source> "…"                                     # inject a test external event
collie pair [--computer]                                     # print phone QR / computer join command
collie node [--hub URL --code CODE] [--name mac]             # connect this machine to a hub
```

</details>

<details>
<summary><b>Flags, environment variables &amp; <code>~/.collie</code> layout</b></summary>

Running `collie` (or `collie serve`) starts the hub, listening dual-stack on IPv4/IPv6. All frontend assets (including `xterm.js` and offline KaTeX) are embedded in the binary.

| Flag | Environment variable | Default |
|---|---|---|
| `-addr` | `COLLIE_ADDR` | `:7788` |
| `-socket` | `HERDR_SOCKET_PATH` | `~/.config/herdr/herdr.sock` |
| `-token` | `COLLIE_TOKEN` | Auto-generated master token |
| `-url` | `COLLIE_URL` | Auto-detected LAN IPv4/IPv6 URL for QR &amp; pairing |
| — | `COLLIE_HOME` | `~/.collie` |

> Anyone with a token can control your terminals. Keep the master token private; pair every other device so it gets its own revocable token (computer tokens can only open tunnels and call the CLI, never the web console).

| Path inside `~/.collie` | Purpose |
|---|---|
| `herd.db` | SQLite (WAL): trigger inbox, threads, workers, chat history, timers, settings, paired device token hashes |
| `node.json` | Node only: hub URL, machine ID, and node token |
| `token` / `addr` | Master access token and active listen address for local CLI calls |
| `secrets.json` | Integration secrets (`0600`, write-only in UI) |
| `providers/` | Optional custom Git-forge CLI provider definitions (`*.json`) |
| `router/` | Router workspace: `ROUTER.md` (mirrored to `CLAUDE.md` / `AGENTS.md` / `GEMINI.md`), `memory/` (long-term notes), and `dreams/` |

</details>

<details>
<summary><b>Build from source &amp; codebase layout</b></summary>

Requires **Go 1.25+**:

```bash
go build -o bin/collie .
./bin/collie                       # listens on :7788 (IPv4/IPv6 dual-stack)
go test ./...
scripts/release.sh v0.1.0 --dry    # cross-compile 4 platforms into dist/
```

```text
Browser ──HTTP + SSE──▶ collie hub (single Go binary, embedded web/) ──NDJSON──▶ herdr.sock
                          ├─ Incremental JSONL transcript parser
                          ├─ Super-agent: inbox → router → workers (SQLite)
                          ├─ Integrations: GitHub / CLI providers / Feishu·Lark WS / webhooks
                          └─ yamux tunnel ◀── collie node (herdr stream + local pane API)
```

```text
main.go              Entrypoint: version string + collie.Main
internal/collie/     Hub, node, super-agent, integrations, memory I/O, and CLI
web/                 Zero-build vanilla HTML/CSS/JS UI embedded via go:embed
docs/                install.sh, bilingual GitHub Pages site, demo/, and screenshots
scripts/release.sh   Cross-compilation and atomic draft GitHub releases
scripts/demo.sh      Builds docs/demo/ from the real frontend + mock data
```

</details>
