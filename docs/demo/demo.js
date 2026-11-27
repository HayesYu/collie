// Collie demo mode: the real web app, driven by made-up data in the browser.
// It replaces fetch("/api/…") and EventSource before app.js loads, so every
// screen works without a hub. Nothing leaves the page.
(() => {
  "use strict";
  window.__collieDemo = true;
  window.__collieDemoHtmlUrl = (p) => (/bench-pagination\.html$/i.test(p || "") ? "bench-pagination.html" : "");
  const now = Date.now();
  const ago = (min) => new Date(now - min * 60000).toISOString();
  const later = (min) => new Date(Date.now() + min * 60000).toISOString();

  // ───────────────────────────── the scene
  const machines = [
    { id: "", name: "devbox", os: "linux/amd64", home: "/home/alex", local: true, online: true, version: "v0.3.9", agents: ["claude", "codex", "agy", "doubao", "cursor"] },
    { id: "macbook", name: "macbook", os: "darwin/arm64", home: "/Users/alex", online: true, version: "v0.3.9", agents: ["claude", "codex"] },
  ];
  const agent = (o) => ({ workspace_label: o.name, title: "", is_agent: true, machine: o.pane_id.includes("@") ? o.pane_id.split("@")[1] : "", ...o });
  const agents = [
    agent({ pane_id: "w1:p1", workspace_id: "w1", name: "collie", kind: "claude", status: "idle", cwd: "/home/alex/.collie/router" }),
    agent({ pane_id: "w2:p1@macbook", workspace_id: "w2@macbook", name: "claude-1", kind: "claude", status: "working", cwd: "/Users/alex/code/shop-web", topic: "首页加暗色模式", run: ["opus", "high"] }),
    agent({ pane_id: "w3:p1", workspace_id: "w3", name: "codex-1", kind: "codex", status: "idle", cwd: "/home/alex/code/shop-api", topic: "修复 /orders 分页的边界问题", run: ["gpt-6.1-sol", "high"] }),
    agent({ pane_id: "w4:p1", workspace_id: "w4", name: "agy-1", kind: "agy", status: "waiting", cwd: "/home/alex/code/shop-api", topic: "整理数据库迁移脚本", run: ["", ""] }),
    agent({ pane_id: "w5:p1", workspace_id: "w5", name: "fix-flaky-test", label: "查分页单测", kind: "codex", status: "working", cwd: "/home/alex/code/shop-api", run: ["gpt-6.1-sol", "medium"] }),
    agent({ pane_id: "w6:p1@macbook", workspace_id: "w6@macbook", name: "release-notes", label: "写v1.4日志", kind: "claude", status: "done", cwd: "/Users/alex/code/shop-web", run: ["sonnet", "low"] }),
  ];
  const shells = [
    { pane_id: "w7:p1", workspace_id: "w7", workspace_label: "logs", name: "logs", kind: "", status: "shell", cwd: "/home/alex/code/shop-api", title: "", is_agent: false, machine: "" },
  ];
  let lastReadEntryId = "";
  const state = () => ({ ok: true, agents, shells, machines, ...(lastReadEntryId ? { last_read_entry_id: lastReadEntryId } : {}) });

  const workers = [
    { name: "fix-flaky-test", label: "查分页单测", kind: "codex", cwd: "/home/alex/code/shop-api", task: "找出 TestOrdersPagination 偶发失败的原因并修好", thread: "chat:main", pane_id: "w5:p1", workspace_id: "w5", machine: "", created_by: "collie", created_at: ago(14), status: "working", run: ["gpt-6.1-sol", "medium"] },
    { name: "release-notes", label: "写v1.4日志", kind: "claude", cwd: "/Users/alex/code/shop-web", task: "根据 v1.3..main 的提交写 v1.4 发布说明", thread: "chat:main", pane_id: "w6:p1@macbook", workspace_id: "w6@macbook", machine: "macbook", created_by: "collie", created_at: ago(14), status: "done", last_report: "CHANGELOG 已更新：6 条改动，2 条需要注意的升级事项。", report_at: ago(6), run: ["sonnet", "low"] },
  ];
  let seq = 100;
  const dismissed = new Set();
  const entry = (role, text, o = {}) => ({ id: "e" + (++seq), role, thread: "chat:main", text, at: o.at || new Date().toISOString(), ...o });
  const earlierEntries = [
    { id: "e62", role: "user", thread: "chat:main", text: "帮我查一下 shop-api 里优惠券接口 SCHEMA_AND_ID 校验报错的栈，看看是不是上周迁移改出来的。", at: ago(60 * 52) },
    { id: "e63", role: "collie", thread: "chat:main", text: "查到了：`coupon_validator.go` 在空购物车时跳过了 `SCHEMA_AND_ID` 默认值填充，导致老客户端传空串时返回 400。已经在 `~/code/shop-api/internal/coupons/validator.go` 补上兜底并加了单测。", at: ago(60 * 51.8) },
    { id: "e64", role: "event", thread: "chat:main", tone: "done", ref: "coupon-fix", text: "coupon-fix 回报：已修复 SCHEMA_AND_ID 空值校验并补单测。", at: ago(60 * 51.5) },
    { id: "e68", role: "user", thread: "chat:main", text: "结算页的缓存过期配置是不是要改名？顺便把压测基线跑一下。", at: ago(60 * 38) },
    { id: "e69", role: "collie", thread: "chat:main", text: "对，`cart.ttl` 统一改成 `cart.expire_after`（兼容旧字段一个版本）。1020 并发压测跑完了，P99 从 142ms 降到 89ms，报告在 [压测看板](https://metrics.example.com/shop-api/run-1020) 可以直接看。", at: ago(60 * 37.5) },
    { id: "e74", role: "user", thread: "chat:main", text: "商品图片首屏加载有点慢，让 macbook 上的 Claude 改成懒加载试试。", at: ago(60 * 26) },
    { id: "e75", role: "collie", thread: "chat:main", text: "已经改好合入了：首屏大图保留高优先级，折叠区下方的商品卡片全部加上 `loading=\"lazy\"`，首屏体积少了约 30%。", at: ago(60 * 25.4) },
    { id: "e80", role: "user", thread: "chat:main", text: "明天早上提醒我看一下 v1.4 的发布清单。", at: ago(60 * 18) },
    { id: "e81", role: "collie", thread: "chat:main", text: "好，明早 09:00 提醒你。", at: ago(60 * 18) },
  ];
  const entries = [
    entry("user", "订单接口的分页测试偶尔会挂，帮我查一下。顺便把 v1.4 的发布说明写了。\n\n附件：\n/home/alex/.collie/uploads/20261119/a1b2c3-ci-failure.png\n/home/alex/.collie/uploads/20261119/d4e5f6-query-plan.png", { at: ago(15) }),
    entry("collie", "好，分两路并行：\n\n- **fix-flaky-test**（Codex · devbox）查 `TestOrdersPagination` 为什么偶发失败\n- **release-notes**（Claude · macbook）按 v1.3 以来的提交写发布说明\n\n有结果我再告诉你。", { at: ago(14.5), claps: 2 }),
    entry("event", "派出 fix-flaky-test（codex · devbox）", { tone: "spawn", ref: "fix-flaky-test", at: ago(14) }),
    entry("event", "派出 release-notes（claude · macbook）", { tone: "spawn", ref: "release-notes", at: ago(14) }),
    entry("user", "另外首页首屏好像有点慢，有空看看。", { at: ago(7.2) }),
    entry("collie", "顺便问一下，`shop-api/migrations` 整理完了（41 → 33 个文件），那 3 个从没在生产跑过的旧回滚脚本怎么处理？", { at: ago(6.5), options: ["移到 archive 留档", "直接删掉"] }),
    entry("event", "release-notes 回报：CHANGELOG 已更新：6 条改动，2 条需要注意的升级事项。", { tone: "done", ref: "release-notes", at: ago(6) }),
    entry("event", "fix-flaky-test 回报：已定位 TestOrdersPagination 偶发失败根因并补齐第二排序键与压测。", { tone: "done", ref: "fix-flaky-test", at: ago(5.8) }),
    entry("collie", "发布说明写好了：6 条改动，其中 **配置项改名** 和 **Node 22 起步** 需要在升级说明里提醒。要我直接开 PR 吗？升级指南放在团队文档（https://docs.example.com/shop-web/upgrade-guide-v1-4#node-22，第 2 节）。详见 ~/code/shop-web/CHANGELOG.md\n\n顺带附上首页明暗主题验收宽截图与架构图：~/code/shop-web/docs/preview-dark.png 、~/code/shop-web/docs/preview-light.png 、~/code/shop-web/docs/architecture.svg\n\n分页测试那边还在跑，初步看是按 `created_at` 排序时同一秒内的订单顺序不稳定。首屏慢的事等它跑完我再派人看。", { at: ago(5.5), re: { worker: "release-notes", who: "release-notes", text: "CHANGELOG 已更新：6 条改动，2 条需要注意的升级事项。" }, options: ["直接开 PR", "我先看一眼改动", "先不用"] }),
    entry("collie", "**fix-flaky-test** 找到根因了：同一秒创建的订单只按 `created_at` 排序，顺序不稳定。已在 `~/code/shop-api/internal/orders/list.go:362-385` 补上 `id DESC` 作为第二排序键：\n\n```go\nconst listSQL = `\n\tSELECT id, user_id, status, total_cents, created_at\n\tFROM orders\n\tWHERE user_id = $1\n\tORDER BY created_at DESC, id DESC\n\tLIMIT $2 OFFSET $3`\n```\n\n200 轮压测对比图表：`~/code/shop-api/bench-pagination.html`，原始分位明细表：`~/code/shop-api/docs/orders-benchmark.csv`，全接口压测与索引复盘长报告已整理到 `~/code/shop-api/docs/perf-audit-v1.4.md`（含 8 列宽表，支持顶栏下载、复制原文或新标签宽屏阅读），压测看板见 `https://metrics.example.com/shop-api/run-1020`，暗色首屏基准图见 `~/code/shop-web/docs/preview-dark.png`。\n\n另外冷备归档路径 `hdfs://cluster-east/warehouse/shop_orders/dt=2026-11-20`、`/api/orders` 路由签名与本地复现命令 `go test -count=1 ./...` 均保持不变；顺带扫到已取消（`cancelled`）的订单目前仍计入列表页眉的 `total_count`，不确定前端是不是故意这样展示的，你空了看一眼。", { at: ago(2.5), files: ["~/code/shop-api/bench-pagination.html", "~/code/shop-api/docs/orders-benchmark.csv"], claps: 5 }),
    entry("user", "先把 `orders` 复合索引的上线窗口排到今晚 23:00，用 `CONCURRENTLY` 建索引避免锁表。", { at: ago(2.0) }),
    entry("collie", "明白，已经按 `CREATE INDEX CONCURRENTLY` 整理进今晚 23:00 的变更单，建完自动跑一遍 200 轮翻页回归。", { at: ago(1.8) }),
    entry("user", "另外把旧的单列索引先留 3 天观察慢查询，确认 P99 稳在 30ms 内再下线。", { at: ago(1.4) }),
    entry("collie", "收到，首页首屏慢的事先记下了；等分页变更落完，我会先量手机端 LCP 和资源瀑布图，再拆具体优化项。", { at: ago(1.1) }),
    entry("user", "好，优先排查手机端首屏，桌面端目前体感还可以。", { at: ago(0.8) }),
    entry("collie", "另外巡检扫到 1 项需你线下处理的待办：测试环境证书明天到期，空了在控制台点一下续签即可（办完直接点「已处理」，不用回复我）。", { at: ago(0.4), check: true }),
  ];
  entries[8].re.entry = entries[6].id;
  entries[9].re = { entry: entries[1].id, who: "Collie", text: "好，分两路并行：fix-flaky-test 查 TestOrdersPagination 为什么偶发失败" };
  entries[10].re = { entry: entries[9].id, who: "Collie", text: "fix-flaky-test 找到根因了：同一秒创建的订单只按 created_at 排序，顺序不稳定。" };
  entries[11].re = { entry: entries[10].id, who: "你", text: "先把 orders 复合索引的上线窗口排到今晚 23:00，用 CONCURRENTLY 建索引避免锁表。" };
  entries[12].re = { entry: entries[9].id, who: "Collie", text: "fix-flaky-test 找到根因了：同一秒创建的订单只按 created_at 排序，顺序不稳定。" };
  entries[13].re = { entry: entries[4].id, who: "你", text: "另外首页首屏好像有点慢，有空看看。" };
  entries[14].re = { entry: entries[13].id, who: "Collie", text: "收到，首页首屏慢的事先记下了；等分页变更落完，我会先量手机端 LCP 和资源瀑布图，再拆具体优化项。" };
  const pinnedIds = [entries[1].id];
  const pendingSummary = (t) => String(t || "").replace(/```[\s\S]*?```/g, " ").replace(/\[image:[^\]]+\]/g, " ")
    .replace(/\[([^\]\n]+)\]\([^)\n]+\)/g, "$1").split("\n")
    .map((l) => l.replace(/^\s*(?:[#>*•\-]+|\d+[.)])\s+/, "").replace(/\*\*|__|`/g, "").trim())
    .filter(Boolean).join(" ").slice(0, 120);
  const humanSummary = (e) => {
    let t = String(e.text || "");
    if (e.role === "user") {
      const att = t.lastIndexOf("附件：\n");
      if (att >= 0) t = (t.slice(0, att).trim() + " 📎").trim();
    }
    return pendingSummary(t);
  };
  function computePinned() {
    const all = [...earlierEntries, ...entries];
    return pinnedIds.map((id) => all.find((e) => e.id === id)).filter(Boolean)
      .map((e) => ({ id: e.id, at: e.at, role: e.role, summary: humanSummary(e) }));
  }
  function computePending() {
    const all = [...earlierEntries, ...entries];
    const out = [];
    for (let i = 0; i < all.length; i++) {
      const e = all[i];
      if (e.role !== "collie" || (!e.options?.length && !e.check) || dismissed.has(e.id) || e.pick) continue;
      let resolved = false;
      const chain = new Set([e.id]);
      for (let j = i + 1; j < all.length; j++) {
        const next = all[j];
        const reId = next.re?.entry || "";
        if (next.role === "user") {
          if (reId && chain.has(reId)) { resolved = true; break; }
        } else if (next.role === "collie" && reId && chain.has(reId)) {
          chain.add(next.id);
        }
      }
      if (!resolved) out.push({ id: e.id, at: e.at, summary: pendingSummary(e.text), ...(e.options?.length ? { options: e.options } : {}), ...(e.check ? { check: true } : {}) });
    }
    return out;
  }
  let routerRun = { kind: "claude", model: "sonnet", effort: "", args: "", ask_permissions: false };
  const routerSig = (s) => ({
    kind: s.router_kind === "antigravity" ? "agy" : (s.router_kind || "claude"),
    model: s.router_model || "",
    effort: s.router_effort || "",
    args: (s.router_args || []).join(" "),
    ask_permissions: !!s.ask_permissions,
  });
  const routerPending = () => {
    const cur = routerSig(settings.settings);
    return cur.kind !== routerRun.kind || cur.model !== routerRun.model || cur.effort !== routerRun.effort || cur.args !== routerRun.args || cur.ask_permissions !== routerRun.ask_permissions;
  };
  const sup = () => ({
    router: { alive: true, status: routerStatus, pane_id: "w1:p1", kind: routerRun.kind, context: 23, rotate_at: 70, model: routerRun.model, effort: routerRun.effort, pending: routerPending() },
    entries, pending: computePending(), pinned: computePinned(), ...(lastReadEntryId ? { last_read_entry_id: lastReadEntryId } : {}), workers, queued: 0, queued_router: 0, paused: !!settings.settings.paused, earlier: true,
    follow: { next: new Date(new Date().setHours(33, 0, 0, 0)).toISOString(), next_text: "复查首页暗色模式的回归测试" }, // a check-again tomorrow 09:00
    timers_count: timers.filter((t) => t.enabled).length,
    integrations_summary: {
      connected: [demoFeishu, demoGitHub, demoGitCLI].filter((x) => x.enabled && x.health.state === "connected").length,
      error: [demoFeishu, demoGitHub, demoGitCLI].filter((x) => x.enabled && x.health.state === "error").length,
      total: 3,
    },
    peers_summary: {
      total: demoPeers.length,
      active: demoPeers.filter((x) => x.status === "active").length,
      online: demoPeers.filter((x) => x.status === "active").length,
      paused: demoPeers.filter((x) => x.status === "paused").length,
      pending_incoming: demoPeers.filter((x) => x.status === "pending_incoming").length,
      pending_outgoing: demoPeers.filter((x) => x.status === "pending_outgoing").length,
      enabled: demoPeersEnabled,
    },
  });
  let demoPeersEnabled = true;
  let demoPeerHubName = "Alex的边牧";
  let demoPeerCardCode = "DEMO2026CARD";
  const demoPeerHubID = "hub_4c8e91a2d301";
  const demoPeers = [
    {
      id: "sam",
      hub_id: "hub_9f2a4c8e1d7b",
      name: "Sam的边牧",
      addrs: ["http://192.168.1.42:7788"],
      primary_addr: "http://192.168.1.42:7788",
      mode: "advise",
      status: "active",
      protocol_ver: 1,
      today_count: 2,
      last_ok_at: ago(4),
      last_active_at: ago(6),
      created_at: ago(60 * 24 * 3),
      updated_at: ago(6),
    },
    {
      id: "lin",
      hub_id: "hub_3b7e1d5c8a20",
      name: "Lin的边牧",
      addrs: ["http://192.168.1.58:7788"],
      primary_addr: "http://192.168.1.58:7788",
      mode: "notify",
      status: "active",
      protocol_ver: 1,
      today_count: 1,
      last_ok_at: ago(18),
      last_active_at: ago(28),
      created_at: ago(60 * 24 * 2),
      updated_at: ago(28),
    },
  ];
  const demoPeerBlocklist = [];
  const demoFeishu = {
    kind: "feishu",
    name: "飞书机器人",
    subtitle: "长连接双向对话 · 零公网门槛",
    description: "在飞书私信或群里 @Collie 直接派活，回复与选项按钮自动同步，点一下就能拍板。",
    enabled: true,
    configured: true,
    secret_set: true,
    config: {
      region: "cn",
      app_id: "cli_demo84f00009f21",
      allow_open_ids: ["ou_demo9a2f7c10"],
      allow_open_id: "ou_demo9a2f7c10",
      feedback_chat_ids: ["oc_demo7c3e91a2"],
      feedback_chat_id: "oc_demo7c3e91a2",
      group_mention_only: true,
      instant_chat_sync: true,
    },
    health: {
      state: "connected",
      detail: "长连接在线",
      last_ok_at: ago(3),
      last_active_at: ago(3),
      today_count: 6,
    },
    extra: {
      pairing: { active: false },
      bot_name: "Alex的边牧 · Collie",
      onboard_presets: {
        owner_name: "Alex",
        default_name: "Alex的边牧 · Collie",
        name_template: "{user}的边牧 · Collie",
        fallback_name: "我的边牧 · Collie",
        default_desc: "帮你放牧一群 Agent 的边牧｜盯进度、收结论、只把关键决策端给你 · https://lihuanyu0w0.github.io/collie/",
        homepage_url: "https://lihuanyu0w0.github.io/collie/",
        default_avatar: "cobalt",
        limits: {
          cn: { name_min: 2, name_max: 32, desc_max: 120 },
          intl: { name_min: 2, name_max: 64, desc_max: 240 },
        },
        avatars: [
          { id: "cobalt", label: "钴蓝领巾", color: "#1d4ed8", preview: "avatars/cobalt.png", url: "https://lihuanyu0w0.github.io/collie/avatars/cobalt.png" },
          { id: "terracotta", label: "赤陶领巾", color: "#c2410c", preview: "avatars/terracotta.png", url: "https://lihuanyu0w0.github.io/collie/avatars/terracotta.png" },
          { id: "pine", label: "松绿领巾", color: "#15803d", preview: "avatars/pine.png", url: "https://lihuanyu0w0.github.io/collie/avatars/pine.png" },
          { id: "plum", label: "梅紫领巾", color: "#7e22ce", preview: "avatars/plum.png", url: "https://lihuanyu0w0.github.io/collie/avatars/plum.png" },
        ],
      },
    },
  };
  let demoFeishuOnboard = null;
  const demoGitHub = {
    kind: "github",
    name: "GitHub",
    subtitle: "PR · Issue · CI 主动追踪",
    description: "零公网增量拉取关注仓库与 @我 的动态，支持只提醒、自动调研出建议，或交给负责该 PR 的 Agent。",
    enabled: true,
    configured: true,
    secret_set: false,
    config: {
      auth_mode: "gh",
      watch_mentions: true,
      repos: ["example-dev/collie", "example-dev/shop-api"],
      events: { pr: true, issue: true, ci: true },
      handle_mode: "advise",
      configured: true,
    },
    health: {
      state: "connected",
      detail: "已连通 · @alex",
      last_ok_at: ago(5),
      last_active_at: ago(12),
      today_count: 4,
    },
    extra: { engine: "gitforge", term_pr: "PR", supports_gh: true, supports_pat: true, user: "alex" },
  };
  const demoGitCLI = {
    kind: "git-cli",
    name: "自建 Git CLI",
    subtitle: "MR 评审 · 评论 @我 · CI 追踪",
    description: "通过 ~/.collie/providers/*.json 可插拔声明式 CLI 适配器复用本机登录态，免公网追踪 MR 评审与失败检查。",
    enabled: false,
    configured: false,
    secret_set: false,
    config: {
      auth_mode: "cli",
      watch_mentions: true,
      repos: ["team-core/service-api"],
      events: { mr: true, mention: true, ci: true },
      handle_mode: "advise",
      configured: false,
    },
    health: {
      state: "unconfigured",
      today_count: 0,
    },
    extra: {
      engine: "gitforge",
      term_pr: "MR",
      provider_type: "cli",
      cli_label: "git-forge-cli",
      provider_path: "~/.collie/providers/git-cli.json",
    },
  };
  let routerStatus = "idle";

  // Conversations, by pane. Tool steps read as one quiet line in the app.
  const tool = (name, input, output, error) => ({ kind: "tool", name, input, output, error });
  const chats = {
    "w2:p1@macbook": [
      { kind: "user", text: "[image:~/.collie/images/0d3a9e5c71b2f4e8a6c1.png]\n首页加一个暗色模式：跟随系统，右上角可以手动切换，记住用户的选择。参考这张图的配色。" },
      { kind: "assistant", text: "先看看现在的样式是怎么组织的。" },
      tool("Read", "src/styles/theme.css", "48 lines"),
      tool("Grep", "color: #", "src/components/Header.tsx:12\nsrc/components/Card.tsx:31\nsrc/pages/Home.tsx:77"),
      tool("Read", "src/components/Header.tsx", "64 lines"),
      { kind: "assistant", text: "颜色大多已经走 CSS 变量，只有三处写死了。计划：\n\n1. `theme.css` 里补一套 `[data-theme=dark]` 变量，并在 `prefers-color-scheme: dark` 下默认启用\n2. 三处硬编码颜色改成变量\n3. Header 加一个切换按钮，选择存进 `localStorage`" },
      tool("Edit", "src/styles/theme.css", "+31 −2"),
      tool("Edit", "src/components/Card.tsx", "+1 −1"),
      tool("Edit", "src/pages/Home.tsx", "+2 −2"),
    ],
    "w3:p1": [
      { kind: "user", text: "/orders?page=N 在最后一页会多返回一条重复记录，修一下并补测试。" },
      tool("Read", "internal/orders/list.go", "112 lines"),
      tool("Read", "internal/orders/list_test.go", "86 lines"),
      { kind: "assistant", text: "找到了：`offset` 用的是 `page * size`，但 `page` 从 1 开始，最后一页会和前一页重叠一条。改成 `(page-1) * size`，再加一个跨页不重复的测试。" },
      tool("Edit", "internal/orders/list.go", "+3 −1"),
      tool("Edit", "internal/orders/list_test.go", "+24"),
      tool("Bash", "go test ./internal/orders/...", "ok  shop-api/internal/orders  0.41s"),
      { kind: "assistant", text: "改好了，跨页去重测试全部通过。" },
    ],
    "w4:p1": [
      { kind: "user", text: "migrations/ 里有不少重复和废弃的脚本，整理一下，保证从空库能一路升到最新。" },
      tool("Bash", "ls migrations | wc -l", "41"),
      tool("Bash", "make db-reset && make migrate", "ok: 41 migrations applied"),
      tool("Edit", "migrations/README.md", "+18"),
      { kind: "assistant", text: "整理完了：\n\n- 合并了 6 个只加索引的小脚本，删掉 3 个从没在生产跑过的回滚脚本\n- 从空库升级到最新验证通过（41 → 33 个文件）\n- `migrations/README.md` 写了命名规则和怎么加新脚本" },
    ],
    "w5:p1": [
      { kind: "user", text: "[collie] 找出 TestOrdersPagination 偶发失败的原因并修好。完成后 collie report。" },
      tool("Bash", "go test -run TestOrdersPagination -count=50 ./internal/orders", "--- FAIL: 3 of 50"),
      tool("Read", "internal/orders/list.go", "112 lines"),
      { kind: "assistant", text: "50 次里挂 3 次。测试在同一秒内插入多条订单，而查询只按 `created_at` 排序，同一秒的顺序数据库不保证。加上 `id` 作为第二排序键。" },
      tool("Edit", "internal/orders/list.go", "+1 −1"),
      tool("Bash", "go test -run TestOrdersPagination -count=200 ./internal/orders"),
    ],
    "w6:p1@macbook": [
      { kind: "user", text: "[collie] 根据 v1.3..main 的提交写 v1.4 发布说明。完成后 collie report。" },
      tool("Bash", "git log --oneline v1.3..main", "27 commits"),
      tool("Edit", "CHANGELOG.md", "+38"),
      { kind: "assistant", text: "CHANGELOG 已更新：6 条改动，2 条需要注意的升级事项（配置项 `api_url` 改名为 `api_base`；最低 Node 版本升到 22）。" },
    ],
    "w1:p1": [
      { kind: "user", text: "[collie] 1 个事件 · 对话 · 订单接口的分页测试偶尔会挂……" },
      tool("Bash", 'collie spawn -kind codex -name fix-flaky-test -label "查分页单测" "…"', "spawned fix-flaky-test on devbox"),
      tool("Bash", 'collie spawn -kind claude -machine macbook -name release-notes -label "写v1.4日志" "…"', "spawned release-notes on macbook"),
      tool("Bash", "collie reply \"好，分两路并行……\"", "ok"),
    ],
  };
  const lives = {
    "w2:p1@macbook": { status: "working", tail: "Editing src/components/Header.tsx…", meta: "42s · ↓ 1.8k tokens" },
    "w5:p1": { status: "working", tail: "Running go test -run TestOrdersPagination -count=200 ./internal/orders", meta: "18s" },
  };
  const approvals = {};
  const demoLogLines = Array.from({ length: 58 }, (_, i) => {
    const mm = String(Math.floor(i / 4)).padStart(2, "0"), ss = String((i * 13) % 60).padStart(2, "0");
    if (i % 9 === 0) return `12:${mm}:${ss} \x1b[33mWARN\x1b[0m  slow query orders_by_created_at (${110 + i}ms)`;
    return `12:${mm}:${ss} \x1b[32mINFO\x1b[0m  GET /orders?page=${(i % 6) + 1} 200 ${12 + (i % 19)}ms`;
  }).join("\n");
  const screens = {
    "w2:p1@macbook": "╭───────────────────────────────────────────────╮\n│ ✻ Claude Code                                 │\n│   ~/code/shop-web                             │\n╰───────────────────────────────────────────────╯\n\n> 首页加一个暗色模式：跟随系统，右上角可以手动切换，记住用户的选择。\n\n● 先看看现在的样式是怎么组织的。\n\n● Read(src/styles/theme.css)\n  ⎿  Read 48 lines\n\n● Grep(color: #)\n  ⎿  src/components/Header.tsx:12\n     src/components/Card.tsx:31\n     src/pages/Home.tsx:77\n\n● Read(src/components/Header.tsx)\n  ⎿  Read 64 lines\n\n● 颜色大多已经走 CSS 变量，只有三处写死了。计划：\n  1. theme.css 里补一套 [data-theme=dark] 变量，默认跟随系统\n  2. 三处硬编码颜色改成变量\n  3. Header 加切换按钮，选择存进 localStorage\n\n● Update(src/styles/theme.css)\n  ⎿  Updated with 31 additions and 2 removals\n     + :root[data-theme=\"dark\"] {\n     +   --surface: #111214;\n     +   --ink: #ededeb;\n     + }\n\n● Update(src/components/Card.tsx)\n  ⎿  Updated with 1 addition and 1 removal\n\n● Update(src/pages/Home.tsx)\n  ⎿  Updated with 2 additions and 2 removals\n\n✻ Editing src/components/Header.tsx… (42s · ↓ 1.8k tokens)\n\n╭───────────────────────────────────────────────╮\n│ >                                             │\n╰───────────────────────────────────────────────╯\n  ? for shortcuts                    opus · high",
    "w7:p1": "alex@devbox:~/code/shop-api$ tail -n 60 -f logs/api.log\n" + demoLogLines + "\nalex@devbox:~/code/shop-api$ ",
  };
  const wrapCols = (s, max = 88) => s.split("\n").flatMap((line) => {
    const out = [];
    let cur = "", w = 0;
    for (const ch of line) {
      const cw = ch.codePointAt(0) > 0x2e7f ? 2 : 1;
      if (w + cw > max) { out.push(cur); cur = ch; w = cw; } else { cur += ch; w += cw; }
    }
    out.push(cur);
    return out;
  }).join("\n");
  const screenOf = (id) => screens[id] || wrapCols((chats[id] || []).filter((m) => m.kind !== "tool").map((m) => (m.kind === "user" ? "> " : "● ") + m.text).join("\n\n"));

  const external = [
    { id: "x1", kind: "claude", pid: 4242, cwd: "/Users/alex/code/notes", app: "iTerm", started: ago(90), session: "s1", title: "周报草稿", updated: ago(20), machine: "macbook", status: "idle", can_send: false, live: true },
    { id: "cd-2", kind: "claude", pid: 4310, cwd: "/Users/alex/code/shop-web", app: "Claude 桌面版", started: ago(240), session: "s2", title: "重构购物车状态管理", updated: ago(55), machine: "macbook", status: "idle", can_send: true, live: true },
  ];
  const history = [
    { id: "h1", kind: "codex", cwd: "/home/alex/code/shop-api", name: "codex-3", session: "s3", ended: ago(60 * 20), topic: "升级 Go 到 1.25" },
    { id: "h2", kind: "claude", cwd: "/Users/alex/code/shop-web", name: "claude-4", session: "s4", ended: ago(60 * 30), topic: "图片懒加载", machine: "macbook" },
  ];
  const extChats = {
    x1: [{ kind: "user", text: "根据这周的 commit 帮我写周报，分成已完成、进行中、下周计划。" }, { kind: "assistant", text: "**已完成**\n- 订单列表分页修复\n- 迁移脚本整理（41 → 33）\n\n**进行中**\n- 首页暗色模式\n\n**下周计划**\n- v1.4 发布" }],
    "cd-2": [{ kind: "user", text: "购物车的状态现在分散在三个 context 里，合并成一个 store。" }, { kind: "assistant", text: "合并完成，三个 context 换成了一个 `useCart` store，组件改动 9 处，测试全部通过。" }],
  };

  const triggers = [
    { id: "t31", source: "chat", thread: "chat:main", from: "you", action: "respond", status: "delivered", text: entries[0].text, at: ago(15), targets: ["collie"] },
    { id: "t32", source: "worker", thread: "chat:main", from: "release-notes", action: "respond", status: "delivered", text: "report from release-notes: CHANGELOG 已更新（v1.3..main 共 27 个提交）\n\n需要注意：\n- 配置项 cart.ttl 改名为 cart.expire_after（兼容旧字段一个版本）\n- 构建与运行最低 Node 版本升至 22\n\n主要改动：订单状态筛选、结算预计送达时间、空购物车券码兜底、首屏图片懒加载（体积约降 30%）、拼音搜索与依赖升级。", at: ago(6), targets: ["collie"] },
    { id: "t33", source: "github", thread: "github:alex/shop-api#58", from: "octo-dev", action: "candidate", status: "delivered", text: "issue_comment: 分页最后一页有重复数据，能看一下吗？@collie", at: ago(40), targets: ["collie"] },
    { id: "t34", source: "timer", thread: "chat:main", from: "", action: "respond", status: "delivered", text: "每天 9 点：检查各个任务的进展，有卡住的告诉我", at: ago(60 * 3), targets: ["collie"] },
  ];
  const demoOlderSpecs = [
    { source: "github", thread: "github:alex/shop-web#112", from: "maya", action: "candidate", status: "delivered", text: "review_comment: 首页骨架屏在窄屏下高度略抖动，建议固定卡片最小高度" },
    { source: "worker", thread: "chat:main", from: "fix-flaky-test", action: "respond", status: "delivered", text: "report from fix-flaky-test: 已定位订单列表分页排序缺少 id 兜底，正在补充回归单测" },
    { source: "github", thread: "github:alex/shop-api#54", from: "ci-bot", action: "notify", status: "delivered", text: "check_run: integration-tests 通过（耗时 3m 42s，0 flaky）" },
    { source: "timer", thread: "chat:main", from: "", action: "respond", status: "delivered", text: "每 2 小时巡检：检查进行中分支测试状态与阻塞项" },
    { source: "github", thread: "github:alex/shop-api#58", from: "octo-dev", action: "candidate", status: "delivered", text: "pull_request_review: 新增游标排序测试覆盖边界页，LGTM" },
    { source: "chat", thread: "chat:main", from: "you", action: "respond", status: "delivered", text: "把昨天合并的迁移脚本顺手整理一下，确认回滚路径干净" },
  ];
  for (let i = 0; i < 56; i++) {
    const sp = demoOlderSpecs[i % demoOlderSpecs.length];
    triggers.push({
      id: "t" + (35 + i),
      source: sp.source,
      thread: sp.thread,
      from: sp.from,
      action: sp.action,
      status: sp.status,
      text: `${sp.text}（记录 #${i + 1}）`,
      at: ago(60 * (4 + i)),
      targets: ["collie"],
    });
  }
  const threads = [
    { id: "chat:main", triggers: triggers.filter((t) => t.thread === "chat:main"), subscribers: [], last_at: ago(6) },
    { id: "github:alex/shop-api#58", triggers: triggers.filter((t) => t.thread === "github:alex/shop-api#58"), subscribers: ["codex-1"], last_at: ago(40) },
    { id: "github:alex/shop-web#112", triggers: triggers.filter((t) => t.thread === "github:alex/shop-web#112"), subscribers: ["claude-2"], last_at: ago(60 * 4) },
    { id: "github:alex/shop-api#54", triggers: triggers.filter((t) => t.thread === "github:alex/shop-api#54"), subscribers: [], last_at: ago(60 * 6) },
  ];
  const timers = [
    { id: "tm1", spec: "weekdays 09:00", text: "检查各个任务的进展，有卡住的告诉我", target: "", enabled: true, next_run: later(60 * 18), owner: "you" },
    { id: "tm2", spec: "in 30m", text: "看一下 fix-flaky-test 跑完没有", target: "", enabled: true, next_run: later(16), owner: "collie" },
  ];
  const memFile = (name, text, min, size) => ({ name, text, size: size ?? text.length * 2, mod: ago(min) });
  const memory = {
    dir: "~/.collie", up_front: 5734, messages: 2867, loaded: 8601, budget: 10240, dreaming: false, last_dream: ago(60 * 9), next_dream: later(60 * 15), dreams: [
      { name: "2026-11-18-0400.md", text: "从 fix-flaky-test 里记下了分页测试的坑，写进 shop-api.md；合并了两条重复的笔记，并沉淀出两条跨会话习惯\n\n- shop-api.md：同一秒内的订单按 created_at 排序不稳定，要加 id 兜底\n- agents.md：Codex 在 devbox 上跑 shop-api 的测试更稳\n- instructions.md：补充 2 条从近期对话识别的偏好（标记为 `(inferred)`），方便在记忆页随时核对或删掉\n- 归档已完成任务：dark-mode-fix.md 已移入 tasks/done/" },
      { name: "2026-11-16-0400.md", text: "归档了 CI 缓存提速任务，精简了 notes/machines.md 里过期的版本号\n\n- tasks/done/ci-cache.md：记录构建耗时从 9 分钟降到 4 分钟\n- notes/machines.md：更新 devbox 与 macbook 的可用工具清单" },
    ],
    files: [
      memFile("instructions.md", "# 你的约定\n- 发布说明用中文，每条一句话\n- 跑测试优先用 devbox，界面相关的活放 macbook\n- 动生产数据库前先问我\n- (inferred) 排查接口问题时先给根因和复现命令，再贴代码改动\n- (inferred) 合并 PR 前希望同步确认单元测试和明暗主题截图", 60 * 24, 1843),
      memFile("tasks/v1.4-release.md", "# v1.4 发布\n目标：整理 v1.3 以来的 27 个提交写发布说明（重点提醒配置项改名与 Node 22 起步），定位并修复 /orders 分页同一秒内记录重复的偶发失败（fix-flaky-test），回归通过后一起开 PR。\n- [x] 发布说明（release-notes）\n- [ ] 分页测试修复（fix-flaky-test）\n- [ ] 开 PR", 6, 2253),
      memFile("tasks/done/dark-mode-fix.md", "# 首页暗色模式修复\n- [x] 定位对比度问题\n- [x] 合入修复（ux-polish）\n已完成：11 月 18 日合入。", 50),
      memFile("tasks/done/ci-cache.md", "# CI 缓存提速\n- [x] 缓存依赖目录\n已完成：构建从 9 分钟降到 4 分钟。", 90),
      memFile("notes/index.md", "# 笔记索引\n- shop-api.md：订单服务，怎么跑测试、分页的坑\n- shop-web.md：前端，怎么起、发布说明放哪\n- agents.md：哪个 agent 在哪台电脑上好用\n- machines.md：devbox 和 macbook 各装了什么", 60 * 30, 1638),
      memFile("notes/shop-api.md", "# shop-api\n- 仓库 ~/code/shop-api（devbox），Go 1.25，测试 `make test`\n- 分页测试偶发失败：同一秒内的订单按 `created_at` 排序不稳定，排序要加 `id` 兜底（fix-flaky-test 查出来的）\n- 测试数据库用 docker 起，`make db`；跑完 `make db-down`\n- CI 只跑 `./internal/...`（截至 11-18）", 60 * 9),
      memFile("notes/shop-web.md", "# shop-web\n- 仓库 ~/code/shop-web（macbook），Vite + React，`pnpm dev`\n- 发布说明写在 CHANGELOG.md，按「需要注意 / 改动」两段\n- 首页明暗主题基准图：![首页暗色预览](~/code/shop-web/docs/preview-dark.png)", 60 * 30),
      memFile("notes/agents.md", "# agent 经验\n- Codex 在 devbox 上跑 shop-api 的测试更稳，Claude 偶尔会改错测试本身\n- 界面相关的活给 macbook 上的 Claude\n- macbook 上没装 Antigravity", 60 * 9),
      memFile("notes/machines.md", "# 电脑\n- devbox：Linux，claude / codex / agy 都有\n- macbook：claude、codex；周末常关机", 60 * 24 * 3),
      memFile("messages.md", "# What Collie recently told the human (written by the hub, oldest first)\n\n- 11-19 09:45 [chat:main] 好，分两路并行：fix-flaky-test 查分页测试，release-notes 写发布说明。\n- 11-19 09:53 [chat:main] shop-api/migrations 整理完了（41 → 33 个文件），那 3 个旧回滚脚本怎么处理？\n- 11-19 09:54 [chat:main] 发布说明写好了：6 条改动，要我直接开 PR 吗？", 5, 2867),
    ],
  };
  const settings = {
    bin: "collie", hook_key: "demo-key", router_dir: "~/.collie/router",
    policies: [{ source: "chat", action: "respond" }, { source: "github", action: "candidate" }, { source: "timer", action: "respond" }],
    settings: { router_kind: "claude", router_model: "sonnet", worker_model: "", worker_effort: "", worker_kind: "codex", worker_cwd: "~/code", worker_machine: "", notify_level: "normal", stall_minutes: 20, reply_chars: 400, rotate_pct: 70, dream_at: "04:00", ask_permissions: false },
  };
  const models = {
    claude: { models: [{ id: "opus", label: "Opus", efforts: ["low", "medium", "high", "max"] }, { id: "sonnet", label: "Sonnet", efforts: ["low", "medium", "high"] }, { id: "haiku", label: "Haiku" }], default: "sonnet", efforts: ["low", "medium", "high", "max"], default_effort: "" },
    codex: { models: [{ id: "gpt-6.1-sol", label: "GPT-6.1-Sol", efforts: ["low", "medium", "high", "xhigh"] }, { id: "gpt-6.1-mini", label: "GPT-6.1-Mini", efforts: null }], default: "gpt-6.1-sol", efforts: ["low", "medium", "high", "xhigh"], default_effort: "medium" },
    agy: {
      models: [
        { id: "gemini-4-pro-xhigh", label: "Gemini 4 Pro (XHigh)", efforts: [] },
        { id: "gemini-4-pro-high", label: "Gemini 4 Pro (High)", efforts: [] },
        { id: "gemini-4-pro-medium", label: "Gemini 4 Pro (Medium)", efforts: [] },
        { id: "gemini-4-pro-low", label: "Gemini 4 Pro (Low)", efforts: [] },
        { id: "gemini-pro-orchestrator", label: "Orchestrator", efforts: [] },
      ],
      default: "gemini-4-pro-high",
      default_effort: "high",
      efforts: ["low", "medium", "high", "xhigh"],
    },
    doubao: {
      models: [
        { id: "9", label: "Auto", efforts: ["low", "medium", "high"] },
        { id: "5", label: "Doubao 2.1 Pro", efforts: ["low", "medium", "high"] },
        { id: "5101", label: "DeepSeek V4.1 Flash", efforts: ["low", "high", "max"] },
        { id: "5262373378", label: "GPT 6.1 Sol", efforts: ["low", "medium", "high", "xhigh", "max"] },
      ],
      default: "9",
      default_effort: "high",
      efforts: ["low", "medium", "high"],
    },
    cursor: {
      models: [
        { id: "auto", label: "Auto", efforts: [] },
        { id: "composer-2.5", label: "Composer 2.5", efforts: [] },
        { id: "claude-4.6-opus-max", label: "Claude 4.6 Opus (Max)", efforts: [] },
        { id: "gpt-5.4-high", label: "GPT 5.4 High", efforts: [] },
      ],
      default: "auto",
      efforts: [],
    },
  };

  // ───────────────────────────── streams
  const streams = new Set();
  class FakeES {
    constructor(url) {
      this.url = String(url); this.readyState = 0; this.ls = {}; this.onopen = null; this.onerror = null; this.onmessage = null;
      streams.add(this);
      setTimeout(() => { if (this.readyState === 2) return; this.readyState = 1; this.onopen?.({}); this.prime(); }, 30);
    }
    addEventListener(t, f) { (this.ls[t] ||= []).push(f); }
    removeEventListener(t, f) { this.ls[t] = (this.ls[t] || []).filter((x) => x !== f); }
    close() { this.readyState = 2; streams.delete(this); }
    emit(t, data) { if (this.readyState !== 1) return; const e = { type: t, data: JSON.stringify(data) }; (this.ls[t] || []).forEach((f) => f(e)); }
    get path() { return new URL(this.url, location.href).pathname.replace(/^.*?\/api\//, "/api/"); }
    prime() {
      const p = this.path, m = /^\/api\/(panes|external)\/([^/]+)\/(chat|stream)$/.exec(p);
      if (p === "/api/events") { this.emit("state", state()); this.emit("super", sup()); return; }
      if (!m) return;
      const id = decodeURIComponent(m[2]);
      if (m[1] === "external") { this.emit("patch", { base: 0, from: 0, msgs: extChats[id] || [] }); return; }
      if (m[3] === "chat") { this.emit("patch", { base: 0, from: 0, msgs: chats[id] || [] }); this.emit("live", liveOf(id)); return; }
      this.emit("frame", { cols: 96, rows: 30, text: screenOf(id) });
    }
  }
  FakeES.CONNECTING = 0; FakeES.OPEN = 1; FakeES.CLOSED = 2;
  window.EventSource = FakeES;
  const liveOf = (id) => {
    const a = agents.find((x) => x.pane_id === id);
    if (a?.status === "blocked" && approvals[id]) return { status: "blocked", approval: approvals[id] };
    return { status: a?.status || "idle", ...(a?.status === "working" ? lives[id] || {} : {}) };
  };
  const pushAll = () => {
    for (const s of streams) {
      if (s.path === "/api/events") { s.emit("state", state()); s.emit("super", sup()); }
      const m = /^\/api\/panes\/([^/]+)\/chat$/.exec(s.path);
      if (m) { const id = decodeURIComponent(m[1]); s.emit("patch", { base: 0, from: 0, msgs: chats[id] || [] }); s.emit("live", liveOf(id)); }
    }
  };
  const setStatus = (id, st) => { const a = agents.find((x) => x.pane_id === id); if (a) a.status = st; const w = workers.find((x) => x.pane_id === id); if (w) w.status = st; };
  const wait = (ms) => new Promise((r) => setTimeout(r, ms));

  // ───────────────────────────── scripted replies
  let demoN = 0;
  const seenSayIds = new Map();
  function collieSay(text, re, clientMsgId, pick = false) {
    if (clientMsgId && seenSayIds.has(clientMsgId)) return seenSayIds.get(clientMsgId);
    const all = [...earlierEntries, ...entries];
    const q = re && all.find((e) => e.id === re && (e.role === "collie" || e.role === "user"));
    if (pick && q && q.role === "collie" && q.options?.length) {
      q.pick = { state: "picked", text, at: new Date().toISOString() };
      if (clientMsgId) seenSayIds.set(clientMsgId, q.id);
      routerStatus = "working";
      pushAll();
      colliePickAnswer(text, q);
      return q.id;
    }
    if (q && q.role === "collie" && q.options?.length && !q.pick) {
      q.pick = { state: "custom", text, at: new Date().toISOString() };
    }
    const mine = entry("user", text, q ? { re: { entry: q.id, who: q.role === "user" ? "你" : "Collie", text: q.text } } : {});
    if (clientMsgId) seenSayIds.set(clientMsgId, mine.id);
    entries.push(mine); routerStatus = "working"; pushAll(); collieAnswer(text, mine);
    return mine.id;
  }
  async function colliePickAnswer(choice, targetEntry) {
    await wait(900);
    entries.push(entry("collie", `收到，已按「${choice}」推进处理；有新进展我会第一时间同步给你。`, {
      re: { entry: targetEntry.id, who: "你选择了", text: `「${choice}」` },
    }));
    routerStatus = "idle";
    pushAll();
  }
  async function collieAnswer(text, fromEntry) {
    await wait(1400);
    const name = "task-" + ["deps", "tests", "docs", "perf"][demoN++ % 4];
    const reBack = fromEntry?.re ? { re: { entry: fromEntry.id, who: "你", text: fromEntry.text } } : {};
    entries.push(entry("collie", `收到。这件事我交给 **${name}**（Codex · devbox）去做，它会在 \`~/code/shop-api\` 里动手；有结果或需要你拍板时我再说。`, reBack));
    const pane = "w" + (20 + demoN) + ":p1";
    agents.push(agent({ pane_id: pane, workspace_id: pane.split(":")[0], name, kind: "codex", status: "working", cwd: "/home/alex/code/shop-api", run: ["gpt-6.1-sol", "medium"] }));
    workers.push({ name, kind: "codex", cwd: "/home/alex/code/shop-api", task: text, thread: "chat:main", pane_id: pane, workspace_id: pane.split(":")[0], machine: "", created_by: "collie", created_at: new Date().toISOString(), status: "working", run: ["gpt-6.1-sol", "medium"] });
    chats[pane] = [{ kind: "user", text: "[collie] " + text }, tool("Bash", "rg -n TODO --stats", "37 matches in 12 files")];
    lives[pane] = { status: "working", tail: "Reading the code", meta: "12s" };
    entries.push(entry("event", `派出 ${name}（codex · devbox）`, { tone: "spawn", ref: name }));
    routerStatus = "idle"; pushAll();
    await wait(5000);
    chats[pane].push({ kind: "assistant", text: "看完了，结论和改动已经写进报告。" });
    setStatus(pane, "done");
    workers.find((w) => w.name === name).last_report = "完成：发现 2 处问题，已修复并补了测试。";
    entries.push(entry("event", `${name} 回报：完成：发现 2 处问题，已修复并补了测试。`, { tone: "done", ref: name }));
    routerStatus = "working"; pushAll();
    await wait(1200);
    entries.push(entry("collie", `${name} 做完了：发现 2 处问题，都已修复并补了测试，改动在 \`shop-api\` 的工作区里。要我开 PR 吗？`, { options: ["开 PR", "先不用"] }));
    routerStatus = "idle"; pushAll();
  }
  async function promptPane(id, text) {
    (chats[id] ||= []).push({ kind: "user", text });
    setStatus(id, "working"); lives[id] = { status: "working", tail: "Thinking…", meta: "3s" }; pushAll();
    await wait(1800);
    chats[id].push(tool("Read", "README.md", "52 lines"));
    pushAll();
    await wait(1600);
    chats[id].push({ kind: "assistant", text: "好的，已经按你说的改好了。这是演示模式，真实环境里这里会是 agent 的回答。" });
    setStatus(id, "done"); pushAll();
  }
  async function answerApproval(id) {
    delete approvals[id];
    setStatus(id, "working"); lives[id] = { status: "working", tail: "Running go test ./internal/orders/...", meta: "4s" };
    chats[id].push(tool("Bash", "go test ./internal/orders/...", "ok  \tshop-api/internal/orders\t0.412s"));
    pushAll();
    await wait(2600);
    chats[id].push({ kind: "assistant", text: "测试全部通过。改动：`list.go` 的 offset 修正，`list_test.go` 加了跨页不重复的用例。" });
    setStatus(id, "done"); pushAll();
  }

  const changelog = "# v1.4\n\n## 需要注意\n\n- **配置项改名**：`cart.ttl` 改为 `cart.expire_after`，旧名字还能用，下个版本移除。\n- **Node 22 起步**：构建和运行都需要 Node 22 以上。\n\n## 改动\n\n- 订单列表支持按状态筛选\n- 结算页显示预计送达时间\n- 修复优惠券在购物车为空时报错\n- 商品图片改为懒加载，首屏快了约 30%\n- 搜索框支持拼音\n- 依赖升级：react 19.2、vite 7\n";
  const perfAuditMd = `# shop-api v1.4 核心接口压测与分页稳定性复盘

> 测试环境：devbox（8 vCPU / 32 GB · PostgreSQL 16.4），压测基线对比 \`v1.3.8\` 与 \`v1.4.0-rc2\`（1020 并发持续 15 分钟）。

## 1. 全接口延迟与吞吐对比（宽表）

| 接口路由 | 压测场景 | v1.3 P50 | v1.3 P99 | v1.4 P50 | v1.4 P99 | 吞吐提升 | 关键变更与代码位置 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | --- |
| \`GET /api/orders\` | 同秒多单翻页 + 状态过滤 | 18.4 ms | 146.2 ms | 7.9 ms | 28.5 ms | +118% | 复合索引 \`(user_id, created_at DESC, id DESC)\` · \`~/code/shop-api/internal/orders/list.go\` |
| \`POST /api/coupons/check\` | 空购物车 + 历史券码校验 | 11.2 ms | 84.0 ms | 6.1 ms | 19.4 ms | +64% | 补齐 \`SCHEMA_AND_ID\` 默认值兜底，消除回表重试 |
| \`GET /api/catalog/home\` | 首页首屏商品流（含缩略图） | 24.6 ms | 162.8 ms | 12.0 ms | 49.1 ms | +92% | 折叠区懒加载 + Redis 热键预热 · \`~/code/shop-web/CHANGELOG.md\` |
| \`POST /api/checkout/quote\` | 运费与预计送达时间试算 | 15.8 ms | 112.5 ms | 9.3 ms | 34.7 ms | +57% | 配置切换至 \`cart.expire_after\`，合并区域费率批量查询 |
| \`GET /api/orders/export\` | 月度账单流式导出（>5 万行） | 42.0 ms | 310.0 ms | 29.5 ms | 118.0 ms | +41% | 游标分页流式写出，冷备落盘 \`hdfs://cluster-east/warehouse/shop_orders/dt=2026-11-20\` |

## 2. 复合索引与查询计划

将原来的单列时间索引替换为覆盖排序键的复合索引后，消除了同秒记录的 \`Incremental Sort\` 抖动：

\`\`\`sql
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_orders_user_created_id
\tON orders (user_id, created_at DESC, id DESC)
\tINCLUDE (status, total_cents);
\`\`\`

整体架构与读写分离链路示意：

![读写链路架构图](~/code/shop-web/docs/architecture.svg)

## 3. 回归验证清单

1. 连续 200 轮并发翻页回归：\`go test -run TestOrdersPagination -count=200 ./internal/orders\` 零失败。
2. 线上监控看板：\`https://metrics.example.com/shop-api/run-1020\`
3. 压测交互式分位曲线：\`~/code/shop-api/bench-pagination.html\`
`;
  const ordersListGo = `package orders

import (
\t"context"
\t"database/sql"
\t"fmt"
\t"time"
)

// Order is a single row in the customer order history page.
type Order struct {
\tID         int64     \`json:"id"\`
\tUserID     int64     \`json:"user_id"\`
\tStatus     string    \`json:"status"\`
\tTotalCents int64     \`json:"total_cents"\`
\tCreatedAt  time.Time \`json:"created_at"\`
}

const listSQL = \`
\tSELECT id, user_id, status, total_cents, created_at
\tFROM orders
\tWHERE user_id = $1
\tORDER BY created_at DESC, id DESC
\tLIMIT $2 OFFSET $3\`

// List returns one 1-indexed page of orders with deterministic tie-breaking.
func List(ctx context.Context, db *sql.DB, userID int64, page, size int) ([]Order, error) {
\tif page < 1 {
\t\tpage = 1
\t}
\tif size <= 0 || size > 100 {
\t\tsize = 20
\t}
\toffset := (page - 1) * size
\trows, err := db.QueryContext(ctx, listSQL, userID, size, offset)
\tif err != nil {
\t\treturn nil, fmt.Errorf("query orders page %d: %w", page, err)
\t}
\tdefer rows.Close()

\tvar out []Order
\tfor rows.Next() {
\t\tvar o Order
\t\tif err := rows.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &o.CreatedAt); err != nil {
\t\t\treturn nil, err
\t\t}
\t\tout = append(out, o)
\t}
\treturn out, rows.Err()
}
`;

  const ordersBenchmarkCsv = [
    "route,scenario,v1_3_p50_ms,v1_3_p99_ms,v1_4_p50_ms,v1_4_p99_ms,throughput_gain,notes",
    "GET /api/orders,\"同秒多单翻页, 含状态过滤\",18.4,146.2,7.9,28.5,+118%,\"复合索引 (user_id, created_at DESC, id DESC)\"",
    "POST /api/coupons/check,\"空购物车 + \"\"历史券码\"\"校验\",11.2,84.0,6.1,19.4,+64%,补齐 SCHEMA_AND_ID 默认值兜底",
    "GET /api/catalog/home,首页首屏商品流,24.6,162.8,12.0,49.1,+92%,折叠区懒加载 + Redis 热键预热",
    "POST /api/checkout/quote,运费与预计送达试算,15.8,112.5,9.3,34.7,+57%,合并区域费率批量查询",
    "GET /api/orders/export,\"月度账单流式导出\n(>5 万行)\",42.0,310.0,29.5,118.0,+41%,游标分页流式写出",
    "GET /api/orders/:id,单笔订单详情,5.2,21.4,4.8,16.2,+18%,主键点查保持平稳",
  ].join("\n") + "\n";

  const demoNotebook = () => {
    const today = new Date().toISOString().slice(0, 10);
    return {
      date: today,
      tasks: [
        { id: "v1.4-release", title: "v1.4 发布与 /orders 分页修复", note: "fix-flaky-test 已定位，待开 PR", tone: "live", worker: "fix-flaky-test" },
        { id: "migrations-cleanup", title: "旧回滚脚本处理待拍板", note: "等待你确认归档或删除", tone: "attn", entry_id: entries[5]?.id || "" },
        { id: "mobile-lcp", title: "手机端首屏 LCP 与资源瀑布图", note: "排在分页索引上线后启动", tone: "idle" },
        { id: "dark-mode", title: "首页暗色模式适配", note: "claude-1 在 macbook 编写 Header 切换", tone: "live", worker: "claude-1" },
      ],
      artifacts: [
        { path: "~/code/shop-api/bench-pagination.html", name: "bench-pagination.html", title: "200 轮分页压测对比", ext: "html", at: ago(2.5) },
        { path: "~/code/shop-api/docs/orders-benchmark.csv", name: "orders-benchmark.csv", title: "orders-benchmark.csv", ext: "csv", at: ago(2.5) },
        { path: "~/code/shop-api/docs/perf-audit-v1.4.md", name: "perf-audit-v1.4.md", title: "v1.4 压测与索引复盘", ext: "md", at: ago(2.5) },
      ],
      timers: timers.filter((t) => t.enabled).slice().sort((a, b) => new Date(a.next_run) - new Date(b.next_run)).map((t) => ({
        id: t.id, spec: t.spec, text: t.text, next_run: t.next_run,
      })),
    };
  };

  // ───────────────────────────── the API
  const json = (data, status = 200) => new Response(JSON.stringify(data), { status, headers: { "Content-Type": "application/json" } });
  const demoUploads = new Map();
  let demoUploadSeq = 0;
  const realFetch = window.fetch.bind(window);
  window.fetch = async (input, init = {}) => {
    const u = new URL(typeof input === "string" ? input : input.url, location.href);
    const i = u.pathname.indexOf("/api/");
    if (i < 0) return realFetch(input, init);
    const p = u.pathname.slice(i), q = u.searchParams, method = (init.method || "GET").toUpperCase();
    let body = {};
    try { body = init.body ? JSON.parse(init.body) : {}; } catch {}
    await wait(60);
    let m;
    if (p === "/api/session") return json({ ok: true });
    if (p === "/api/state") return json(state());
    if (p === "/api/super") return json(sup());
    if (p === "/api/super/notebook") return json(demoNotebook());
    if (p === "/api/super/settings") {
      if (method === "POST") {
        if (body.settings) settings.settings = { ...settings.settings, ...body.settings };
        if (body.policies) settings.policies = body.policies;
        pushAll();
      }
      return json(settings);
    }
    if (p === "/api/super/router/restart" && method === "POST") {
      routerRun = routerSig(settings.settings);
      routerStatus = "idle";
      pushAll();
      return json({ ok: true });
    }
    if (p === "/api/super/seen") return json({ ok: true });
    if (p === "/api/chat/read" && method === "POST") {
      const idN = (id) => parseInt(String(id || "").replace(/^[a-z]+/i, ""), 10) || 0;
      const cand = String(body.entry_id || body.last_read_entry_id || "").trim();
      if (!/^e[0-9]+$/.test(cand) || idN(cand) <= 0) return json({ error: "invalid entry_id" }, 400);
      const changed = idN(cand) > idN(lastReadEntryId);
      if (changed) lastReadEntryId = cand;
      return json({ ok: true, last_read_entry_id: lastReadEntryId, changed });
    }
    if (p === "/api/chat/clap" && method === "POST") {
      const cand = String(body.entry_id || body.id || "").trim();
      if (!/^e[0-9]+$/.test(cand)) return json({ error: "invalid entry_id" }, 400);
      const hit = entries.find((e) => e.id === cand) || earlierEntries.find((e) => e.id === cand);
      if (!hit) return json({ error: "找不到这条消息" }, 404);
      if (hit.role !== "collie") return json({ error: "只能给 Collie 的回复点赞" }, 400);
      const cur = Math.max(0, Math.min(50, Math.floor(Number(hit.claps) || 0)));
      let next = cur;
      if (body.reset) next = 0;
      else if (typeof body.claps === "number") {
        if (body.claps < 0) return json({ error: "claps must be >= 0" }, 400);
        next = Math.max(0, Math.min(50, Math.floor(body.claps)));
      } else {
        const delta = typeof body.delta === "number" ? Math.trunc(body.delta) : 1;
        next = Math.max(0, Math.min(50, cur + delta));
      }
      const changed = next !== cur;
      if (changed) {
        if (next > 0) hit.claps = next;
        else delete hit.claps;
        pushAll();
      }
      return json({ ok: true, entry_id: cand, claps: next, max: 50, changed });
    }
    if (p === "/api/super/dismiss") { if (body.id) dismissed.add(body.id); pushAll(); return json({ ok: true }); }
    if (p === "/api/super/pin") {
      const idx = pinnedIds.indexOf(body.id);
      if (body.pinned && body.id && idx < 0) pinnedIds.push(body.id);
      else if (!body.pinned && idx >= 0) pinnedIds.splice(idx, 1);
      pushAll();
      return json({ ok: true, pinned: !!body.pinned });
    }
    if (p === "/api/super/earlier") { // earlier history pages + direct `until` jump
      const idN = (id) => parseInt(String(id || "").replace(/^[a-z]+/i, ""), 10) || 0;
      const beforeN = idN(q.get("before")), untilN = idN(q.get("until"));
      const pool = earlierEntries.filter((e) => idN(e.id) < beforeN);
      if (!pool.length) return json({ more: false, entries: [] });
      const pageSize = 4;
      let slice;
      if (untilN > 0 && untilN < beforeN) {
        const idx = pool.findIndex((e) => idN(e.id) >= untilN);
        const start = idx < 0 ? 0 : Math.max(0, idx - 2);
        slice = pool.slice(start);
        return json({ more: start > 0, entries: slice });
      }
      const start = Math.max(0, pool.length - pageSize);
      slice = pool.slice(start);
      return json({ more: start > 0, entries: slice });
    }
    if (p === "/api/super/search") {
      const rawQ = (q.get("q") || "").trim();
      const lim = Math.min(50, Math.max(1, parseInt(q.get("limit") || "30", 10) || 30));
      if (!rawQ) return json({ query: "", results: [], total: 0, has_more: false });
      const terms = [...new Set(rawQ.toLowerCase().split(/\s+/).filter(Boolean))];
      const all = [...earlierEntries, ...entries].filter((e) => e.role === "user" || e.role === "collie");
      const cleanOne = (e, withLinks) => {
        let t = String(e.text || "").replace(/```[^\n`]*\n?([\s\S]*?)(?:```|$)/g, " $1 ");
        if (!withLinks) t = t.replace(/\[image:[^\]]+\]/g, " ").replace(/\[([^\]\n]+)\]\([^)\n]+\)/g, "$1");
        else t = t.replace(/\[([^\]\n]+)\]\(([^)\n]+)\)/g, "$1 ($2)") + (e.options?.length ? " " + e.options.join(" / ") : "");
        return t.split("\n").map((l) => l.replace(/^\s*(?:[#>*•]+|-\s)\s*/, "").replace(/\*\*|__|`/g, "").trim()).filter(Boolean).join(" ").replace(/\s+/g, " ").trim();
      };
      const snipOf = (text) => {
        const low = text.toLowerCase();
        for (const tm of terms) if (!low.includes(tm)) return null;
        const i0 = low.indexOf(terms[0]);
        if (text.length <= 140) return text;
        let s = Math.max(0, i0 - 36), end = Math.min(text.length, s + 140);
        s = Math.max(0, end - 140);
        return (s > 0 ? "…" : "") + text.slice(s, end).trim() + (end < text.length ? "…" : "");
      };
      const hits = [];
      for (const e of all) {
        const sn = snipOf(cleanOne(e, false)) ?? snipOf(cleanOne(e, true));
        if (sn !== null) hits.push({ id: e.id, role: e.role, at: e.at, snippet: sn });
      }
      hits.sort((a, b) => new Date(b.at) - new Date(a.at));
      return json({ query: rawQ, results: hits.slice(0, lim), total: hits.length, has_more: hits.length > lim });
    }
    if (p === "/api/super/lark") return json({ ready: true, who: "Alex" });
    if (p === "/api/super/integrations") {
      return json({
        integrations: [demoFeishu, demoGitHub, demoGitCLI],
        summary: sup().integrations_summary,
        hook_key: settings.hook_key,
        notify: {
          notify_webhook: settings.settings.notify_webhook || "",
          notify_lark: !!settings.settings.notify_lark,
          notify_level: settings.settings.notify_level || "quiet",
        },
      });
    }
    if (p === "/api/super/integrations/feishu" && method === "POST") {
      if (body.clear_secret) {
        demoFeishu.secret_set = false;
      } else if (body.secrets?.app_secret) {
        demoFeishu.secret_set = true;
      }
      if (body.config) {
        const oid = String(body.config.allow_open_id || "").trim();
        const rawFb = Array.isArray(body.config.feedback_chat_ids)
          ? body.config.feedback_chat_ids.join(", ")
          : String(body.config.feedback_chat_ids ?? body.config.feedback_chat_id ?? "").trim();
        const fbList = rawFb ? rawFb.split(/[\s,，;；]+/).map((s) => s.trim()).filter(Boolean) : [];
        demoFeishu.config = {
          ...demoFeishu.config,
          ...body.config,
          allow_open_id: oid,
          allow_open_ids: oid ? oid.split(/[,\s]+/).filter(Boolean) : [],
          feedback_chat_ids: fbList,
          feedback_chat_id: fbList.join(", "),
        };
      }
      demoFeishu.configured = !!(demoFeishu.config.app_id && demoFeishu.secret_set);
      if (typeof body.enabled === "boolean") demoFeishu.enabled = body.enabled && demoFeishu.configured;
      demoFeishu.health = !demoFeishu.configured
        ? { state: "unconfigured", today_count: 0 }
        : !demoFeishu.enabled
          ? { state: "disabled", today_count: demoFeishu.health.today_count || 0 }
          : { state: "connected", detail: "长连接在线", last_ok_at: new Date().toISOString(), last_active_at: new Date().toISOString(), today_count: demoFeishu.health.today_count || 0 };
      pushAll();
      return json({ integration: demoFeishu, summary: sup().integrations_summary });
    }
    if (p === "/api/super/integrations/feishu/test" && method === "POST") {
      return json({ ok: true, latency_ms: 42, message: "已连通（42ms），验证卡片已发至飞书（演示模式）" });
    }
    if (p === "/api/super/integrations/feishu/probe") {
      return json({ ready: true, open_id: "ou_demo9a2f7c10", who: "Alex" });
    }
    if (p === "/api/super/integrations/feishu/pair" && method === "POST") {
      demoFeishu.extra.pairing = { active: !body.cancel, remaining_sec: 120 };
      return json({ ok: true, active: !body.cancel, message: "请在 2 分钟内用飞书给机器人发任意一条私信（演示模式）" });
    }
    if (p === "/api/super/integrations/feishu/onboard") {
      if (method === "DELETE") {
        demoFeishuOnboard = null;
        return json({ ok: true, state: "idle" });
      }
      if (method === "POST") {
        const region = body.region === "intl" ? "intl" : "cn";
        const mode = body.mode === "existing" ? "existing" : "create";
        const clientId = mode === "existing" ? String(body.client_id || "").trim() : "";
        const host = region === "intl" ? "open.larksuite.com" : "open.feishu.cn";
        const presets = demoFeishu.extra.onboard_presets;
        const avatarId = presets.avatars.some((a) => a.id === body.avatar) ? body.avatar : presets.default_avatar;
        const botName = mode === "create" ? (String(body.name || "").trim() || presets.default_name) : "";
        const botDesc = mode === "create" ? (String(body.desc || "").trim() || presets.default_desc) : "";
        const qp = new URLSearchParams({ user_code: "DEMO-2026", from: "sdk", tp: "sdk", source: "go-sdk/collie" });
        if (mode === "create") {
          qp.set("createOnly", "true");
          qp.set("name", botName);
          qp.set("desc", botDesc);
          const ordered = [presets.avatars.find((a) => a.id === avatarId), ...presets.avatars.filter((a) => a.id !== avatarId)].filter(Boolean);
          for (const av of ordered) qp.append("avatar", av.url);
        } else if (clientId) {
          qp.set("clientID", clientId);
        }
        demoFeishuOnboard = {
          session_id: "demo-ob-" + (++seq),
          state: "waiting",
          mode,
          region,
          client_id: clientId,
          ...(mode === "create" ? { name: botName, desc: botDesc, avatar: avatarId } : {}),
          url: `https://${host}/page/launcher?${qp.toString()}`,
          user_code: "DEMO-2026",
          qr_data_url: "demo-qr.png",
          expire_in: 600,
          expiresAtMs: Date.now() + 600000,
        };
        return json({ ...demoFeishuOnboard, remaining_sec: 600 });
      }
      if (!demoFeishuOnboard) return json({ state: "idle" });
      const rem = Math.max(0, Math.ceil((demoFeishuOnboard.expiresAtMs - Date.now()) / 1000));
      return json({ ...demoFeishuOnboard, remaining_sec: rem });
    }
    const demoForgeMap = { github: demoGitHub, "git-cli": demoGitCLI };
    const forgeMatch = p.match(/^\/api\/super\/integrations\/(github|git-cli)(?:\/(test|probe|sample))?$/);
    if (forgeMatch) {
      const target = demoForgeMap[forgeMatch[1]];
      const sub = forgeMatch[2] || "";
      const isGH = forgeMatch[1] === "github";
      const demoSamples = isGH ? [
        { kind: "pr", repo: "example-dev/collie #214", tag: "PR 评审", title: "@octo 请求复核「补充 SQLite 持久去重索引与单测」", at: ago(6), action: "按当前模式：Collie 自动调研 PR 改动与评审意见并给拍板选项，严禁擅自评论或合并" },
        { kind: "ci", repo: "example-dev/collie", tag: "CI 失败", title: "Go Test (main) · failure", at: ago(22), action: "按当前模式：Collie 自动拉取失败日志定位原因，给修复拍板选项（不擅自修改远端）" },
        { kind: "issue", repo: "example-dev/shop-api #58", tag: "Issue 新评论", title: "分页最后一页有重复数据，建议统一按游标翻页", at: ago(54), action: "按当前模式：Collie 结合上下文调研并拟好建议选项，严禁擅自评论" },
      ] : [
        { kind: "mr", repo: "team-core/service-api !821", tag: "指派给我评审", title: "feat(dedup): 新增幂等去重表定时清理与并发单测", at: ago(8), action: "按当前模式：Collie 自动调研 MR 改动与评审意见并给拍板选项，严禁擅自评论或合并" },
        { kind: "mention", repo: "team-core/service-api !821", tag: "评论 @我", title: "@alex 去重保留周期设为 30 天会不会导致 WAL 膨胀？", at: ago(19), action: "按当前模式：Collie 结合上下文调研并拟好建议选项，严禁擅自评论" },
        { kind: "ci", repo: "team-core/service-api", tag: "流水线失败", title: "unit_test · step #4 静态检查未通过", at: ago(41), action: "按当前模式：Collie 自动拉取失败日志定位原因，给修复拍板选项（不擅自修改远端）" },
      ];
      if (sub === "probe") {
        return json({ ready: true, user: "alex", repos: isGH ? ["example-dev/collie", "example-dev/shop-api", "example-dev/herdr"] : ["team-core/service-api", "team-core/web-console"] });
      }
      if (sub === "sample") {
        return json({ ok: true, samples: demoSamples });
      }
      if (sub === "test") {
        return json({
          ok: true,
          latency_ms: isGH ? 128 : 164,
          message: isGH ? "已连通 @alex（128ms），试拉到最近 3 条动态（剩余额度 4982/5000）" : "已连通 @alex（164ms），试拉到最近 3 条动态",
          detail: { user: "alex", samples: demoSamples },
        });
      }
      if (method === "POST") {
        if (body.clear_secret) target.secret_set = false;
        else if (body.secrets?.pat) target.secret_set = true;
        if (body.config) target.config = { ...target.config, ...body.config };
        target.configured = body.config?.configured !== false;
        if (typeof body.enabled === "boolean") target.enabled = body.enabled && target.configured;
        target.health = !target.configured
          ? { state: "unconfigured", today_count: 0 }
          : !target.enabled
            ? { state: "disabled", today_count: target.health.today_count || 0 }
            : { state: "connected", detail: "已连通 · @alex", last_ok_at: new Date().toISOString(), last_active_at: new Date().toISOString(), today_count: target.health.today_count || 0 };
        pushAll();
        return json({ integration: target, summary: sup().integrations_summary });
      }
    }
    if (p === "/api/super/notify/test" && method === "POST") {
      return json({ ok: true, message: "测试提醒已发送（演示模式 HTTP 200）" });
    }
    if (p === "/api/super/peers" && method === "GET") {
      return json({
        hub_id: demoPeerHubID,
        hub_name: demoPeerHubName,
        enabled: demoPeersEnabled,
        addrs: ["http://192.168.1.20:7788"],
        peers: demoPeers,
        blocklist: demoPeerBlocklist,
        blocked_count: demoPeerBlocklist.length,
        summary: sup().peers_summary,
      });
    }
    if (p === "/api/super/peers/settings" && method === "POST") {
      if (typeof body.enabled === "boolean") demoPeersEnabled = body.enabled;
      if (typeof body.hub_name === "string" && body.hub_name.trim()) demoPeerHubName = body.hub_name.trim();
      pushAll();
      return json({ ok: true, hub_name: demoPeerHubName, enabled: demoPeersEnabled, summary: sup().peers_summary });
    }
    if (p === "/api/super/peers/invite" && method === "POST") {
      const mode = body.mode || "advise";
      if (body.reset) demoPeerCardCode = "DEMO" + Math.random().toString(36).slice(2, 10) + Math.random().toString(36).slice(2, 12);
      const compact = { h: demoPeerHubID, n: demoPeerHubName, a: ["192.168.1.20:7788"], t: demoPeerCardCode };
      const bytes = new TextEncoder().encode(JSON.stringify(compact));
      let bin = "";
      bytes.forEach((b) => { bin += String.fromCharCode(b); });
      const b64 = btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
      const inv = "collie-peer://v2/" + b64;
      const joinUrl = "https://lihuanyu0w0.github.io/collie/join/#2." + b64;
      return json({ invite_code: inv, join_url: joinUrl, code: compact.t, hub_id: demoPeerHubID, hub_name: demoPeerHubName, mode, addrs: ["http://192.168.1.20:7788"], permanent: true });
    }
    if (p === "/api/super/peers/pair" && method === "POST") {
      const id = "peer-" + (demoPeers.length + 1);
      const np = { id, hub_id: "hub_demo_" + (demoPeers.length + 1), name: body.name || "新好友的边牧", addrs: ["http://192.168.1.88:7788"], primary_addr: "http://192.168.1.88:7788", mode: body.mode || "advise", status: "pending_outgoing", protocol_ver: 1, today_count: 0, created_at: new Date().toISOString(), updated_at: new Date().toISOString() };
      demoPeers.push(np);
      pushAll();
      return json({ ok: true, pending: true, peer: np });
    }
    const peerApproveM = /^\/api\/super\/peers\/([^/]+)\/approve$/.exec(p);
    if (peerApproveM && method === "POST") {
      const pid = decodeURIComponent(peerApproveM[1]);
      const target = demoPeers.find((x) => x.id === pid);
      if (!target) return json({ error: "not found" }, 404);
      if (body.mode) target.mode = body.mode;
      if (body.name) target.name = body.name;
      target.status = "active";
      target.last_ok_at = new Date().toISOString();
      pushAll();
      return json({ ok: true, peer: target });
    }
    const peerRejectM = /^\/api\/super\/peers\/([^/]+)\/reject$/.exec(p);
    if (peerRejectM && method === "POST") {
      const pid = decodeURIComponent(peerRejectM[1]);
      const idx = demoPeers.findIndex((x) => x.id === pid);
      if (idx < 0) return json({ error: "not found" }, 404);
      const [rm] = demoPeers.splice(idx, 1);
      const block = !!body.block;
      if (block && rm) demoPeerBlocklist.push({ hub_id: rm.hub_id, name: rm.name, blocked_at: new Date().toISOString() });
      pushAll();
      return json({ ok: true, blocked: block });
    }
    const peerPingM = /^\/api\/super\/peers\/([^/]+)\/ping$/.exec(p);
    if (peerPingM && method === "POST") {
      return json({ ok: true, peer_id: decodeURIComponent(peerPingM[1]), addr: "http://192.168.1.42:7788", latency_ms: 12, remote_name: "Sam的边牧", remote_version: "v0.2.95", protocol_ver: 1 });
    }
    const peerBlockDelM = /^\/api\/super\/peers\/blocklist\/([^/]+)$/.exec(p);
    if (peerBlockDelM && method === "DELETE") {
      const hid = decodeURIComponent(peerBlockDelM[1]);
      const idx = demoPeerBlocklist.findIndex((b) => b.hub_id === hid);
      if (idx >= 0) demoPeerBlocklist.splice(idx, 1);
      return json({ ok: true });
    }
    const peerOneM = /^\/api\/super\/peers\/([^/]+)$/.exec(p);
    if (peerOneM) {
      const pid = decodeURIComponent(peerOneM[1]);
      const idx = demoPeers.findIndex((x) => x.id === pid);
      if (idx < 0) return json({ error: "not found" }, 404);
      if (method === "PATCH") {
        if (body.mode) demoPeers[idx].mode = body.mode;
        if (body.status) demoPeers[idx].status = body.status;
        if (body.name) demoPeers[idx].name = body.name;
        pushAll();
        return json({ ok: true, peer: demoPeers[idx], blocked: false });
      }
      if (method === "DELETE") {
        const [rm] = demoPeers.splice(idx, 1);
        const block = q.get("block") === "1";
        if (block && rm) demoPeerBlocklist.push({ hub_id: rm.hub_id, name: rm.name, blocked_at: new Date().toISOString() });
        pushAll();
        return json({ ok: true, blocked: block });
      }
    }
    if (p === "/api/super/triggers") return json({ triggers, threads });
    if (p === "/api/super/subscribe" && method === "POST") {
      const th = threads.find((x) => x.id === body.thread);
      if (th && body.worker) {
        if (body.off) th.subscribers = (th.subscribers || []).filter((w) => w !== body.worker);
        else if (!(th.subscribers || []).includes(body.worker)) (th.subscribers ||= []).push(body.worker);
      }
      return json({ thread: body.thread, subscribers: th?.subscribers || [] });
    }
    if (p === "/api/super/timers") {
      if (method === "POST") timers.push({ id: "tm" + (++seq), spec: body.spec, text: body.text, target: body.target, enabled: true, next_run: later(60), owner: "you" });
      const active = timers.filter((t) => t.enabled);
      const ended = timers.filter((t) => !t.enabled);
      if (q.get("ended_only") === "1") {
        const off = Math.max(0, Number(q.get("ended_offset")) || 0);
        const lim = Math.max(1, Math.min(200, Number(q.get("ended_limit")) || 40));
        return json({ ended: ended.slice(off, off + lim), active_count: active.length, ended_count: ended.length, ended_offset: off, ended_limit: lim, ended_has_more: off + lim < ended.length });
      }
      if (q.has("ended_limit")) {
        const lim = Math.max(0, Math.min(200, Number(q.get("ended_limit")) || 0));
        return json({ timers: [...active, ...ended.slice(0, lim)], active_count: active.length, ended_count: ended.length, ended_has_more: lim < ended.length });
      }
      return json({ timers });
    }
    const delTimerM = /^\/api\/super\/timers\/([^/]+)\/delete$/.exec(p);
    if (delTimerM && method === "POST") {
      const idx = timers.findIndex((t) => t.id === delTimerM[1]);
      if (idx >= 0) timers.splice(idx, 1);
      return json({ ok: true });
    }
    if (p === "/api/super/memory/file") {
      const reqPath = q.get("path") || "";
      if (reqPath.startsWith("dreams/")) {
        const name = reqPath.slice("dreams/".length);
        const d = (memory.dreams || []).find((x) => x.name === name);
        return d ? json({ file: d }) : json({ error: "not found" }, 404);
      }
      const f = (memory.files || []).find((x) => x.name === reqPath);
      return f ? json({ file: f }) : json({ error: "not found" }, 404);
    }
    if (p === "/api/super/memory/done") {
      const done = (memory.files || []).filter((x) => x.name.startsWith("tasks/done/")).sort((a, b) => new Date(b.mod) - new Date(a.mod));
      const off = Math.max(0, Number(q.get("offset")) || 0);
      const lim = Math.max(1, Math.min(200, Number(q.get("limit")) || 60));
      const firstLine = (t) => String(t || "").split("\n").filter((l) => !/^\s*#/.test(l)).map((l) => l.replace(/^[>*\-\s]+/, "").replace(/`|\*\*/g, "").trim()).find(Boolean) || "";
      const slice = done.slice(off, off + lim).map((f) => ({ name: f.name, text: "", preview: firstLine(f.text), summary_only: true, size: f.size, mod: f.mod }));
      return json({ files: slice, total: done.length, offset: off, limit: lim, has_more: off + lim < done.length });
    }
    if (p === "/api/super/memory/search") {
      const terms = (q.get("q") || "").trim().toLowerCase().split(/\s+/).filter(Boolean);
      const lim = Math.max(1, Math.min(200, Number(q.get("limit")) || 60));
      const hitOf = (txt) => {
        const low = String(txt || "").toLowerCase();
        if (!terms.length || !terms.every((t) => low.includes(t))) return 0;
        return terms.reduce((s, t) => s + Math.max(1, low.split(t).length - 1), 0);
      };
      let totalHits = 0;
      const mFiles = (memory.files || []).filter((f) => { const h = hitOf(f.name + "\n" + f.text); if (h) totalHits += h; return h > 0; });
      const mDreams = (memory.dreams || []).filter((d) => { const h = hitOf("dreams/" + d.name + "\n" + d.text); if (h) totalHits += h; return h > 0; });
      return json({ query: q.get("q") || "", files: mFiles.slice(0, lim), dreams: mDreams.slice(0, lim), total_files: mFiles.length + mDreams.length, total_hits: totalHits, truncated: mFiles.length + mDreams.length > lim });
    }
    if (p === "/api/super/memory") {
      if (method === "POST" && body.path) {
        const f = memory.files.find((x) => x.name === body.path);
        if (f) { f.text = body.text; f.size = new Blob([body.text]).size; f.mod = new Date().toISOString(); }
        memory.up_front = memory.files.filter((x) => x.name === "instructions.md" || x.name === "notes/index.md" || /^tasks\/[^/]+\.md$/.test(x.name)).reduce((s, x) => s + x.size, 0);
        memory.messages = memory.files.filter((x) => x.name === "messages.md").reduce((s, x) => s + x.size, 0);
        memory.loaded = memory.up_front + memory.messages;
      }
      if (q.get("summary") === "1") {
        const includeDone = q.get("include_done") !== "0";
        const firstLine = (t) => String(t || "").split("\n").filter((l) => !/^\s*#/.test(l)).map((l) => l.replace(/^[>*\-\s]+/, "").replace(/`|\*\*/g, "").trim()).find(Boolean) || "";
        const isUpFront = (n) => n === "instructions.md" || n === "notes/index.md" || n === "messages.md" || /^tasks\/[^/]+\.md$/.test(n);
        const doneCount = (memory.files || []).filter((f) => f.name.startsWith("tasks/done/")).length;
        const files = (memory.files || []).filter((f) => includeDone || !f.name.startsWith("tasks/done/")).map((f) => (
          isUpFront(f.name) || f.legacy ? f : { name: f.name, text: "", preview: firstLine(f.text), summary_only: true, size: f.size, mod: f.mod }
        ));
        const dreams = (memory.dreams || []).map((d, i) => (
          i === 0 ? d : { name: d.name, text: "", preview: firstLine(d.text), summary_only: true, size: d.size, mod: d.mod }
        ));
        return json({ ...memory, files, dreams, done_count: doneCount, summary_mode: true });
      }
      return json(memory);
    }
    if (p === "/api/memory/export" && method === "GET") {
      const enc = new TextEncoder();
      const crcTable = new Uint32Array(256);
      for (let n = 0; n < 256; n++) { let c = n; for (let k = 0; k < 8; k++) c = (c & 1) ? (0xedb88320 ^ (c >>> 1)) : (c >>> 1); crcTable[n] = c >>> 0; }
      const crc32 = (u8) => { let c = 0xffffffff; for (let i = 0; i < u8.length; i++) c = crcTable[(c ^ u8[i]) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
      const parts = [], central = [];
      let offset = 0;
      const mdFiles = memory.files.filter((f) => !f.legacy && /\.md$/.test(f.name));
      for (const f of mdFiles) {
        const nameB = enc.encode(f.name), dataB = enc.encode(f.text || ""), crc = crc32(dataB);
        const lh = new ArrayBuffer(30), lv = new DataView(lh);
        lv.setUint32(0, 0x04034b50, true); lv.setUint16(4, 20, true); lv.setUint16(6, 0x0800, true);
        lv.setUint32(14, crc, true); lv.setUint32(18, dataB.length, true); lv.setUint32(22, dataB.length, true); lv.setUint16(26, nameB.length, true);
        const ch = new ArrayBuffer(46), cv = new DataView(ch);
        cv.setUint32(0, 0x02014b50, true); cv.setUint16(4, 20, true); cv.setUint16(6, 20, true); cv.setUint16(8, 0x0800, true);
        cv.setUint32(16, crc, true); cv.setUint32(20, dataB.length, true); cv.setUint32(24, dataB.length, true); cv.setUint16(28, nameB.length, true); cv.setUint32(42, offset, true);
        parts.push(new Uint8Array(lh), nameB, dataB);
        central.push(new Uint8Array(ch), nameB);
        offset += 30 + nameB.length + dataB.length;
      }
      const cdSize = central.reduce((s, b) => s + b.length, 0);
      const eocd = new ArrayBuffer(22), ev = new DataView(eocd);
      ev.setUint32(0, 0x06054b50, true); ev.setUint16(8, mdFiles.length, true); ev.setUint16(10, mdFiles.length, true);
      ev.setUint32(12, cdSize, true); ev.setUint32(16, offset, true);
      const blob = new Blob([...parts, ...central, new Uint8Array(eocd)], { type: "application/zip" });
      const d = new Date(), pad = (n) => String(n).padStart(2, "0");
      const stamp = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
      return new Response(blob, {
        status: 200,
        headers: {
          "Content-Type": "application/zip",
          "Content-Disposition": `attachment; filename="collie-memory-${stamp}.zip"`,
          "X-Memory-Files": String(mdFiles.length),
        },
      });
    }
    if (p === "/api/memory/import" && method === "POST") {
      const mode = (q.get("mode") || (init.body instanceof FormData ? init.body.get("mode") : "") || "merge").toString();
      const file = init.body instanceof FormData ? init.body.get("file") : null;
      if (!file || typeof file.arrayBuffer !== "function") return json({ error: "请选择要导入的 .zip 文件" }, 400);
      const buf = new Uint8Array(await file.arrayBuffer());
      if (buf.length < 4 || buf[0] !== 0x50 || buf[1] !== 0x4b) return json({ error: "无效的 .zip 压缩包" }, 400);
      const d = new Date(), pad = (n) => String(n).padStart(2, "0");
      const stamp = `${d.getFullYear()}${pad(d.getMonth() + 1)}${pad(d.getDate())}-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}`;
      return json({ ok: true, mode: mode === "replace" ? "replace" : "merge", imported: memory.files.length, backup: `pre-import-${stamp}.zip` });
    }
    if (p === "/api/uploads" && method === "POST") {
      const rawName = (q.get("name") || "file.png").replace(/^.*[\\/]/, "") || "file.png";
      const blob = init.body instanceof Blob ? init.body : new Blob([init.body || ""]);
      const id = String(++demoUploadSeq).padStart(6, "0");
      const path = `/home/alex/.collie/uploads/demo/${id}-${rawName}`;
      demoUploads.set(path, { blob, name: rawName, size: blob.size || 0 });
      return json({ path, name: rawName, size: blob.size || 0 });
    }
    if (p === "/api/super/say") return json({ ok: true, entry: collieSay(body.text || "", body.re, body.client_msg_id, !!body.pick), picked: !!body.pick });
    if (p === "/api/external") return json({ sessions: external });
    if (p === "/api/history") return json({ sessions: history });
    if (p === "/api/hubs") return json({ hubs: [] });
    if (p === "/api/version") return json({
      version: "v0.3.9", latest: "v0.3.9", outdated: null,
      machines: machines.map((x) => ({ id: x.id, name: x.name, version: x.version, local: !!x.local, online: !!x.online, outdated: false })),
    });
    if (p === "/api/local-app") return json({ url: "" });
    if (p === "/api/models") { const k = q.get("kind") === "antigravity" ? "agy" : q.get("kind"); return json(models[k] || { models: [], efforts: [] }); }
    if (p === "/api/file") {
      const fp = q.get("path") || "", mach = q.get("machine") || "", dl = q.get("download") === "1";
      const dlHead = (name) => dl ? { "Content-Disposition": `attachment; filename="${name}"` } : {};
      if (demoUploads.has(fp)) {
        const up = demoUploads.get(fp);
        const isImg = /\.(png|jpe?g|gif|webp|bmp|svg|avif)$/i.test(up.name);
        return new Response(up.blob, { headers: { "X-File-Kind": dl ? "download" : (isImg ? "image" : "text"), "X-File-Size": String(up.size), "X-File-Path": encodeURIComponent(fp), ...dlHead(up.name) } });
      }
      const demoImgs = [
        [/a1b2c3-ci-failure\.png$/i, "../img/screen.png", ""],
        [/d4e5f6-query-plan\.png$/i, "../img/agent.png", ""],
        [/images\/0d3a9e5c71b2f4e8a6c1\.png$/i, "../img/hero-dark.png", "macbook"],
        [/preview-dark\.png$/i, "../img/hero-dark.png", "macbook"],
        [/preview-light\.png$/i, "../img/hero-light.png", "macbook"],
        [/architecture\.svg$/i, "../img/architecture-light.svg", "macbook"],
        [/uploads\/.*\.png$/i, "../img/hero-dark.png", ""],
      ];
      const hitImg = demoImgs.find(([re, , wantM]) => re.test(fp) && (!wantM || mach === wantM));
      if (hitImg) {
        const b = await (await realFetch(new URL(hitImg[1], location.href))).blob();
        return new Response(b, { headers: { "X-File-Kind": dl ? "download" : "image", "X-File-Size": String(b.size), "X-File-Path": encodeURIComponent(fp), ...dlHead(fp.split("/").pop() || "image.png") } });
      }
      if (/bench-pagination\.html$/i.test(fp)) {
        const html = await (await realFetch(new URL("bench-pagination.html", location.href))).text();
        const isHtml = !dl && q.get("as") === "html";
        return new Response(html, { headers: { "Content-Type": isHtml ? "text/html; charset=utf-8" : "application/octet-stream", "X-File-Kind": dl ? "download" : (isHtml ? "html" : "text"), "X-File-Size": String(new Blob([html]).size), "X-File-Path": encodeURIComponent("~/code/shop-api/bench-pagination.html"), ...dlHead("bench-pagination.html") } });
      }
      if (!mach && /orders-benchmark\.csv$/i.test(fp)) {
        return new Response(ordersBenchmarkCsv, { headers: { "X-File-Kind": dl ? "download" : "text", "X-File-Size": String(new Blob([ordersBenchmarkCsv]).size), "X-File-Path": encodeURIComponent("~/code/shop-api/docs/orders-benchmark.csv"), ...dlHead("orders-benchmark.csv") } });
      }
      if (!mach && /perf-audit-v1\.4\.md$/.test(fp)) {
        return new Response(perfAuditMd, { headers: { "X-File-Kind": dl ? "download" : "text", "X-File-Size": String(new Blob([perfAuditMd]).size), "X-File-Path": encodeURIComponent("~/code/shop-api/docs/perf-audit-v1.4.md"), ...dlHead("perf-audit-v1.4.md") } });
      }
      if (mach === "macbook" && /CHANGELOG\.md$/.test(fp)) {
        return new Response(changelog, { headers: { "X-File-Kind": dl ? "download" : "text", "X-File-Size": String(new Blob([changelog]).size), "X-File-Path": encodeURIComponent("~/code/shop-web/CHANGELOG.md"), ...dlHead("CHANGELOG.md") } });
      }
      if (!mach && /(?:internal\/orders\/)?list\.go$/.test(fp)) {
        return new Response(ordersListGo, { headers: { "X-File-Kind": dl ? "download" : "text", "X-File-Size": String(new Blob([ordersListGo]).size), "X-File-Path": encodeURIComponent("~/code/shop-api/internal/orders/list.go"), ...dlHead("list.go") } });
      }
      return json({ error: "找不到这个文件" }, 404);
    }
    if (p === "/api/dirs") {
      const mach = q.get("machine") || "";
      const isMac = mach === "macbook";
      const home = isMac ? "/Users/alex" : "/home/alex";
      const tree = isMac ? {
        "/Users/alex": ["agent-workspace", "code", "Desktop", "Documents", ".config"],
        "/Users/alex/agent-workspace": ["collie", "shop-web"],
        "/Users/alex/code": ["shop-web", "notes", "design-system", ".cache"],
        "/Users/alex/code/shop-web": ["src", "public", "tests"],
        "/Users/alex/code/notes": ["2026", "drafts"],
        "/Users/alex/code/design-system": ["tokens", "components"],
        "/Users/alex/.config": ["collie", "herdr"],
      } : {
        "/home/alex": ["agent-workspace", "code", "logs", ".collie", ".config"],
        "/home/alex/agent-workspace": ["collie", "shop-api", "bench"],
        "/home/alex/code": ["shop-api", "migrations", "scripts", ".git"],
        "/home/alex/code/shop-api": ["cmd", "internal", "docs"],
        "/home/alex/code/shop-api/internal": ["orders", "payments", "auth"],
        "/home/alex/code/shop-api/docs": ["rfcs", "runbooks"],
        "/home/alex/.collie": ["router", "transcripts"],
        "/home/alex/.config": ["herdr"],
      };
      const recent = isMac ? [
        { path: "/Users/alex", display: "~", label: "主目录" },
        { path: "/Users/alex/agent-workspace", display: "~/agent-workspace", label: "工作区" },
        { path: "/Users/alex/code/shop-web", display: "~/code/shop-web" },
        { path: "/Users/alex/code/notes", display: "~/code/notes" },
      ] : [
        { path: "/home/alex", display: "~", label: "主目录" },
        { path: "/home/alex/agent-workspace", display: "~/agent-workspace", label: "工作区" },
        { path: "/home/alex/code/shop-api", display: "~/code/shop-api" },
        { path: "/home/alex/code", display: "~/code" },
      ];
      const toDisp = (fp) => fp === home ? "~" : fp.startsWith(home + "/") ? "~" + fp.slice(home.length) : fp;
      const raw = (q.get("path") || "").trim();
      const hiddenParam = q.get("hidden") === "1";
      const trailingSlash = raw.endsWith("/");
      const norm = (s) => {
        const exp = !s || s === "~" ? home : s.startsWith("~/") ? home + "/" + s.slice(2) : s.startsWith("/") ? s : home + "/" + s;
        const parts = [];
        for (const seg of exp.split("/")) {
          if (!seg || seg === ".") continue;
          if (seg === "..") parts.pop(); else parts.push(seg);
        }
        return "/" + parts.join("/");
      };
      const target = norm(raw);
      const exists = Object.prototype.hasOwnProperty.call(tree, target);
      const parentOf = (fp) => fp === "/" ? "" : (fp.slice(0, fp.lastIndexOf("/")) || "/");
      let browse = target, prefix = "", wantHidden = hiddenParam || raw === "." || raw.endsWith("/.");
      if (!exists) {
        const par = parentOf(target);
        if (!trailingSlash && Object.prototype.hasOwnProperty.call(tree, par)) {
          browse = par;
          prefix = target.slice(target.lastIndexOf("/") + 1);
          if (prefix.startsWith(".")) wantHidden = true;
        } else {
          let anc = par;
          while (anc && !Object.prototype.hasOwnProperty.call(tree, anc)) anc = parentOf(anc);
          browse = anc || home;
        }
      }
      const dirs = (tree[browse] || [])
        .filter((d) => (wantHidden || !d.startsWith(".")) && (!prefix || d.toLowerCase().startsWith(prefix.toLowerCase())))
        .sort((a, b) => a.toLowerCase().localeCompare(b.toLowerCase()));
      const par = parentOf(browse);
      return json({
        machine: mach || undefined, home, path: target, display: toDisp(target),
        browse_path: browse, browse_display: toDisp(browse),
        parent: par, parent_display: par ? toDisp(par) : "",
        prefix, exists, hidden: wantHidden, dirs, recent,
        note: !exists && !dirs.length ? "路径暂不存在，仍可直接手输使用" : "",
      });
    }
    if (p === "/api/devices") return json({
      devices: [
        { id: "macbook", kind: "machine", name: "macbook", os: "darwin/arm64", online: true, home: "/Users/alex", version: "v0.3.9", agents: ["claude", "codex"], created_at: ago(60 * 24 * 3), last_seen: ago(0) },
        { id: "iphone", kind: "browser", name: "iPhone · Safari", current: false, created_at: ago(60 * 24 * 3), last_seen: ago(12) },
        { id: "chrome", kind: "browser", name: "Mac · Chrome", current: true, created_at: ago(60 * 24 * 2), last_seen: ago(0) },
      ],
      hub: { name: "devbox", home: "/home/alex", herdr: true, version: "v0.3.9", agents: ["claude", "codex", "agy"] }, addresses: ["http://192.168.1.20:7788"], latest: "v0.3.9",
    });
    if (p === "/api/pair/new") return json({ code: "DEMO2026", expires: new Date(Date.now() + 600000).toISOString(), kind: body.kind });
    if ((m = /^\/api\/panes\/([^/]+)\/ask$/.exec(p))) { const id = decodeURIComponent(m[1]); return json(approvals[id] && agents.find((a) => a.pane_id === id)?.status === "blocked" ? { asking: true, approval: approvals[id] } : { asking: false }); }
    if ((m = /^\/api\/panes\/([^/]+)\/(keys|text)$/.exec(p))) {
      const id = decodeURIComponent(m[1]);
      if (approvals[id]) answerApproval(id); else if (m[2] === "text" && body.text) promptPane(id, body.text);
      return json({ ok: true });
    }
    if ((m = /^\/api\/agents\/([^/]+)\/prompt$/.exec(p))) {
      const t = decodeURIComponent(m[1]), id = agents.find((a) => a.pane_id === t || a.name === t)?.pane_id || t;
      promptPane(id, body.text || body.prompt || ""); return json({ ok: true });
    }
    if ((m = /^\/api\/panes\/([^/]+)\/(earlier|last)$/.exec(p))) return json({ messages: [], earlier: 0, text: "" });
    if (p === "/api/agents" && method === "POST") {
      const n = agents.length + 10, pane = `w${n}:p1${body.machine ? "@" + body.machine : ""}`;
      const kind = body.kind === "antigravity" ? "agy" : body.kind;
      agents.push(agent({ pane_id: pane, workspace_id: pane.split(":")[0], name: body.name || `${kind}-${n}`, kind, status: body.prompt ? "working" : "idle", cwd: (body.cwd || "~/code").replace(/^~/, body.machine ? "/Users/alex" : "/home/alex"), run: [body.model || "", body.effort || ""] }));
      chats[pane] = [];
      if (body.prompt) promptPane(pane, body.prompt); else pushAll();
      return json({ pane_id: pane, workspace_id: pane.split(":")[0] });
    }
    if (method === "POST") return json({ ok: true });
    return json({});
  };

  // The demo opens light unless this visitor picked a theme in 偏好设置.
  try { if (localStorage.getItem("collie.theme") === null) { localStorage.setItem("collie.theme", JSON.stringify("light")); document.documentElement.dataset.theme = "light"; } } catch {}

  // A demo has nothing to notify about: never ask for the permission.
  try { if (window.Notification) Object.defineProperty(Notification, "permission", { get: () => "denied" }); Notification.requestPermission = async () => "denied"; } catch {}

  // ───────────────────────────── a quiet badge: this is a demo
  if (!/[?&]clean\b/.test(location.search)) {
    addEventListener("DOMContentLoaded", () => {
      const b = document.createElement("a");
      b.href = "../"; b.className = "demo-badge"; b.textContent = "演示 · 数据是虚构的 · 返回主页";
      document.body.append(b);
    });
  }
})();
