<p align="center"><img src="web/icon.svg" width="72" alt="Collie"></p>

<h1 align="center">Collie</h1>

<p align="center">在手机上放牧你的编程 agent<br>
<a href="https://lihuanyu0w0.github.io/collie">主页</a> · <a href="#安装">安装</a> · <a href="https://github.com/lihuanyu0w0/collie/releases">下载</a></p>

Collie 是一个安静的 Web 控制台和 super-agent，用来"放牧"跑在 [herdr](https://herdr.dev) 里的编程 agent（Claude Code、Codex、Antigravity）。手机、平板、桌面浏览器都能用：看对话、发消息、处理确认框，需要你的时候叫你一声。

> Collie 是牧羊犬：herdr 管终端，Collie 把 agent 赶到一处干活。

## 安装

一个 **hub** 放在常开的开发机上；其他电脑运行 `collie node` 主动连上 hub；手机、平板、浏览器只连 hub。

```
 手机 / 平板 / 浏览器 ──HTTP──▶  hub（开发机：collie + herdr）
                                  ▲   ▲
               collie node ──出站─┘   └─出站── collie node
               （本地 Mac + herdr）           （另一台机器）
```

每台电脑上都要先有 herdr 服务端在跑，并装好 agent 集成（`herdr integration install claude`、`codex`、`antigravity-cli`）。没装集成时，herdr 认不出 agent 在干什么，Collie 也就没法给它发消息。

安装脚本下载对应平台的二进制（校验 SHA-256），装到 `~/.local/bin/collie`，并装成后台服务（macOS 用 launchd，Linux 用 `systemd --user`）。以后重跑同一条命令就是升级。

**1. 开发机：起 hub**

```bash
curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh -s -- hub
```

启动时打印本机内网地址和手机配对二维码；地址认不准时加 `--url http://<开发机 IP>:7788`。日志：`journalctl --user -u collie-hub -f`。要在退出登录后继续跑，运行一次 `loginctl enable-linger`。

**2. 手机：扫码**

扫终端里的二维码，或者在「设备 → 连接手机或平板」里扫。二维码里是 `http://<内网 IP>:7788/?pair=<配对码>`，10 分钟内有效、只能用一次；打开后这台手机拿到自己的令牌，之后「添加到主屏幕」即可。也可以在登录页直接输入 8 位配对码。

**3. 其他电脑：加入**

在 hub 的「设备 → 连接一台电脑」里拿配对码（或在 hub 上运行 `collie pair --computer`），然后在那台电脑上：

```bash
curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh -s -- node --hub http://<hub>:7788 --code <配对码>
```

它会配对、把 hub 地址和令牌存进 `~/.collie/node.json`，再装成后台服务。之后重跑不带参数的 `… | sh -s -- node` 就是升级。

**4. 在 Mac 上装成 app（可选）**

加入了 hub 的电脑会在本机 `http://127.0.0.1:17788` 提供同一个界面（转发到 hub）。浏览器把 127.0.0.1 当作安全来源，所以用 Chrome 打开它、登录一次（「设备 → 连接手机或平板」里的 8 位配对码），地址栏右侧点「安装 Collie」，就会得到一个能放进 Dock 的独立 app，浏览器通知也能用了。换端口：`collie node --ui 127.0.0.1:端口`；不要：`--ui off`。

- node 出站连接、长连接复用（yamux），笔记本在 NAT 后面也能用，**不用开端口、不用 SSH**；断线后自动重连
- hub 把所有电脑的 herdr 合成一个：远端的 pane id 带后缀，例如 `w1:p1@mac`；列表、对话、启动 agent、Collie 派 worker 都可以指定电脑
- 每台设备一个令牌，可以在「设备」页随时移除；电脑的令牌只能连隧道和调用 `collie` CLI，打不开 Web 控制台
- 在加入了 hub 的电脑上，`collie` CLI 会自动用 `node.json` 里的地址和令牌，所以跑在那里的 worker 也能 `collie report`

> 内网里是明文 HTTP。跨网络使用请放在 VPN / Tailscale 或 HTTPS 反向代理后面（这时用 `--url https://…` 让二维码指向它）。

**从源码安装**（需要 Go 1.25+ 和 git）：clone 下来在仓库里运行同一个脚本，它用本地源码编译、不下载，参数和上面一样：

```bash
git clone https://github.com/lihuanyu0w0/collie.git ~/collie && cd ~/collie
sh docs/install.sh hub                                          # 做 hub
sh docs/install.sh node --hub http://<hub>:7788 --code <配对码>   # 加入电脑
```

以后 `git pull` 再跑一次就是升级。

只想装命令、不装服务：`curl -fsSL https://lihuanyu0w0.github.io/collie/install.sh | sh`。指定版本或目录：`COLLIE_VERSION=v0.2.0`、`COLLIE_BIN_DIR=…`。脚本在 [docs/install.sh](docs/install.sh)。

## 两种用法

**直接管 agent**：新建、查看、接管任意一台电脑上的 agent 或终端。终端被包装成对话视图；agent 停在确认框时，会变成一张决策卡片。

**和 Collie 对话（super-agent）**：你只和一个入口说话。Collie 的 Router 理解上下文，把活拆给 worker（Claude / Codex / Antigravity，各自在独立的 herdr 工作区里）并行去做，结果以事件的形式回到对话里，只在需要你决定或验收时出声。

侧栏按电脑分组列出**你自己的** agent；Collie 派出的 worker 和 Router 收在单独的「Collie 的 worker」组里（默认折叠），两者互不干扰。桌面端打开时回到上次的页面，手机端首页是列表。

## Super-agent 设计

参考 DeepSeek 的 Super-agent 架构和 Anthropic 的 *Building always-on agents*：

```
          ┌──────── 你（Web 对话 / 飞书 / GitHub / 定时器 …）
          ▼
   Trigger 收件箱 ── 快路径：来源策略 respond / candidate / ignore
          │
          ├── 订阅了线程的 worker ◀── 直达，不经过 Router
          ▼
   Router（常驻 session，只理解、分派、回复）
          │  collie spawn / send / reply / timer …
          ▼
   Worker × N（隔离的 herdr 工作区，干活，collie report 汇报）
```

- **Router / Worker 分层**：Router 只和人说话、分派任务，不亲自干活；回复限制在 100 词以内（可调）。Router 用轻量模型就够，worker 才需要最强的模型
- **Trigger**：聊天、webhook、定时器、worker 状态变化都是 Trigger。程序先按来源策略过滤（快路径），再交给 Router 判断（慢路径）。未经请求的外部事件默认是 `candidate`：只有 Router 的记忆或你的规则覆盖到时才行动
- **线程订阅**：worker 订阅某个线程（Issue、群聊话题）后，后续事件直达它
- **批量投递**：session 忙或停在确认框时事件会积压，空闲后合并成一批投递
- **Inline & Spill**：超过 700 字的事件只内联开头，其余用 `collie show <id>` 按需取回；所有东西都有 ID，可以顺着链条回溯
- **异步启动**：`collie spawn` 立即返回，worker 的汇报、完成、阻塞都作为事件回来
- **Watchdog**：worker 长时间屏幕无变化会被标记为卡住并通知 Router；Router 自己出错（凭证、地区限制、额度）会直接显示在对话里
- **状态与计算分离**：所有状态存在 SQLite，Router 可以随时重启，读 `memory.md` 恢复上下文，排队的事件不会丢
- **Outbox**：可选的飞书 / Slack 机器人通知，20 秒合并一次，安静模式只在需要你时推送

## CLI

Router 和 worker 通过同一个二进制和 Collie 通信（你也可以用）：

```bash
collie reply "…"                                   # Router → 你（≤100 词）
collie spawn [-kind claude] [-machine mac] [-cwd ~/x] [-name n] [-thread t] [-subscribe] "任务"
collie send <worker> "追加指令"
collie report "结果或需要你决定的问题"              # worker → Router（按 HERDR_PANE_ID 识别自己）
collie subscribe [--off] <worker> <thread>
collie stop <worker>
collie show <trigger | thread | worker>            # 取回被截断的内容
collie list
collie timer add [--thread T] [--to worker] "daily 09:00 | every 2h | in 30m | at 2026-11-18 09:00" "内容"
collie timer ls | rm <id>
collie say "…"                                     # 以你的身份发给 Router
collie hook <source> "…"                           # 模拟一个外部事件
collie pair [--computer]                           # 打印手机配对二维码 / 电脑加入命令
collie node [--hub URL --code CODE] [--name mac]   # 把这台电脑接到 hub
```

## 接入外部事件

在「自动化 → 接入」里复制地址：

- GitHub：`/hooks/github?key=…`（Content type 选 application/json）
- 飞书 / Lark 事件订阅：`/hooks/feishu?key=…`（已处理 url_verification）
- 通用：`POST /hooks/<source>?key=…`，JSON `{"text","thread","from","id"}`，`X-Idempotency-Key` 去重

Webhook 立即返回 202，过滤和路由在收件箱里异步完成。

## 直接管 agent

- **新建 Agent**：选类型、名字、目录，可以顺手给它第一个任务
- **对话视图**：消息来自 agent 自己的会话记录——Claude `~/.claude/projects/*/<id>.jsonl`、Codex `~/.codex/sessions/**/rollout-*.jsonl`、Antigravity `~/.gemini/antigravity-cli/brain/<id>/…/transcript.jsonl`（桌面版在 `~/.gemini/antigravity`）。Antigravity 不向 herdr 报告会话 id，Collie 用它自己的会话索引按工作目录精确配对，配不上时宁可显示终端输出，也不会串到别的会话
- **决策卡片**：agent 停在确认框时，把问题用一句话讲清楚，选项变成列表；编号菜单发数字，光标菜单发方向键 + 回车。「查看终端」可以展开原始屏幕
- **屏幕 / 日志视图**：xterm.js 原样呈现，或自动换行的滚动日志（适合窄屏）
- **需要你**：blocked 的 agent 置顶，标签页标题带计数，后台时弹浏览器通知
- PWA

## 其他终端里的会话

在 iTerm、VS Code、tmux、SSH 里直接跑的 claude / codex / agy 不归 herdr 管，Collie 也会把它们列在侧栏「其他终端里的会话」，hub 和每台连上的电脑都会列出：

- **看**：按进程找到它正在写的会话记录（Claude 用 `~/.claude/sessions/<pid>.json`，Codex 按工作目录匹配 rollout，agy 用它自己的索引），以只读对话的形式实时显示
- **接管**：Collie 没法往别的终端里打字，所以「在 herdr 里接管」会先结束原来的进程（相当于 Ctrl-C），再在 herdr 里用同一个会话继续（`claude --resume`、`codex resume`、`agy --conversation`）。之后它就是普通 agent，可以在手机上对话、处理确认

## 偏好设置

侧栏底部的齿轮。只影响当前浏览器：主题（跟随系统 / 浅色 / 深色）、对话文字大小、打开 agent 时默认显示对话还是屏幕、Enter 还是 ⌘Enter 发送、是否折叠连续的工具步骤、浏览器通知。

## herdr 版本差异

Collie 同时支持 herdr 0.8 和 0.9：

- 0.8 只能按 pane id 找 agent，而且 agent 一启动完就忘了名字。Collie 自己记着它启动的每个 agent 叫什么（存在 `herd.db`），调用前换成 pane id
- 0.8 有时一直停在 `launch_pending`，不接受 `agent.prompt`。这时 Collie 确认 agent 空闲、屏幕上没有选择框以后，直接把消息贴进终端
- 0.8 会把 Claude 的「是否信任此文件夹」报告成空闲。Collie 会看一眼屏幕，有选择框就当作「等你确认」处理

## 参数与文件

`collie`（等同 `collie serve`）启动 hub，打印带主令牌的地址和手机配对二维码。前端（含 xterm.js）全部内嵌在二进制里，内网不需要访问 CDN。

| 参数 | 环境变量 | 默认值 |
|---|---|---|
| `-addr` | `COLLIE_ADDR` | `0.0.0.0:7788` |
| `-socket` | `HERDR_SOCKET_PATH` | `~/.config/herdr/herdr.sock` |
| `-token` | `COLLIE_TOKEN` | 自动生成（主令牌） |
| `-url` | `COLLIE_URL` | 自动探测内网地址；二维码和配对命令用它 |
| | `COLLIE_HOME` | `~/.collie` |

> 拿到令牌的人就能控制你的终端。主令牌只给自己用；其他设备都通过配对拿各自的令牌。

### `~/.collie`

| 路径 | 内容 |
|---|---|
| `herd.db` | SQLite（WAL）：Trigger 收件箱、线程、worker、对话、定时器、设置、已配对设备（只存令牌哈希） |
| `node.json` | 仅 node：hub 地址、本机 id 和令牌 |
| `token` | 访问令牌 |
| `addr` | 当前监听地址，供 CLI 使用 |
| `router/` | Router 的工作目录：`ROUTER.md`（操作手册，同时写成 CLAUDE.md / AGENTS.md / GEMINI.md）、`memory.md`（长期记忆） |
| `bin/collie` | 用 `go run` 启动时复制出的稳定 CLI 路径 |

## 从源码构建

需要 Go 1.25+。

```bash
go build -o bin/collie .
./bin/collie                       # 监听 0.0.0.0:7788
go test ./...
scripts/release.sh v0.1.0 --dry    # 交叉编译四个平台到 dist/
```

## 代码结构

```
浏览器 ──HTTP + SSE──▶ collie hub（单个 Go 二进制，前端 embed）──NDJSON──▶ herdr.sock
                         ├─ 会话记录增量解析（JSONL）
                         ├─ Super：收件箱 → 路由 → 投递（SQLite）
                         └─ yamux 隧道 ◀── collie node（herdr 流 + 本机 pane API）
```

```
main.go              入口：只有版本号和 collie.Main
internal/collie/     hub、node 和 CLI（一个包）
web/                 前端：原生 HTML/CSS/JS，无构建步骤；embed.go 用 go:embed 打进二进制
docs/                install.sh 和主页
scripts/release.sh   交叉编译和发布
```

`internal/collie/` 里：

- `herdr.go`：herdr socket API 客户端
- `fleet.go`：多台电脑的 herdr 路由（id 后缀、列表合并）
- `hub.go` / `node.go`：设备配对与令牌、二维码、内网地址、隧道、请求转发
- `external.go` / `desktop.go`：其他终端和桌面 app 里的 agent 进程、会话记录定位、只读对话流、接管
- `server.go`：鉴权、状态 SSE、pane 屏幕/日志 SSE、输入、启动 agent
- `chat.go` / `agy.go`：会话记录解析（Claude / Codex / Antigravity）、决策卡片识别、对话 SSE
- `store.go` / `db.go`：super-agent 的数据模型和 SQLite 持久化
- `super.go` / `superapi.go` / `cli.go`：Router 与 worker 的调度循环、HTTP API、webhook、CLI 与 Router 手册
