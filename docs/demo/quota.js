/* Collie — Account Subscription Quota Badge & Popover/Sheet Controller
   Standalone module bridged with app.js via window.__collieQuota to avoid merge conflicts. */
(() => {
  "use strict";

  const $ = (s, el = document) => el.querySelector(s);
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
  const interp = (tpl, vars) => (!vars ? String(tpl ?? "") : String(tpl ?? "").replace(/\{([a-zA-Z0-9_]+)\}/g, (m, k) => (vars[k] !== undefined && vars[k] !== null ? String(vars[k]) : m)));
  const tr = (key, fallback, vars) => (window.CollieI18n ? window.CollieI18n.t(key, vars, fallback) : interp(fallback, vars));
  const locQ = (s) => (window.CollieI18n?.localizeQuotaText ? window.CollieI18n.localizeQuotaText(s) : String(s ?? ""));

  const CACHE_TTL_MS = 3 * 60 * 1000;
  const LOW_THRESHOLD = 20;
  const getClaudeOptinGuide = () => tr("quota.claudeGuide", "Collie 绝不修改 ~/.claude/settings.json，也不抓取私有 OAuth 接口；若你自行配置了 Claude Code 官方 statusLine 写入本地缓存，Collie 会自动读取并展示 5 小时与 7 天余量。");

  function isNarrow() {
    return typeof matchMedia === "function" && matchMedia("(max-width: 760px)").matches;
  }

  function isSafeHttpsURL(raw) {
    try {
      const u = new URL(String(raw || ""));
      return u.protocol === "https:";
    } catch {
      return false;
    }
  }

  function isWindowExpired(win, nowMs) {
    if (!win?.resets_at) return false;
    const t = Date.parse(win.resets_at);
    return Number.isFinite(t) && t <= nowMs;
  }

  function formatResetCountdown(resetsAt, remainingPct, nowMs = Date.now()) {
    if (!resetsAt) {
      return remainingPct === 100 ? tr("quota.unused", "尚未消耗") : "";
    }
    const target = Date.parse(resetsAt);
    if (!Number.isFinite(target)) return "";
    const diff = target - nowMs;
    if (diff <= 0) return tr("quota.resetDue", "已到重置点，待刷新");
    const totalMin = Math.ceil(diff / 60000);
    if (totalMin < 1) return tr("quota.resetLt1m", "<1m 后重置");
    if (totalMin < 60) return tr("quota.resetMin", "{m}m 后重置", { m: totalMin });
    const totalH = Math.floor(totalMin / 60);
    const remM = totalMin % 60;
    if (totalH < 24) return remM > 0 ? tr("quota.resetHrMin", "{h}h {m}m 后重置", { h: totalH, m: remM }) : tr("quota.resetHr", "{h}h 后重置", { h: totalH });
    const days = Math.floor(totalH / 24);
    const remH = totalH % 24;
    return remH > 0 ? tr("quota.resetDayHr", "{d}d {h}h 后重置", { d: days, h: remH }) : tr("quota.resetDay", "{d}d 后重置", { d: days });
  }

  function formatUpdatedAgo(updatedAt, nowMs = Date.now()) {
    if (!updatedAt) return tr("quota.neverSynced", "尚未同步");
    const t = Date.parse(updatedAt);
    if (!Number.isFinite(t)) return tr("quota.neverSynced", "尚未同步");
    const sec = Math.max(0, Math.floor((nowMs - t) / 1000));
    if (sec < 45) return tr("quota.justSynced", "刚刚同步");
    const min = Math.floor(sec / 60);
    if (min < 60) return tr("common.minsAgo", "{n} 分钟前", { n: min });
    const hr = Math.floor(min / 60);
    if (hr < 24) return tr("common.hoursAgo", "{n} 小时前", { n: hr });
    return tr("common.daysAgo", "{n} 天前", { n: Math.floor(hr / 24) });
  }

  // ─── Model-Aware Bottleneck Selection ───
  const EFFORT_SUFFIXES = new Set([
    "off", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto", "thinking",
  ]);
  const PAREN_EFFORT_RE = /\s*\((off|none|minimal|low|medium|high|xhigh|max|ultra|auto|thinking)\)\s*$/i;
  const TRAIL_EFFORT_RE = /[-:](off|none|minimal|low|medium|high|xhigh|max|ultra|auto|thinking)$/i;
  const SCOPE_SPLIT_RE = /(?:\s*\/\s*|\s*[,&+、·]\s*|\s+and\s+|\s+和\s+)/i;
  const ZH_NOISE_RE = /(?:\d+\s*(?:分钟|小时|天)(?:窗口)?|周额度|日额度|月额度|主窗口|副窗口|周期额度|默认窗口|模型池|模型|额度|窗口|池)/g;
  const EN_NOISE_RE = /(?:^|[\s_-])(?:five[\s_-]?hour|seven[\s_-]?day|thirty[\s_-]?day|24[\s_-]?hours?|5[\s_-]?hours?|7[\s_-]?days?|30[\s_-]?days?|\d+[mhd]|weekly|daily|monthly|primary|secondary|window|limits?|remaining|models?|pool|quota|default|all|shared|general|overall|account)(?=$|[\s_-])/gi;

  function normalizeQuotaKind(kind) {
    const k = String(kind || "").trim().toLowerCase();
    if (k === "agy" || k === "antigravity") return "agy";
    if (k === "cursor" || k === "cursor-agent") return "cursor";
    if (k === "omp" || k === "oh-my-pi") return "omp";
    return k;
  }

  function stripProviderPrefix(raw) {
    const s = String(raw || "").trim();
    if (!s) return "";
    if (s.includes("/") && !/\s\/\s/.test(s)) {
      const parts = s.split("/").filter(Boolean);
      if (parts.length >= 2) return parts[parts.length - 1];
    }
    return s;
  }

  function shortModelLabel(raw) {
    const s = stripProviderPrefix(raw);
    return s.length > 32 ? s.slice(0, 31) + "…" : s;
  }

  function canonicalSlug(raw) {
    return String(raw || "")
      .trim()
      .toLowerCase()
      .replace(/[\s._:]+/g, "-")
      .replace(/[^a-z0-9-]+/g, "-")
      .replace(/-+/g, "-")
      .replace(/^-|-$/g, "");
  }

  function parseModelDescriptor(raw, { stripProvider = false } = {}) {
    let s = String(raw || "").trim();
    if (stripProvider) s = stripProviderPrefix(s);
    if (!s) return { rawSlug: "", baseSlug: "", effort: "", tokens: [], familyAliases: new Set() };

    let effort = "";
    let working = s;
    for (let i = 0; i < 3; i++) {
      const pm = working.match(PAREN_EFFORT_RE);
      if (!pm) break;
      const e = pm[1].toLowerCase();
      if (!effort && e !== "thinking") effort = e;
      working = working.slice(0, pm.index).trim();
    }
    const rawSlug = canonicalSlug(working);
    let baseWorking = working;
    for (let i = 0; i < 3; i++) {
      const tm = baseWorking.match(TRAIL_EFFORT_RE);
      if (!tm || tm.index <= 0) break;
      const e = tm[1].toLowerCase();
      if (!effort && e !== "thinking") effort = e;
      baseWorking = baseWorking.slice(0, tm.index).trim();
    }
    const baseSlug = canonicalSlug(baseWorking) || rawSlug;
    const tokens = baseSlug ? baseSlug.split("-").filter(Boolean) : [];
    const familyAliases = new Set();
    if (tokens.some((t) => t === "claude" || t === "sonnet" || t === "opus" || t === "haiku")) {
      familyAliases.add("claude");
    }
    if (tokens.some((t) => t === "gpt" || t === "chatgpt" || t === "codex" || t === "openai" || /^o[134]$/.test(t))) {
      familyAliases.add("gpt");
      familyAliases.add("openai");
    }
    if (tokens.includes("gemini")) {
      familyAliases.add("gemini");
    }
    return { rawSlug, baseSlug, effort, tokens, familyAliases };
  }

  function normalizeModelKey(raw) {
    return parseModelDescriptor(raw, { stripProvider: true }).baseSlug;
  }

  function cleanScopeSegment(raw) {
    let s = String(raw || "").trim();
    if (!s) return "";
    s = s.replace(ZH_NOISE_RE, " ");
    for (let i = 0; i < 3; i++) {
      const next = s.replace(EN_NOISE_RE, " ");
      if (next === s) break;
      s = next;
    }
    return s.replace(/^[\s\-_.:·]+|[\s\-_.:·]+$/g, "").trim();
  }

  function extractScopeSegments(raw) {
    const str = String(raw || "").trim();
    if (!str) return [];
    const out = [];
    for (const part of str.split(SCOPE_SPLIT_RE)) {
      const cleaned = cleanScopeSegment(part);
      if (!cleaned) continue;
      const desc = parseModelDescriptor(cleaned, { stripProvider: false });
      if (!desc.baseSlug || !/[a-z]/.test(desc.baseSlug)) continue;
      if (EFFORT_SUFFIXES.has(desc.baseSlug)) continue;
      out.push(cleaned);
    }
    return out;
  }

  function scoreModelMatch(modelRaw, scopeRaw) {
    const m = parseModelDescriptor(modelRaw, { stripProvider: true });
    const s = parseModelDescriptor(scopeRaw, { stripProvider: true });
    if (!m.baseSlug || !s.baseSlug) return 0;

    // If a bucket explicitly specifies an effort tier that differs from the active model's effort, skip.
    if (s.effort && m.effort && s.effort !== m.effort && m.baseSlug === s.baseSlug) {
      return 0;
    }
    // Tier 1: exact raw ID match (including effort suffix if present on both)
    if (m.rawSlug && s.rawSlug && m.rawSlug === s.rawSlug) return 100;
    // Tier 2: exact normalized base model match (effort suffix stripped)
    if (m.baseSlug === s.baseSlug) return 90;
    // Tier 3: token-boundary prefix match (same series, e.g. gemini-4-neon -> gemini-4 or gemini)
    if (m.baseSlug.startsWith(s.baseSlug + "-")) {
      return 70 + Math.min(18, s.baseSlug.length);
    }
    if (s.baseSlug.startsWith(m.baseSlug + "-")) {
      return 65 + Math.min(18, m.baseSlug.length);
    }
    // Tier 4: subset token match (all tokens of the narrower descriptor appear in the other, with >=1 alpha token)
    const mSet = new Set(m.tokens);
    const sSet = new Set(s.tokens);
    if (s.tokens.length > 0 && s.tokens.some((t) => /[a-z]/.test(t)) && s.tokens.every((t) => mSet.has(t))) {
      return 60 + Math.min(9, s.tokens.length * 3);
    }
    if (m.tokens.length > 0 && m.tokens.some((t) => /[a-z]/.test(t)) && m.tokens.every((t) => sSet.has(t))) {
      return 58 + Math.min(9, m.tokens.length * 3);
    }
    // Tier 5: family alias match (e.g. sonnet -> claude, o3-mini -> gpt)
    if (s.tokens.length === 1 && m.familyAliases.has(s.baseSlug)) {
      return 50 + Math.min(9, s.baseSlug.length);
    }
    return 0;
  }

  function collectPoolScopes(pool) {
    const out = [];
    const seen = new Set();
    const push = (seg) => {
      const k = canonicalSlug(seg);
      if (!k || seen.has(k)) return;
      seen.add(k);
      out.push(seg);
    };
    if (Array.isArray(pool?.models)) {
      for (const m of pool.models) {
        const t = String(m || "").trim();
        if (t) push(t);
      }
    }
    for (const seg of extractScopeSegments(pool?.group)) push(seg);
    return out;
  }

  function collectWindowScopes(win) {
    const out = [];
    const seen = new Set();
    const push = (seg) => {
      const k = canonicalSlug(seg);
      if (!k || seen.has(k)) return;
      seen.add(k);
      out.push(seg);
    };
    if (win?.model) push(String(win.model).trim());
    if (Array.isArray(win?.models)) {
      for (const m of win.models) {
        const t = String(m || "").trim();
        if (t) push(t);
      }
    }
    for (const seg of extractScopeSegments(win?.id)) push(seg);
    for (const seg of extractScopeSegments(win?.label)) push(seg);
    return out;
  }

  // selectAgentBottleneck selects the bottleneck (minimum remaining %) window
  // within one agent, scoping to buckets matching `model` when available and
  // safely falling back to default/all active windows of that agent otherwise.
  function selectAgentBottleneck(ag, model = "", nowMs = Date.now()) {
    if (!ag || ag.status !== "ok" || !Array.isArray(ag.pools)) return null;
    const entries = [];
    for (let poolIndex = 0; poolIndex < ag.pools.length; poolIndex++) {
      const pool = ag.pools[poolIndex];
      const poolScopes = collectPoolScopes(pool);
      const poolIsScoped = poolScopes.length > 0;
      const wins = Array.isArray(pool?.windows) ? pool.windows : [];
      for (let winIndex = 0; winIndex < wins.length; winIndex++) {
        const win = wins[winIndex];
        if (typeof win?.remaining_pct !== "number" || !Number.isFinite(win.remaining_pct)) continue;
        if (isWindowExpired(win, nowMs)) continue;
        const winScopes = collectWindowScopes(win);
        const allScopes = [...poolScopes, ...winScopes];
        const pct = Math.max(0, Math.min(100, Math.round(win.remaining_pct)));
        entries.push({
          kind: ag.kind || "",
          agentName: ag.name || ag.kind || "",
          poolIndex,
          winIndex,
          pool,
          win,
          pct,
          poolIsScoped,
          isScoped: allScopes.length > 0,
          allScopes,
          matchScore: 0,
        });
      }
    }
    if (!entries.length) return null;

    const cleanModel = String(model || "").trim();
    let maxScore = 0;
    if (cleanModel) {
      for (const e of entries) {
        let best = 0;
        for (const sc of e.allScopes) {
          const scScore = scoreModelMatch(cleanModel, sc);
          if (scScore > best) best = scScore;
        }
        e.matchScore = best;
        if (best > maxScore) maxScore = best;
      }
    }

    let candidates;
    let matchMode;
    if (cleanModel && maxScore > 0) {
      const matched = entries.filter((e) => e.matchScore === maxScore);
      const unscopedPoolIndices = new Set(matched.filter((e) => !e.poolIsScoped).map((e) => e.poolIndex));
      if (unscopedPoolIndices.size > 0) {
        const picked = new Set(matched);
        for (const e of entries) {
          if (!e.isScoped && unscopedPoolIndices.has(e.poolIndex)) picked.add(e);
        }
        candidates = entries.filter((e) => picked.has(e));
      } else {
        candidates = matched;
      }
      matchMode = "model";
    } else {
      const hasScopedPool = entries.some((e) => e.poolIsScoped);
      const unscoped = entries.filter((e) => !e.isScoped);
      if (hasScopedPool && unscoped.length > 0) {
        candidates = unscoped;
        matchMode = "agent";
      } else {
        candidates = entries;
        matchMode = "agent";
      }
    }

    let best = candidates[0];
    for (let i = 1; i < candidates.length; i++) {
      if (candidates[i].pct < best.pct) best = candidates[i];
    }
    return { best, candidates, matchMode, min: best.pct };
  }

  function normActiveContext(ctx) {
    if (!ctx || typeof ctx !== "object") return { kind: "", model: "", source: "" };
    return {
      kind: normalizeQuotaKind(ctx.kind),
      model: String(ctx.model || "").trim(),
      source: String(ctx.source || "").trim(),
    };
  }

  function formatBucketTitle(pool, win) {
    const winLbl = locQ(win?.label || win?.id || "");
    if (pool?.compact_full && pool?.group) return pool.group;
    if (pool?.group && winLbl) return `${pool.group} · ${winLbl}`;
    return winLbl || pool?.group || "";
  }

  // summarizeQuota extracts the bottleneck remaining % scoped to the active
  // Agent & Model (when provided and available), falling back safely to the
  // lowest remaining window across all numeric agents otherwise.
  function summarizeQuota(snap, nowMs = Date.now(), activeCtx = null) {
    const agents = Array.isArray(snap?.agents) ? snap.agents : [];
    const ctx = normActiveContext(activeCtx);

    if (ctx.kind) {
      const activeAg = agents.find((ag) => normalizeQuotaKind(ag?.kind) === ctx.kind && ag?.status === "ok");
      const sel = activeAg ? selectAgentBottleneck(activeAg, ctx.model, nowMs) : null;
      if (sel) {
        const minVal = sel.min;
        const level = minVal === 0 ? "exhausted" : minVal < LOW_THRESHOLD ? "low" : "normal";
        const prefix = level === "exhausted"
          ? tr("quota.exhaustedPrefix", "额度已用尽")
          : level === "low"
            ? tr("quota.lowPrefix", "额度偏低")
            : tr("quota.title", "订阅额度");
        const bTitle = formatBucketTitle(sel.best.pool, sel.best.win);
        const summary = bTitle ? `${activeAg.kind} · ${bTitle} ${minVal}%` : `${activeAg.kind} ${minVal}%`;
        return {
          known: true,
          min: minVal,
          level,
          tooltip: `${prefix} · ${summary}`,
          shortPct: `${minVal}%`,
          summary,
          scope: sel.matchMode,
          activeKind: ctx.kind,
          activeModel: ctx.model,
          bottleneck: {
            kind: activeAg.kind || "",
            poolIndex: sel.best.poolIndex,
            winIndex: sel.best.winIndex,
            group: sel.best.pool?.group || "",
            label: sel.best.win?.label || sel.best.win?.id || "",
            compact: !!sel.best.pool?.compact_full,
            pct: minVal,
          },
        };
      }
    }

    const items = [];
    let globalBest = null;
    for (const ag of agents) {
      const sel = selectAgentBottleneck(ag, "", nowMs);
      if (!sel) continue;
      items.push({ kind: ag.kind || "", name: ag.name || ag.kind || "", min: sel.min, best: sel.best });
      if (!globalBest || sel.min < globalBest.pct) {
        globalBest = sel.best;
      }
    }
    if (!items.length || !globalBest) {
      return {
        known: false,
        min: null,
        level: "unknown",
        tooltip: tr("quota.clickToView", "订阅额度 · 点击查看"),
        shortPct: "",
        summary: tr("common.view", "查看"),
        scope: "unknown",
        activeKind: ctx.kind,
        activeModel: ctx.model,
        bottleneck: null,
      };
    }
    const overallMin = globalBest.pct;
    const level = overallMin === 0 ? "exhausted" : overallMin < LOW_THRESHOLD ? "low" : "normal";
    const ordered = level === "normal" ? items : [...items].sort((a, b) => a.min - b.min);
    const summary = ordered.slice(0, 3).map((x) => `${x.kind} ${x.min}%`).join(" · ");
    const prefix = level === "exhausted"
      ? tr("quota.exhaustedPrefix", "额度已用尽")
      : level === "low"
        ? tr("quota.lowPrefix", "额度偏低")
        : tr("quota.title", "订阅额度");
    return {
      known: true,
      min: overallMin,
      level,
      tooltip: `${prefix} · ${summary}`,
      shortPct: `${overallMin}%`,
      summary,
      scope: ctx.kind ? "fallback" : "all",
      activeKind: ctx.kind,
      activeModel: ctx.model,
      bottleneck: {
        kind: globalBest.kind || "",
        poolIndex: globalBest.poolIndex,
        winIndex: globalBest.winIndex,
        group: globalBest.pool?.group || "",
        label: globalBest.win?.label || globalBest.win?.id || "",
        compact: !!globalBest.pool?.compact_full,
        pct: overallMin,
      },
    };
  }

  let hostAPI = null;
  let hostToast = null;
  let hostGetContext = null;
  let activeContext = { kind: "", model: "", source: "" };
  let activeContextSig = "";
  let initialized = false;
  let wired = false;
  let snapshot = { updated_at: "", agents: [] };
  let isRefreshing = false;
  let lastTrigger = null;
  let pendingCloseTrigger = null;
  let claudeGuideOpen = false;
  let cooldownUntil = 0;
  let cooldownTimer = null;
  let openTickTimer = null;
  let fallbackScrim = null;

  function setContext(nextCtx, { silent = false } = {}) {
    const norm = normActiveContext(nextCtx);
    const sig = `${norm.kind}|${norm.model}|${norm.source}`;
    if (sig === activeContextSig) return false;
    activeContextSig = sig;
    activeContext = norm;
    if (!silent) {
      renderTriggers();
      const pop = $("#quota-popover");
      if (isPopoverOpen(pop)) renderPopoverBody();
    }
    return true;
  }

  function onContextChange() {
    if (typeof hostGetContext !== "function") return false;
    try {
      return setContext(hostGetContext());
    } catch {
      return false;
    }
  }

  function hasNativePopover(el) {
    return !!(el && typeof el.showPopover === "function" && typeof el.hidePopover === "function");
  }

  function isPopoverOpen(pop) {
    if (!pop) return false;
    if (hasNativePopover(pop)) {
      try { return pop.matches(":popover-open"); } catch {}
    }
    return pop.classList.contains(":popover-open");
  }

  function applyRing(el, val, level) {
    if (!el) return;
    el.classList.remove("is-low", "is-exhausted", "is-unknown");
    if (level === "unknown" || val === null || !Number.isFinite(val)) {
      el.classList.add("is-unknown");
      el.setAttribute("value", "0");
      el.style.setProperty("--q-val", "0");
      return;
    }
    const clamped = Math.max(0, Math.min(100, Math.round(val)));
    el.setAttribute("value", String(clamped));
    el.style.setProperty("--q-val", String(clamped));
    if (level === "low") el.classList.add("is-low");
    if (level === "exhausted") el.classList.add("is-exhausted");
  }

  function renderTriggers(nowMs = Date.now()) {
    const sum = summarizeQuota(snapshot, nowMs, activeContext);
    const warn = sum.level === "low" || sum.level === "exhausted";
    const danger = sum.level === "exhausted";

    const navBtn = $("#nav-quota");
    const navRing = $("#nav-quota-ring");
    const navDot = $("#nav-quota-dot");
    if (navBtn) {
      applyRing(navRing, sum.min, sum.level);
      navBtn.title = sum.tooltip;
      navBtn.setAttribute("aria-label", sum.tooltip);
      if (navDot) {
        navDot.hidden = !warn;
        navDot.classList.toggle("is-danger", danger);
      }
    }

    const railBtn = $("#rail-quota");
    const railRing = $("#rail-quota-ring");
    const railDot = $("#rail-quota-dot");
    if (railBtn) {
      applyRing(railRing, sum.min, sum.level);
      railBtn.title = sum.tooltip;
      railBtn.setAttribute("aria-label", sum.tooltip);
      if (railDot) {
        railDot.hidden = !warn;
        railDot.classList.toggle("is-danger", danger);
      }
    }

    // Mobile main chat header: 0 extra widgets; only a 5px corner dot on ⋯ when <20%.
    const mobMoreDot = $("#collie-more-quota-dot");
    if (mobMoreDot) {
      mobMoreDot.hidden = !warn;
      mobMoreDot.classList.toggle("is-danger", danger);
    }

    const menuSub = $("#menu-quota-sub");
    if (menuSub) {
      menuSub.textContent = sum.known ? (warn ? tr("quota.minPct", "最低 {pct}", { pct: sum.shortPct }) : sum.shortPct) : tr("common.view", "查看");
      menuSub.classList.toggle("is-low", sum.level === "low");
      menuSub.classList.toggle("is-exhausted", danger);
    }
  }

  function syncRefreshControls(nowMs = Date.now()) {
    const btn = $("#qp-refresh-btn");
    const upd = $("#qp-updated");
    const cd = $("#qp-cooldown");
    const remSec = Math.max(0, Math.ceil((cooldownUntil - nowMs) / 1000));
    if (btn) {
      btn.classList.toggle("is-spinning", isRefreshing);
      btn.disabled = isRefreshing || remSec > 0;
    }
    if (upd) {
      upd.textContent = isRefreshing ? tr("quota.syncing", "同步中…") : formatUpdatedAgo(snapshot?.updated_at, nowMs);
    }
    if (cd) {
      cd.textContent = !isRefreshing && remSec > 0 ? tr("quota.cooldown", "{s}s 后可再刷", { s: remSec }) : "";
    }
    if (remSec <= 0 && cooldownTimer) {
      clearInterval(cooldownTimer);
      cooldownTimer = null;
    }
  }

  function startCooldown(seconds) {
    const sec = Number(seconds) || 0;
    if (sec <= 0) return;
    cooldownUntil = Math.max(cooldownUntil, Date.now() + sec * 1000);
    syncRefreshControls();
    if (!cooldownTimer) {
      cooldownTimer = setInterval(() => syncRefreshControls(), 1000);
    }
  }

  function renderPopoverBody(nowMs = Date.now()) {
    const body = $("#qp-body");
    if (!body) return;
    syncRefreshControls(nowMs);

    const agents = Array.isArray(snapshot?.agents) ? snapshot.agents : [];
    if (!agents.length) {
      const msg = isRefreshing || snapshot?.refreshing
        ? tr("quota.emptyLoading", "正在读取本机 Agent 订阅额度…")
        : tr("quota.emptyNone", "未检测到支持额度查询的本机 Agent");
      body.innerHTML = `<div class="qp-empty">${esc(msg)}</div>`;
      return;
    }

    const sum = summarizeQuota(snapshot, nowMs, activeContext);
    const bn = sum.bottleneck;
    const isScopedActive = sum.scope === "model" || sum.scope === "agent";
    const bnTagText = isScopedActive
      ? tr("quota.activeMinTag", "当前最低")
      : tr("quota.minTag", "最低");

    const parts = [];
    for (const ag of agents) {
      let agMin = Infinity;
      let anyExpired = false;
      let activeWin = 0;
      if (ag.status === "ok" && Array.isArray(ag.pools)) {
        for (const p of ag.pools) {
          for (const w of (p.windows || [])) {
            if (isWindowExpired(w, nowMs)) {
              anyExpired = true;
              continue;
            }
            if (typeof w.remaining_pct === "number") {
              activeWin++;
              if (w.remaining_pct < agMin) agMin = w.remaining_pct;
            }
          }
        }
      }
      const agLevel = activeWin > 0
        ? (agMin === 0 ? "exhausted" : agMin < LOW_THRESHOLD ? "low" : "normal")
        : (anyExpired ? "expired" : "normal");

      let stateHtml = "";
      if (agLevel === "exhausted") stateHtml = `<span class="qa-state is-exhausted">${esc(tr("quota.stateExhausted", "已触限"))}</span>`;
      else if (agLevel === "low") stateHtml = `<span class="qa-state is-low">${esc(tr("quota.stateLow", "余量偏低"))}</span>`;
      else if (agLevel === "expired") stateHtml = `<span class="qa-state">${esc(tr("quota.stateExpired", "已到重置点"))}</span>`;

      const isActiveAg = !!(bn && ag.kind === bn.kind && isScopedActive);
      const activeModelBadge = isActiveAg && sum.activeModel
        ? `<span class="qa-model" title="${esc(sum.activeModel)}">${esc(shortModelLabel(sum.activeModel))}</span>`
        : "";

      let innerHtml = "";
      if (ag.status === "ok" && Array.isArray(ag.pools) && ag.pools.length) {
        const rows = [];
        let wIdx = 0;
        for (let poolIdx = 0; poolIdx < ag.pools.length; poolIdx++) {
          const pool = ag.pools[poolIdx];
          if (pool.compact_full) {
            const label = pool.group
              ? tr("quota.poolFullGroup", "{group} 池 · 100% 满额", { group: pool.group })
              : tr("quota.poolFullOther", "其余模型池 · 100% 满额");
            const isBnPool = !!(bn && bn.kind === ag.kind && bn.poolIndex === poolIdx);
            if (isBnPool) {
              rows.push(`
                <div class="qm-compact is-bottleneck" data-quota-bottleneck="true">
                  <span class="qm-left">
                    <span class="qm-lbl">${esc(label)}</span>
                    <span class="qm-tag">${esc(bnTagText)}</span>
                  </span>
                </div>
              `);
            } else {
              rows.push(`<div class="qm-compact"><span>${esc(label)}</span></div>`);
            }
            continue;
          }
          const wins = pool.windows || [];
          for (let winIdx = 0; winIdx < wins.length; winIdx++) {
            const w = wins[winIdx];
            wIdx++;
            const pct = Math.max(0, Math.min(100, Math.round(Number(w.remaining_pct) || 0)));
            const expired = isWindowExpired(w, nowMs);
            const wLevel = expired ? "normal" : (pct === 0 ? "exhausted" : pct < LOW_THRESHOLD ? "low" : "normal");
            const cls = wLevel === "low" ? "is-low" : wLevel === "exhausted" ? "is-exhausted" : "";
            const isBnWin = !expired && !!(bn && bn.kind === ag.kind && bn.poolIndex === poolIdx && bn.winIndex === winIdx);
            const winLbl = locQ(w.label || w.id);
            const title = pool.group ? `${pool.group} · ${winLbl}` : winLbl;
            const resetTxt = formatResetCountdown(w.resets_at, pct, nowMs);
            const lblId = `qm-${esc(ag.kind)}-${wIdx}`;
            const baseValText = tr("quota.progressAria", "剩余 {pct}%{reset}", {
              pct,
              reset: resetTxt ? tr("quota.progressComma", "，{reset}", { reset: resetTxt }) : "",
            });
            const valText = isBnWin
              ? baseValText + tr("quota.progressBottleneck", "，{tag}", { tag: bnTagText })
              : baseValText;
            const rowCls = [
              "qm-row",
              expired ? "is-expired" : "",
              isBnWin ? "is-bottleneck" : "",
              isBnWin && cls ? cls : "",
            ].filter(Boolean).join(" ");
            rows.push(`
              <div class="${rowCls}"${isBnWin ? ' data-quota-bottleneck="true"' : ""}>
                <div class="qm-top">
                  <span class="qm-left">
                    <span class="qm-lbl" id="${lblId}">${esc(title)}</span>
                    ${isBnWin ? `<span class="qm-tag${cls ? " " + cls : ""}">${esc(bnTagText)}</span>` : ""}
                    ${resetTxt ? `<span class="qm-reset">${esc(resetTxt)}</span>` : ""}
                  </span>
                  <span class="qm-pct ${cls}">${pct}%</span>
                </div>
                <progress class="q-bar ${cls}" value="${pct}" max="100" aria-labelledby="${lblId}" aria-valuetext="${esc(valText)}"></progress>
              </div>
            `);
          }
        }
        innerHtml = `<div class="qa-meters">${rows.join("")}</div>`;
        if (ag.note) {
          innerHtml += `<div class="qa-note is-sub"><span>${esc(locQ(ag.note))}</span></div>`;
        }
      } else if (ag.status === "optin") {
        const toggleTxt = claudeGuideOpen ? tr("quota.hideGuide", "收起说明") : tr("int.guide", "说明");
        innerHtml = `
          <div class="qa-note">
            <span>${esc(ag.note ? locQ(ag.note) : tr("quota.optinFallback", "按对话被动同步 · 未开启状态行采集"))}</span>
            <button type="button" class="qa-link" data-quota-guide="claude" aria-expanded="${claudeGuideOpen ? "true" : "false"}">${esc(toggleTxt)}</button>
          </div>
          ${claudeGuideOpen ? `<div class="qa-guide">${esc(getClaudeOptinGuide()).replace("~/.claude/settings.json", "<code>~/.claude/settings.json</code>")}</div>` : ""}
        `;
      } else if (ag.status === "external") {
        const link = isSafeHttpsURL(ag.link_url)
          ? `<a class="qa-link" href="${esc(ag.link_url)}" target="_blank" rel="noopener noreferrer">${esc(ag.link_text ? locQ(ag.link_text) : tr("quota.consoleLink", "控制台 ↗"))}</a>`
          : "";
        innerHtml = `
          <div class="qa-note">
            <span>${esc(ag.note ? locQ(ag.note) : tr("quota.externalFallback", "CLI 未开放实时额度接口"))}</span>
            ${link}
          </div>
        `;
      } else {
        innerHtml = `<div class="qa-note"><span>${esc(ag.note ? locQ(ag.note) : tr("quota.unavailFallback", "暂不可用"))}</span></div>`;
      }

      parts.push(`
        <section class="qa-sec${isActiveAg ? " is-active-agent" : ""}" data-quota-kind="${esc(ag.kind)}">
          <div class="qa-head">
            <div class="qa-id">
              <h3 class="qa-name">${esc(ag.name || ag.kind)}</h3>
              ${ag.plan ? `<span class="qa-plan">${esc(locQ(ag.plan))}</span>` : ""}
              ${activeModelBadge}
            </div>
            ${stateHtml}
          </div>
          ${innerHtml}
        </section>
      `);
    }
    body.innerHTML = parts.join("");
  }

  function clearPopoverPosition(pop) {
    pop.classList.remove("pos-anchor-full", "pos-anchor-rail", "pos-manual", "is-sheet");
    pop.style.left = "";
    pop.style.right = "";
    pop.style.top = "";
    pop.style.bottom = "";
  }

  function resolveTrigger(triggerEl) {
    if (triggerEl && triggerEl.getBoundingClientRect && triggerEl.getBoundingClientRect().width > 0) {
      return triggerEl;
    }
    const app = $("#app");
    if (app?.classList.contains("side-collapsed") && !isNarrow()) {
      return $("#rail-quota") || $("#nav-quota");
    }
    return $("#nav-quota") || $("#rail-quota");
  }

  function positionPopover(pop, triggerEl) {
    clearPopoverPosition(pop);
    if (isNarrow()) {
      pop.classList.add("is-sheet");
      return;
    }

    const navBtn = $("#nav-quota");
    const railBtn = $("#rail-quota");
    const supportsAnchor = typeof CSS !== "undefined" && typeof CSS.supports === "function" && CSS.supports("position-area: block-start");
    const anchor = resolveTrigger(triggerEl);

    if (supportsAnchor && anchor === navBtn) {
      pop.classList.add("pos-anchor-full");
      return;
    }
    if (supportsAnchor && anchor === railBtn) {
      pop.classList.add("pos-anchor-rail");
      return;
    }

    // Manual clamped positioning fallback (e.g. Firefox or opened via ⋯ menu when sidebar collapsed/hidden)
    pop.classList.add("pos-manual");
    const popW = Math.min(326, window.innerWidth - 24);
    const r = anchor?.getBoundingClientRect?.() || { left: 12, right: 54, top: window.innerHeight - 48, bottom: window.innerHeight - 10 };
    if (anchor === railBtn) {
      pop.style.left = `${Math.max(10, Math.min(window.innerWidth - popW - 12, Math.round(r.right + 12)))}px`;
      pop.style.bottom = `${Math.max(12, Math.round(window.innerHeight - r.bottom))}px`;
    } else {
      pop.style.left = `${Math.max(10, Math.min(window.innerWidth - popW - 12, Math.round(r.left)))}px`;
      pop.style.bottom = `${Math.max(12, Math.round(window.innerHeight - r.top + 8))}px`;
    }
  }

  function setExpandedAria(open) {
    for (const btn of [$("#nav-quota"), $("#rail-quota")]) {
      if (!btn) continue;
      btn.setAttribute("aria-expanded", open && btn === lastTrigger ? "true" : "false");
    }
  }

  function startOpenTicker() {
    stopOpenTicker();
    openTickTimer = setInterval(() => {
      const pop = $("#quota-popover");
      if (!isPopoverOpen(pop)) {
        stopOpenTicker();
        return;
      }
      renderPopoverBody();
      renderTriggers();
    }, 30000);
  }

  function stopOpenTicker() {
    if (openTickTimer) {
      clearInterval(openTickTimer);
      openTickTimer = null;
    }
  }

  function removeFallbackScrim() {
    if (fallbackScrim) {
      fallbackScrim.remove();
      fallbackScrim = null;
    }
  }

  function hidePopoverEl(pop) {
    if (!pop || !isPopoverOpen(pop)) return;
    if (hasNativePopover(pop)) {
      pop.hidePopover();
    } else {
      pop.classList.remove(":popover-open");
      removeFallbackScrim();
      setExpandedAria(false);
      stopOpenTicker();
    }
  }

  function showPopoverEl(pop) {
    if (!pop) return;
    if (hasNativePopover(pop)) {
      pop.showPopover();
    } else {
      pop.classList.add(":popover-open");
      if (pop.classList.contains("is-sheet")) {
        removeFallbackScrim();
        fallbackScrim = document.createElement("div");
        fallbackScrim.className = "quota-fallback-scrim";
        fallbackScrim.addEventListener("click", () => hidePopoverEl(pop));
        document.body.append(fallbackScrim);
      }
      setExpandedAria(true);
      startOpenTicker();
    }
  }

  function isSnapshotStale(nowMs = Date.now()) {
    if (!snapshot?.updated_at) return true;
    const t = Date.parse(snapshot.updated_at);
    if (!Number.isFinite(t) || nowMs - t >= CACHE_TTL_MS) return true;
    for (const ag of (snapshot.agents || [])) {
      for (const p of (ag.pools || [])) {
        for (const w of (p.windows || [])) {
          if (isWindowExpired(w, nowMs)) return true;
        }
      }
    }
    return false;
  }

  async function fetchQuota() {
    if (!hostAPI) return;
    try {
      const data = await hostAPI("/api/quota");
      applySnapshot(data);
    } catch {
      // Quietly keep previous state; never disrupt main chat on quota errors.
      isRefreshing = false;
      syncRefreshControls();
    }
  }

  async function manualRefresh() {
    if (!hostAPI || isRefreshing) return;
    if (cooldownUntil > Date.now()) {
      syncRefreshControls();
      return;
    }
    isRefreshing = true;
    syncRefreshControls();
    try {
      const data = await hostAPI("/api/quota/refresh", { method: "POST", body: {} });
      isRefreshing = false;
      applySnapshot(data);
      const cd = Number(data?.refresh_cooldown_sec) || 30;
      startCooldown(cd);
      const live = $("#quota-live-status");
      if (live) live.textContent = tr("quota.liveSynced", "已同步最新订阅额度，未消耗模型额度");
    } catch (err) {
      isRefreshing = false;
      syncRefreshControls();
      hostToast?.(err?.message || tr("quota.refreshFail", "刷新订阅额度失败"), "attn");
    }
  }

  function applySnapshot(data) {
    if (!data || typeof data !== "object") return;
    snapshot = {
      updated_at: data.updated_at || "",
      stale: !!data.stale,
      refreshing: !!data.refreshing,
      refresh_cooldown_sec: Number(data.refresh_cooldown_sec) || 0,
      agents: Array.isArray(data.agents) ? data.agents : [],
    };
    isRefreshing = !!snapshot.refreshing;
    if (snapshot.refresh_cooldown_sec > 0) {
      startCooldown(snapshot.refresh_cooldown_sec);
    }
    renderTriggers();
    const pop = $("#quota-popover");
    if (isPopoverOpen(pop)) {
      renderPopoverBody();
    }
  }

  function openQuota(triggerEl) {
    const pop = $("#quota-popover");
    if (!pop) return;
    pendingCloseTrigger = null;
    document.querySelectorAll?.(".menu")?.forEach((m) => { m.hidden = true; });
    const resolved = triggerEl || resolveTrigger(null);
    if (isPopoverOpen(pop) && lastTrigger === resolved) {
      hidePopoverEl(pop);
      return;
    }
    lastTrigger = resolved;
    if (typeof hostGetContext === "function") {
      try { setContext(hostGetContext(), { silent: true }); } catch {}
    }
    renderTriggers();
    renderPopoverBody();
    positionPopover(pop, resolved);
    if (!isPopoverOpen(pop)) {
      showPopoverEl(pop);
    }
    setExpandedAria(true);
    if (isSnapshotStale()) {
      fetchQuota();
    }
  }

  function wireDOM() {
    if (wired) return;
    wired = true;

    const navBtn = $("#nav-quota");
    const railBtn = $("#rail-quota");
    const pop = $("#quota-popover");
    const refreshBtn = $("#qp-refresh-btn");
    const closeBtn = $("#qp-close-btn");
    const body = $("#qp-body");

    const bindQuotaTrigger = (btn) => {
      if (!btn) return;
      btn.addEventListener("pointerdown", (e) => {
        if (e.isPrimary === false) return;
        if (e.pointerType === "mouse" && e.button !== 0) return;
        // Native popover="auto" light-dismisses before click fires; remember close intent here.
        pendingCloseTrigger = isPopoverOpen(pop) && lastTrigger === btn ? btn : null;
      });
      btn.addEventListener("keydown", (e) => {
        if (e.key === "Enter" || e.key === " ") {
          pendingCloseTrigger = null;
        }
      });
      btn.addEventListener("click", (e) => {
        e.stopPropagation();
        if (pendingCloseTrigger === btn) {
          pendingCloseTrigger = null;
          hidePopoverEl(pop);
          setExpandedAria(false);
          return;
        }
        pendingCloseTrigger = null;
        openQuota(btn);
      });
    };

    bindQuotaTrigger(navBtn);
    bindQuotaTrigger(railBtn);
    refreshBtn?.addEventListener("click", () => manualRefresh());
    closeBtn?.addEventListener("click", () => {
      hidePopoverEl(pop);
      lastTrigger?.focus?.();
    });
    body?.addEventListener("click", (e) => {
      const btn = e.target?.closest?.('[data-quota-guide="claude"]');
      if (!btn) return;
      claudeGuideOpen = !claudeGuideOpen;
      renderPopoverBody();
    });

    pop?.addEventListener("toggle", (e) => {
      const open = e.newState === "open";
      setExpandedAria(open);
      if (open) startOpenTicker();
      else {
        stopOpenTicker();
        if (pop.style) {
          pop.style.transform = "";
          pop.style.willChange = "";
        }
        pop.classList.remove("is-dragging");
      }
    });

    // Mobile bottom-sheet rAF-batched drag-to-dismiss (zero layout reads during drag).
    if (pop) {
      const zones = [pop.querySelector?.(".qp-handle"), pop.querySelector?.(".qp-head")].filter(Boolean);
      let wcTimer = 0;
      const clearWC = () => {
        if (wcTimer) { clearTimeout(wcTimer); wcTimer = 0; }
        if (pop.style) pop.style.willChange = "";
      };
      for (const zone of zones) {
        let startY = 0, startX = 0, startT = 0, lastY = 0, pid = null;
        let dragging = false, applied = false, pendingDy = 0, rafId = 0, threshold = 96;
        const cancelRaf = () => {
          if (rafId && typeof cancelAnimationFrame === "function") cancelAnimationFrame(rafId);
          rafId = 0;
        };
        const flush = () => {
          rafId = 0;
          if (!dragging) return;
          if (!applied) {
            applied = true;
            if (wcTimer) { clearTimeout(wcTimer); wcTimer = 0; }
            pop.style.willChange = "transform";
            pop.classList.add("is-dragging");
          }
          pop.style.transform = `translate3d(0, ${pendingDy}px, 0)`;
        };
        zone.addEventListener("pointerdown", (e) => {
          if (!pop.classList.contains("is-sheet") || !isPopoverOpen(pop)) return;
          if (e.pointerType === "mouse" && e.button !== 0) return;
          if (e.target?.closest?.("button, a")) return;
          cancelRaf();
          pid = e.pointerId;
          startY = lastY = e.clientY;
          startX = e.clientX;
          startT = (typeof performance !== "undefined" && performance.now) ? performance.now() : Date.now();
          dragging = false;
          applied = false;
          pendingDy = 0;
          threshold = Math.min(110, (pop.offsetHeight || 360) * 0.24);
        });
        zone.addEventListener("pointermove", (e) => {
          if (pid !== e.pointerId) return;
          const dy = e.clientY - startY, dx = e.clientX - startX;
          lastY = e.clientY;
          if (!dragging) {
            if (Math.abs(dx) > 10 && Math.abs(dx) > Math.abs(dy)) { pid = null; return; }
            if (dy <= 8) return;
            dragging = true;
            try { zone.setPointerCapture?.(e.pointerId); } catch {}
          }
          pendingDy = Math.max(0, dy);
          if (!rafId) {
            if (typeof requestAnimationFrame === "function") rafId = requestAnimationFrame(flush);
            else flush();
          }
        });
        const endDrag = (e, cancelled) => {
          if (pid !== e.pointerId) return;
          cancelRaf();
          const wasDragging = dragging;
          const hadApplied = applied;
          pid = null;
          dragging = false;
          applied = false;
          const nowT = (typeof performance !== "undefined" && performance.now) ? performance.now() : Date.now();
          const dy = Math.max(0, lastY - startY);
          const vy = dy / Math.max(1, nowT - startT);
          const shouldClose = wasDragging && !cancelled && (dy > threshold || (dy > 40 && vy > 0.45));
          pop.classList.remove("is-dragging");
          pop.style.transform = "";
          if (hadApplied) {
            const reduced = typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
            if (reduced) clearWC();
            else wcTimer = setTimeout(clearWC, 320);
          }
          if (shouldClose) hidePopoverEl(pop);
        };
        zone.addEventListener("pointerup", (e) => endDrag(e, false));
        zone.addEventListener("pointercancel", (e) => endDrag(e, true));
      }
    }

    document.addEventListener("pointerup", (e) => {
      if (pendingCloseTrigger && e.target !== pendingCloseTrigger && !pendingCloseTrigger.contains?.(e.target)) {
        pendingCloseTrigger = null;
      }
    });
    document.addEventListener("pointercancel", () => {
      pendingCloseTrigger = null;
    });
    window.addEventListener("blur", () => {
      pendingCloseTrigger = null;
    });
    document.addEventListener("keydown", (e) => {
      if (e.key === "Escape" && pop && !hasNativePopover(pop) && isPopoverOpen(pop)) {
        hidePopoverEl(pop);
        lastTrigger?.focus?.();
      }
    });
    document.addEventListener("click", (e) => {
      if (pop && !hasNativePopover(pop) && isPopoverOpen(pop) && !pop.contains(e.target) && e.target !== lastTrigger && !lastTrigger?.contains?.(e.target)) {
        hidePopoverEl(pop);
      }
    });
    window.addEventListener("resize", () => {
      if (pop && isPopoverOpen(pop)) {
        positionPopover(pop, lastTrigger);
      }
    });
  }

  function init(opts = {}) {
    if (opts.api) hostAPI = opts.api;
    if (opts.toast) hostToast = opts.toast;
    if (typeof opts.getContext === "function") {
      hostGetContext = opts.getContext;
      try { setContext(hostGetContext(), { silent: true }); } catch {}
    }
    wireDOM();
    if (!initialized) {
      initialized = true;
      renderTriggers();
    } else {
      renderTriggers();
    }
    fetchQuota();
  }

  function onLangChange() {
    renderTriggers();
    const pop = $("#quota-popover");
    if (isPopoverOpen(pop)) renderPopoverBody();
    else syncRefreshControls();
  }

  window.__collieQuota = {
    init,
    onEvent: applySnapshot,
    onLangChange,
    onContextChange,
    setContext,
    getContext: () => ({ ...activeContext }),
    open: openQuota,
    close: () => hidePopoverEl($("#quota-popover")),
    refresh: manualRefresh,
    getSnapshot: () => snapshot,
    // Pure helpers exported for contract tests
    summarizeQuota,
    selectAgentBottleneck,
    scoreModelMatch,
    normalizeModelKey,
    formatResetCountdown,
    formatUpdatedAgo,
  };
})();
