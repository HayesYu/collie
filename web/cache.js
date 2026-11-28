/* Collie — Local Instant-Paint Snapshot Cache (Stale-While-Revalidate)
   Standalone module bridged with app.js via window.__collieCache to minimize merge conflicts. */
(() => {
  "use strict";

  const SCHEMA_VERSION = 1;
  const KEY_PREFIX = "collie.snap.";
  const MAX_ENTRIES = 45;
  const MAX_PENDING = 40;
  const MAX_PINNED = 40;
  const MAX_WORKERS = 36;
  const MAX_AGENTS = 50;
  const MAX_SHELLS = 20;
  const MAX_BYTES = 200 * 1024; // 200 KB UTF-8 hard ceiling
  const QUOTA_RETRY_BYTES = 64 * 1024;
  const QUOTA_RETRY_ENTRIES = 16;
  const MAX_AGE_MS = 30 * 24 * 60 * 60 * 1000; // 30 days
  const DEBOUNCE_MS = 350;
  const SINGLE_ENTRY_SOFT_BYTES = 28 * 1024;

  // Compact synchronous SHA-256 (FIPS 180-4) for irreversible origin + token key fingerprints.
  const SHA256_K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
  ]);
  const _utf8Encoder = typeof TextEncoder !== "undefined" ? new TextEncoder() : null;

  function toUtf8Bytes(str) {
    const s = String(str ?? "");
    if (_utf8Encoder) return _utf8Encoder.encode(s);
    const out = [];
    for (let i = 0; i < s.length; i++) {
      let c = s.charCodeAt(i);
      if (c < 0x80) {
        out.push(c);
      } else if (c < 0x800) {
        out.push(0xc0 | (c >> 6), 0x80 | (c & 0x3f));
      } else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) {
        const lo = s.charCodeAt(++i);
        const cp = 0x10000 + (((c & 0x3ff) << 10) | (lo & 0x3ff));
        out.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
      } else {
        out.push(0xe0 | (c >> 12), 0x80 | ((c >> 6) & 0x3f), 0x80 | (c & 0x3f));
      }
    }
    return new Uint8Array(out);
  }

  function utf8ByteLen(str) {
    const s = String(str ?? "");
    if (!s) return 0;
    if (_utf8Encoder) return _utf8Encoder.encode(s).length;
    let n = 0;
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i);
      if (c < 0x80) n += 1;
      else if (c < 0x800) n += 2;
      else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length) { n += 4; i++; }
      else n += 3;
    }
    return n;
  }

  function sha256Hex(str) {
    const msg = toUtf8Bytes(str);
    const bitLen = msg.length * 8;
    const totalLen = (((msg.length + 9 + 63) >> 6) << 6);
    const buf = new Uint8Array(totalLen);
    buf.set(msg);
    buf[msg.length] = 0x80;
    const dv = new DataView(buf.buffer);
    dv.setUint32(totalLen - 8, Math.floor(bitLen / 0x100000000), false);
    dv.setUint32(totalLen - 4, bitLen >>> 0, false);

    let h0 = 0x6a09e667, h1 = 0xbb67ae85, h2 = 0x3c6ef372, h3 = 0xa54ff53a;
    let h4 = 0x510e527f, h5 = 0x9b05688c, h6 = 0x1f83d9ab, h7 = 0x5be0cd19;
    const w = new Uint32Array(64);

    for (let off = 0; off < totalLen; off += 64) {
      for (let i = 0; i < 16; i++) w[i] = dv.getUint32(off + i * 4, false);
      for (let i = 16; i < 64; i++) {
        const s0 = ((w[i - 15] >>> 7) | (w[i - 15] << 25)) ^ ((w[i - 15] >>> 18) | (w[i - 15] << 14)) ^ (w[i - 15] >>> 3);
        const s1 = ((w[i - 2] >>> 17) | (w[i - 2] << 15)) ^ ((w[i - 2] >>> 19) | (w[i - 2] << 13)) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
      }
      let a = h0, b = h1, c = h2, d = h3, e = h4, f = h5, g = h6, h = h7;
      for (let i = 0; i < 64; i++) {
        const S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        const ch = (e & f) ^ (~e & g);
        const t1 = (h + S1 + ch + SHA256_K[i] + w[i]) >>> 0;
        const S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        const maj = (a & b) ^ (a & c) ^ (b & c);
        const t2 = (S0 + maj) >>> 0;
        h = g; g = f; f = e; e = (d + t1) >>> 0;
        d = c; c = b; b = a; a = (t1 + t2) >>> 0;
      }
      h0 = (h0 + a) >>> 0; h1 = (h1 + b) >>> 0; h2 = (h2 + c) >>> 0; h3 = (h3 + d) >>> 0;
      h4 = (h4 + e) >>> 0; h5 = (h5 + f) >>> 0; h6 = (h6 + g) >>> 0; h7 = (h7 + h) >>> 0;
    }
    return [h0, h1, h2, h3, h4, h5, h6, h7].map((v) => v.toString(16).padStart(8, "0")).join("");
  }

  function resolveOrigin(override) {
    if (typeof override === "string" && override) return override;
    try {
      if (typeof location !== "undefined" && location.origin && location.origin !== "null") {
        return String(location.origin);
      }
      if (typeof location !== "undefined" && location.protocol && location.host) {
        return `${location.protocol}//${location.host}`;
      }
    } catch {}
    return "null";
  }

  function resolveStorage(override) {
    if (override) return override;
    try {
      return typeof localStorage !== "undefined" ? localStorage : null;
    } catch {
      return null;
    }
  }

  function originFingerprint(origin) {
    return sha256Hex("collie-snap-origin:" + resolveOrigin(origin)).slice(0, 12);
  }

  function tokenFingerprint(token) {
    const tok = String(token || "").trim();
    if (!tok) return "anon";
    return "t" + sha256Hex("collie-snap-tok:" + tok).slice(0, 12);
  }

  function buildKey(origin, token, schemaVersion = SCHEMA_VERSION) {
    return `${KEY_PREFIX}v${schemaVersion}.${originFingerprint(origin)}.${tokenFingerprint(token)}`;
  }

  function clipStringUtf8(str, maxBytes) {
    const s = String(str ?? "");
    if (maxBytes <= 0) return "";
    if (utf8ByteLen(s) <= maxBytes) return s;
    const suffix = "…";
    const budget = Math.max(0, maxBytes - utf8ByteLen(suffix));
    let lo = 0, hi = s.length;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (utf8ByteLen(s.slice(0, mid)) <= budget) lo = mid;
      else hi = mid - 1;
    }
    let cut = lo;
    if (cut > 0) {
      const c = s.charCodeAt(cut - 1);
      if (c >= 0xd800 && c <= 0xdbff) cut--;
    }
    return s.slice(0, cut).trimEnd() + suffix;
  }

  function clipChars(str, maxChars) {
    const s = String(str ?? "");
    if (!s || s.length <= maxChars) return s;
    return s.slice(0, Math.max(0, maxChars - 1)).trimEnd() + "…";
  }

  function compactEntry(e) {
    if (!e || typeof e !== "object" || !e.id) return null;
    const out = {
      id: String(e.id),
      role: String(e.role || ""),
      at: e.at || "",
      text: typeof e.text === "string" ? e.text : "",
    };
    if (e.thread) out.thread = String(e.thread);
    if (e.tone) out.tone = String(e.tone);
    if (e.ref) out.ref = String(e.ref);
    if (e.source) out.source = String(e.source);
    if (e.from) out.from = String(e.from);
    if (e.in_place) out.in_place = true;
    if (e.check) out.check = true;
    if (e.client_msg_id) out.client_msg_id = String(e.client_msg_id);
    if (typeof e.claps === "number" && e.claps > 0) out.claps = e.claps;
    if (e.re && typeof e.re === "object") {
      const re = {};
      if (e.re.entry) re.entry = String(e.re.entry);
      if (e.re.worker) re.worker = String(e.re.worker);
      if (e.re.who) re.who = String(e.re.who);
      if (e.re.text) re.text = clipChars(e.re.text, 240);
      if (Object.keys(re).length) out.re = re;
    }
    if (Array.isArray(e.options) && e.options.length) {
      out.options = e.options.slice(0, 12).map((o) => String(o ?? ""));
    }
    if (e.pick && typeof e.pick === "object") {
      const pick = {};
      if (e.pick.text) pick.text = String(e.pick.text);
      if (e.pick.at) pick.at = e.pick.at;
      if (e.pick.in_place) pick.in_place = true;
      if (Object.keys(pick).length) out.pick = pick;
    }
    if (Array.isArray(e.files) && e.files.length) {
      out.files = e.files.slice(0, 16).map((f) => String(f ?? ""));
    }
    if (utf8ByteLen(out.text) > SINGLE_ENTRY_SOFT_BYTES) {
      out.text = clipStringUtf8(out.text, SINGLE_ENTRY_SOFT_BYTES);
    }
    return out;
  }

  function compactWorker(w) {
    if (!w || typeof w !== "object" || !w.name) return null;
    const out = {
      name: String(w.name),
      status: String(w.status || ""),
    };
    if (w.label) out.label = String(w.label);
    if (w.kind) out.kind = String(w.kind);
    if (w.pane_id) out.pane_id = String(w.pane_id);
    if (w.workspace_id) out.workspace_id = String(w.workspace_id);
    if (w.machine) out.machine = String(w.machine);
    if (w.cwd) out.cwd = String(w.cwd);
    if (w.thread) out.thread = String(w.thread);
    if (w.created_by) out.created_by = String(w.created_by);
    if (w.created_at) out.created_at = w.created_at;
    if (w.ended) out.ended = true;
    if (w.task) out.task = clipChars(w.task, 280);
    if (w.last_report) out.last_report = clipChars(w.last_report, 200);
    if (w.report_at) out.report_at = w.report_at;
    if (Array.isArray(w.run) && w.run.length) out.run = w.run.slice(0, 2).map((x) => String(x ?? ""));
    return out;
  }

  function compactAgent(a) {
    if (!a || typeof a !== "object" || !a.pane_id) return null;
    const out = {
      pane_id: String(a.pane_id),
      name: String(a.name || ""),
      kind: String(a.kind || ""),
      status: String(a.status || ""),
      is_agent: !!a.is_agent,
      machine: String(a.machine || ""),
    };
    if (a.workspace_id) out.workspace_id = String(a.workspace_id);
    if (a.workspace_label) out.workspace_label = String(a.workspace_label);
    if (a.label) out.label = String(a.label);
    if (a.cwd) out.cwd = String(a.cwd);
    if (a.title) out.title = clipChars(a.title, 120);
    if (a.topic) out.topic = clipChars(a.topic, 120);
    if (a.created_at) out.created_at = a.created_at;
    if (Array.isArray(a.run) && a.run.length) out.run = a.run.slice(0, 2).map((x) => String(x ?? ""));
    return out;
  }

  function compactPayload(sup, state, opts = {}) {
    const maxEntries = Math.max(1, Number(opts.maxEntries) || MAX_ENTRIES);
    const rawSup = sup && typeof sup === "object" ? sup : {};
    const allEntries = Array.isArray(rawSup.entries) ? rawSup.entries : [];
    const persisted = allEntries.filter((e) => e && e.id && !e.pending);
    const dropped = persisted.length > maxEntries;
    const sliced = dropped ? persisted.slice(persisted.length - maxEntries) : persisted;
    const entries = [];
    for (let i = 0; i < sliced.length; i++) {
      const c = compactEntry(sliced[i]);
      if (c) entries.push(c);
    }

    const r = rawSup.router && typeof rawSup.router === "object" ? rawSup.router : {};
    const router = {
      alive: !!r.alive,
      status: String(r.status || "asleep"),
      kind: String(r.kind || ""),
    };
    if (r.pane_id) router.pane_id = String(r.pane_id);
    if (typeof r.context === "number" && r.context > 0) router.context = r.context;
    if (typeof r.rotate_at === "number") router.rotate_at = r.rotate_at;
    if (r.handover) router.handover = true;
    if (r.dreaming) router.dreaming = true;
    if (r.replied) router.replied = true;
    if (r.turn_kind) router.turn_kind = String(r.turn_kind);
    if (r.model) router.model = String(r.model);
    if (r.effort) router.effort = String(r.effort);
    if (r.pending) router.pending = true;

    const pending = Array.isArray(rawSup.pending)
      ? rawSup.pending.slice(-MAX_PENDING).filter((p) => p && p.id).map((p) => {
          const item = { id: String(p.id), at: p.at || "", summary: clipChars(p.summary || "", 160) };
          if (Array.isArray(p.options) && p.options.length) item.options = p.options.slice(0, 8).map((o) => String(o ?? ""));
          if (p.check) item.check = true;
          return item;
        })
      : [];

    const pinned = Array.isArray(rawSup.pinned)
      ? rawSup.pinned.slice(-MAX_PINNED).filter((p) => p && p.id).map((p) => ({
          id: String(p.id),
          at: p.at || "",
          role: String(p.role || ""),
          summary: clipChars(p.summary || "", 160),
        }))
      : [];

    const rawWorkers = Array.isArray(rawSup.workers) ? rawSup.workers : [];
    const activeWorkers = [];
    const endedWorkers = [];
    for (let i = 0; i < rawWorkers.length; i++) {
      const w = rawWorkers[i];
      if (!w || !w.name) continue;
      if (w.ended) endedWorkers.push(w);
      else activeWorkers.push(w);
    }
    const keptWorkers = [...activeWorkers, ...endedWorkers.slice(-Math.max(0, MAX_WORKERS - activeWorkers.length))]
      .slice(0, MAX_WORKERS)
      .map(compactWorker)
      .filter(Boolean);

    const compactSup = {
      router,
      entries,
      earlier: !!(rawSup.earlier || dropped),
      pending,
      pinned,
      workers: keptWorkers,
      queued: Number(rawSup.queued) || 0,
      queued_router: Number(rawSup.queued_router) || 0,
      paused: !!rawSup.paused,
    };
    if (rawSup.last_read_entry_id) compactSup.last_read_entry_id = String(rawSup.last_read_entry_id);
    if (typeof rawSup.timers_count === "number" && rawSup.timers_count > 0) compactSup.timers_count = rawSup.timers_count;
    if (rawSup.follow && rawSup.follow.next) {
      compactSup.follow = { next: rawSup.follow.next, next_text: clipChars(rawSup.follow.next_text || "", 120) };
    }
    if (rawSup.integrations_summary && typeof rawSup.integrations_summary === "object") {
      compactSup.integrations_summary = {
        connected: Number(rawSup.integrations_summary.connected) || 0,
        error: Number(rawSup.integrations_summary.error) || 0,
      };
    }
    if (rawSup.peers_summary && typeof rawSup.peers_summary === "object") {
      compactSup.peers_summary = {
        enabled: rawSup.peers_summary.enabled !== false,
        active: Number(rawSup.peers_summary.active) || 0,
        pending_incoming: Number(rawSup.peers_summary.pending_incoming) || 0,
      };
    }

    let compactState = null;
    if (state && typeof state === "object") {
      const machines = Array.isArray(state.machines)
        ? state.machines.slice(0, 16).filter((m) => m && !m.hub).map((m) => {
            const cm = { id: String(m.id || ""), name: String(m.name || ""), local: !!m.local, online: !!m.online };
            if (m.os) cm.os = String(m.os);
            if (m.version) cm.version = String(m.version);
            if (Array.isArray(m.agents) && m.agents.length) cm.agents = m.agents.slice(0, 12).map(String);
            return cm;
          })
        : [];
      const agents = Array.isArray(state.agents)
        ? state.agents.filter((a) => a && !a.hub).slice(0, MAX_AGENTS).map(compactAgent).filter(Boolean)
        : [];
      const shells = Array.isArray(state.shells)
        ? state.shells.filter((s) => s && !s.hub).slice(0, MAX_SHELLS).map(compactAgent).filter(Boolean)
        : [];
      compactState = { ok: !!state.ok, agents, shells, machines };
      if (state.last_read_entry_id) compactState.last_read_entry_id = String(state.last_read_entry_id);
    }

    return { sup: compactSup, state: compactState };
  }

  function serializeBounded(envelope, opts = {}) {
    const maxBytes = Math.max(2048, Number(opts.maxBytes) || MAX_BYTES);
    let trimmedEntries = 0;
    let truncatedText = 0;

    const tryJson = () => {
      const json = JSON.stringify(envelope);
      return { json, bytes: utf8ByteLen(json) };
    };

    let cur = tryJson();
    if (cur.bytes <= maxBytes) {
      return { ...cur, trimmedEntries, truncatedText };
    }

    const entries = envelope.sup?.entries;
    if (Array.isArray(entries) && entries.length > 1) {
      // Binary search for the largest suffix of entries that fits within maxBytes.
      const total = entries.length;
      let lo = 1, hi = total - 1, bestKeep = 0;
      while (lo <= hi) {
        const mid = (lo + hi) >> 1;
        envelope.sup.entries = entries.slice(total - mid);
        envelope.sup.earlier = true;
        const probe = tryJson();
        if (probe.bytes <= maxBytes) {
          bestKeep = mid;
          lo = mid + 1;
        } else {
          hi = mid - 1;
        }
      }
      if (bestKeep > 0) {
        trimmedEntries = total - bestKeep;
        envelope.sup.entries = entries.slice(total - bestKeep);
        envelope.sup.earlier = true;
        cur = tryJson();
        return { ...cur, trimmedEntries, truncatedText };
      }
      trimmedEntries = total - 1;
      envelope.sup.entries = entries.slice(total - 1);
      envelope.sup.earlier = true;
      cur = tryJson();
    }

    if (cur.bytes > maxBytes && envelope.sup) {
      // Compact auxiliary sidebar/worker fields before shrinking the last entry text.
      if (Array.isArray(envelope.sup.workers)) {
        envelope.sup.workers = envelope.sup.workers
          .filter((w) => w && !w.ended)
          .slice(0, 12)
          .map((w) => ({ name: w.name, label: w.label, kind: w.kind, status: w.status, pane_id: w.pane_id }));
      }
      if (envelope.state) {
        envelope.state.agents = (envelope.state.agents || []).slice(0, 16);
        envelope.state.shells = [];
      }
      cur = tryJson();
    }

    if (cur.bytes > maxBytes && Array.isArray(envelope.sup?.entries) && envelope.sup.entries.length) {
      const last = envelope.sup.entries[envelope.sup.entries.length - 1];
      if (last && typeof last.text === "string" && last.text.length > 0) {
        const overhead = cur.bytes - utf8ByteLen(last.text);
        const allow = Math.max(0, maxBytes - overhead - 64);
        last.text = clipStringUtf8(last.text, allow);
        truncatedText++;
        cur = tryJson();
      }
    }

    if (cur.bytes > maxBytes && envelope.sup) {
      envelope.sup.entries = [];
      envelope.sup.pending = (envelope.sup.pending || []).slice(-10);
      envelope.sup.pinned = (envelope.sup.pinned || []).slice(-10);
      envelope.state = null;
      cur = tryJson();
    }

    if (cur.bytes > maxBytes) return null;
    return { ...cur, trimmedEntries, truncatedText };
  }

  function listSnapKeys(storage, originFilter = null) {
    const keys = [];
    if (!storage) return keys;
    try {
      const len = typeof storage.length === "number" ? storage.length : 0;
      if (typeof storage.key !== "function" || len <= 0) return keys;
      const wantOriginFp = originFilter ? originFingerprint(originFilter) : "";
      for (let i = 0; i < len; i++) {
        const k = storage.key(i);
        if (!k || !k.startsWith(KEY_PREFIX)) continue;
        if (!wantOriginFp) {
          keys.push(k);
          continue;
        }
        // Key layout: collie.snap.v<N>.<originFp>.<authFp>
        const rest = k.slice(KEY_PREFIX.length);
        const parts = rest.split(".");
        if (parts.length >= 3 && parts[1] === wantOriginFp) {
          keys.push(k);
          continue;
        }
        // Unknown/legacy key format under collie.snap.* -> inspect envelope origin if parseable.
        try {
          const raw = storage.getItem(k);
          const env = raw ? JSON.parse(raw) : null;
          if (!env || !env.origin || env.origin === resolveOrigin(originFilter)) keys.push(k);
        } catch {
          keys.push(k);
        }
      }
    } catch {}
    return keys;
  }

  function safeRemove(storage, key) {
    if (!storage || !key || typeof storage.removeItem !== "function") return false;
    try {
      storage.removeItem(key);
      return true;
    } catch {
      return false;
    }
  }

  function isQuotaError(err) {
    if (!err) return false;
    if (err.name === "QuotaExceededError" || err.name === "NS_ERROR_DOM_QUOTA_REACHED") return true;
    if (err.code === 22 || err.code === 1014) return true;
    return /quota/i.test(String(err.message || ""));
  }

  let debounceTimer = 0;
  let pendingGetter = null;
  let pendingOpts = null;

  function cancelPending() {
    if (debounceTimer) {
      clearTimeout(debounceTimer);
      debounceTimer = 0;
    }
    pendingGetter = null;
    pendingOpts = null;
  }

  function clear(opts = {}) {
    cancelPending();
    const storage = resolveStorage(opts.storage);
    if (!storage) return 0;
    const originFilter = opts.all ? null : (typeof opts.origin === "string" ? opts.origin : null);
    const keys = listSnapKeys(storage, originFilter);
    const seen = new Set();
    let removed = 0;
    for (const k of keys) {
      if (seen.has(k)) continue;
      seen.add(k);
      if (safeRemove(storage, k)) removed++;
    }
    // Fallback for minimal storage mocks without length/key(): remove exact active key.
    if (typeof opts.token !== "undefined" || typeof opts.origin === "string" || !keys.length) {
      const exact = buildKey(opts.origin, opts.token);
      if (!seen.has(exact) && safeRemove(storage, exact)) removed++;
      const anon = buildKey(opts.origin, "");
      if (!seen.has(anon) && safeRemove(storage, anon)) removed++;
    }
    return removed;
  }

  function read(opts = {}) {
    if (!opts.allowDemo && typeof window !== "undefined" && window.__collieDemo) return null;
    const storage = resolveStorage(opts.storage);
    if (!storage || typeof storage.getItem !== "function") return null;
    const origin = resolveOrigin(opts.origin);
    const authFp = tokenFingerprint(opts.token);
    const key = buildKey(origin, opts.token);
    let raw = null;
    try {
      raw = storage.getItem(key);
    } catch {
      return null;
    }
    if (!raw || typeof raw !== "string") return null;
    let env = null;
    try {
      env = JSON.parse(raw);
    } catch {
      safeRemove(storage, key);
      return null;
    }
    const maxAgeMs = typeof opts.maxAgeMs === "number" ? opts.maxAgeMs : MAX_AGE_MS;
    const now = typeof opts.now === "number" ? opts.now : Date.now();
    const valid = !!(
      env && typeof env === "object"
      && env.v === SCHEMA_VERSION
      && env.origin === origin
      && env.authFp === authFp
      && typeof env.savedAt === "number" && Number.isFinite(env.savedAt)
      && (maxAgeMs <= 0 || now - env.savedAt <= maxAgeMs)
      && env.sup && typeof env.sup === "object"
      && Array.isArray(env.sup.entries)
    );
    if (!valid) {
      safeRemove(storage, key);
      return null;
    }
    return {
      v: env.v,
      origin: env.origin,
      authFp: env.authFp,
      savedAt: env.savedAt,
      key,
      sup: env.sup,
      state: env.state && typeof env.state === "object" ? env.state : null,
    };
  }

  function pruneOldVersions(storage, origin, keepKey) {
    const wantOriginFp = originFingerprint(origin);
    const curVerTag = `v${SCHEMA_VERSION}`;
    const keys = listSnapKeys(storage, origin);
    for (const k of keys) {
      if (k === keepKey) continue;
      const parts = k.slice(KEY_PREFIX.length).split(".");
      if (parts.length < 3 || parts[1] !== wantOriginFp || parts[0] !== curVerTag) {
        safeRemove(storage, k);
      }
    }
  }

  function writeNow(payload, opts = {}) {
    if (!opts.allowDemo && typeof window !== "undefined" && window.__collieDemo) {
      return { ok: false, skipped: "demo" };
    }
    const storage = resolveStorage(opts.storage);
    if (!storage || typeof storage.setItem !== "function") {
      return { ok: false, skipped: "no-storage" };
    }
    const origin = resolveOrigin(opts.origin);
    const authFp = tokenFingerprint(opts.token);
    const key = buildKey(origin, opts.token);
    const now = typeof opts.now === "number" ? opts.now : Date.now();
    const maxBytes = Math.max(2048, Number(opts.maxBytes) || MAX_BYTES);
    const maxEntries = Math.max(1, Number(opts.maxEntries) || MAX_ENTRIES);

    const compacted = compactPayload(payload?.sup, payload?.state, { maxEntries });
    const envelope = {
      v: SCHEMA_VERSION,
      origin,
      authFp,
      savedAt: now,
      sup: compacted.sup,
      state: compacted.state,
    };
    const packed = serializeBounded(envelope, { maxBytes });
    if (!packed) return { ok: false, error: "oversized" };

    pruneOldVersions(storage, origin, key);

    try {
      storage.setItem(key, packed.json);
      return {
        ok: true,
        key,
        bytes: packed.bytes,
        entries: envelope.sup.entries.length,
        trimmedEntries: packed.trimmedEntries,
        truncatedText: packed.truncatedText,
      };
    } catch (err) {
      if (!isQuotaError(err)) {
        return { ok: false, key, error: String(err?.name || err?.message || "storage-error") };
      }
      // QuotaExceededError recovery: purge all snapshot keys for this origin and retry with compact subset.
      for (const k of listSnapKeys(storage, origin)) safeRemove(storage, k);
      const retryCompacted = compactPayload(payload?.sup, payload?.state, {
        maxEntries: Math.min(maxEntries, QUOTA_RETRY_ENTRIES),
      });
      const retryEnv = {
        v: SCHEMA_VERSION,
        origin,
        authFp,
        savedAt: now,
        sup: retryCompacted.sup,
        state: retryCompacted.state,
      };
      const retryPacked = serializeBounded(retryEnv, {
        maxBytes: Math.min(maxBytes, QUOTA_RETRY_BYTES),
      });
      if (!retryPacked) return { ok: false, key, quotaExceeded: true };
      try {
        storage.setItem(key, retryPacked.json);
        return {
          ok: true,
          key,
          bytes: retryPacked.bytes,
          entries: retryEnv.sup.entries.length,
          trimmedEntries: retryPacked.trimmedEntries,
          truncatedText: retryPacked.truncatedText,
          quotaFallback: true,
        };
      } catch (err2) {
        return { ok: false, key, quotaExceeded: true, error: String(err2?.name || err2?.message || "quota") };
      }
    }
  }

  function flush() {
    if (!pendingGetter) {
      cancelPending();
      return null;
    }
    const getter = pendingGetter;
    const opts = pendingOpts || {};
    cancelPending();
    try {
      const payload = typeof getter === "function" ? getter() : getter;
      if (!payload) return null;
      return writeNow(payload, opts);
    } catch {
      return null;
    }
  }

  function scheduleWrite(getPayload, opts = {}) {
    if (!opts.allowDemo && typeof window !== "undefined" && window.__collieDemo) return;
    pendingGetter = getPayload;
    pendingOpts = opts;
    if (debounceTimer) clearTimeout(debounceTimer);
    const delay = typeof opts.debounceMs === "number" ? Math.max(0, opts.debounceMs) : DEBOUNCE_MS;
    debounceTimer = setTimeout(() => {
      debounceTimer = 0;
      flush();
    }, delay);
  }

  const api = {
    SCHEMA_VERSION,
    KEY_PREFIX,
    MAX_ENTRIES,
    MAX_BYTES,
    DEBOUNCE_MS,
    sha256Hex,
    utf8ByteLen,
    originFingerprint,
    tokenFingerprint,
    buildKey,
    compactPayload,
    serializeBounded,
    read,
    writeNow,
    scheduleWrite,
    flush,
    cancelPending,
    clear,
  };

  if (typeof window !== "undefined") {
    window.__collieCache = api;
  } else if (typeof globalThis !== "undefined") {
    globalThis.__collieCache = api;
  }
})();
