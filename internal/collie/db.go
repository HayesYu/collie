package collie

import (
	"database/sql"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLite (WAL) is the source of truth for the super-agent: the trigger inbox,
// threads, workers, conversation, timers and settings (in ~/.collie/herd.db). The process keeps a
// recent working set in memory and upserts it back; history stays in the DB.

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
CREATE TABLE IF NOT EXISTS kv       (k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS triggers (id TEXT PRIMARY KEY, n INTEGER NOT NULL, at INTEGER NOT NULL,
                                     status TEXT NOT NULL, source TEXT NOT NULL, thread TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS triggers_status ON triggers(status);
CREATE INDEX IF NOT EXISTS triggers_thread ON triggers(thread, n);
CREATE TABLE IF NOT EXISTS threads  (id TEXT PRIMARY KEY, last_at INTEGER NOT NULL, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS workers  (name TEXT PRIMARY KEY, created_at INTEGER NOT NULL, label TEXT NOT NULL DEFAULT '', data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS entries  (id TEXT PRIMARY KEY, n INTEGER NOT NULL, at INTEGER NOT NULL, thread TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS entries_thread ON entries(thread, n);
CREATE TABLE IF NOT EXISTS timers   (id TEXT PRIMARY KEY, data TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS integrations (
    kind       TEXT PRIMARY KEY,
    enabled    INTEGER NOT NULL DEFAULT 0,
    config     TEXT NOT NULL DEFAULT '{}',
    cursor     TEXT NOT NULL DEFAULT '{}',
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS integration_dedup (
    source     TEXT NOT NULL,
    dedup_key  TEXT NOT NULL,
    seen_at    INTEGER NOT NULL,
    PRIMARY KEY (source, dedup_key)
);
CREATE INDEX IF NOT EXISTS integration_dedup_seen ON integration_dedup(seen_at);
CREATE TABLE IF NOT EXISTS integration_messages (
    entry_id    TEXT NOT NULL,
    kind        TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    external_id TEXT NOT NULL,
    resolved    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (entry_id, kind)
);
CREATE INDEX IF NOT EXISTS integration_messages_ext ON integration_messages(kind, external_id);
CREATE TABLE IF NOT EXISTS peers (
    id              TEXT PRIMARY KEY,
    hub_id          TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    addrs           TEXT NOT NULL DEFAULT '[]',
    mode            TEXT NOT NULL DEFAULT 'advise',
    status          TEXT NOT NULL DEFAULT 'active',
    inbound_hash    TEXT NOT NULL DEFAULT '',
    protocol_ver    INTEGER NOT NULL DEFAULT 1,
    last_ok_at      INTEGER NOT NULL DEFAULT 0,
    last_active_at  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS peers_inbound_hash ON peers(inbound_hash) WHERE inbound_hash != '';
CREATE TABLE IF NOT EXISTS peer_messages (
    msg_id          TEXT PRIMARY KEY,
    peer_id         TEXT NOT NULL,
    topic_id        TEXT NOT NULL,
    thread          TEXT NOT NULL,
    direction       TEXT NOT NULL,
    kind            TEXT NOT NULL,
    subject         TEXT NOT NULL DEFAULT '',
    in_reply_to     TEXT NOT NULL DEFAULT '',
    trigger_id      TEXT NOT NULL DEFAULT '',
    entry_id        TEXT NOT NULL DEFAULT '',
    status          TEXT NOT NULL,
    hop             INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS peer_messages_thread ON peer_messages(thread, created_at);
CREATE INDEX IF NOT EXISTS peer_messages_peer   ON peer_messages(peer_id, created_at DESC);
CREATE TABLE IF NOT EXISTS peer_blocklist (
    hub_id          TEXT PRIMARY KEY,
    name            TEXT NOT NULL DEFAULT '',
    blocked_at      INTEGER NOT NULL,
    token_hash      TEXT NOT NULL DEFAULT ''
);
`

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := migrateDB(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrateDB applies lossless, idempotent schema upgrades for existing databases.
func migrateDB(db *sql.DB) error {
	hasLabel, err := tableHasColumn(db, "workers", "label")
	if err != nil {
		return err
	}
	if !hasLabel {
		if _, err := db.Exec(`ALTER TABLE workers ADD COLUMN label TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	hasClaps, err := tableHasColumn(db, "entries", "claps")
	if err != nil {
		return err
	}
	if !hasClaps {
		if _, err := db.Exec(`ALTER TABLE entries ADD COLUMN claps INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS entries_claps ON entries(claps) WHERE claps > 0`); err != nil {
		return err
	}
	return nil
}

func tableHasColumn(db *sql.DB, table, col string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

func loadWorkers(db *sql.DB, into func(*Worker)) error {
	rows, err := db.Query(`SELECT label, data FROM workers`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var colLabel, data string
		if err := rows.Scan(&colLabel, &data); err != nil {
			return err
		}
		w := new(Worker)
		if json.Unmarshal([]byte(data), w) == nil {
			if w.Label == "" && colLabel != "" {
				w.Label = cleanWorkerLabel(colLabel)
			} else if w.Label != "" {
				w.Label = cleanWorkerLabel(w.Label)
			}
			into(w)
		}
	}
	return rows.Err()
}

func clampClaps(n int) int {
	if n < 0 {
		return 0
	}
	if n > maxEntryClaps {
		return maxEntryClaps
	}
	return n
}

func loadEntryRows(db *sql.DB, q string, into func(*Entry), args ...any) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		var colClaps int
		if err := rows.Scan(&data, &colClaps); err != nil {
			return err
		}
		v := new(Entry)
		if json.Unmarshal([]byte(data), v) == nil {
			if colClaps > 0 && v.Claps == 0 {
				v.Claps = colClaps
			}
			v.Claps = clampClaps(v.Claps)
			into(v)
		}
	}
	return rows.Err()
}

func upsertEntryClaps(db *sql.DB, e *Entry) error {
	if db == nil || e == nil || e.ID == "" {
		return nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO entries(id,n,at,thread,data,claps) VALUES(?,?,?,?,?,?)
		 ON CONFLICT(id) DO UPDATE SET data=excluded.data, claps=excluded.claps`,
		e.ID, idNum(e.ID), e.At.Unix(), e.Thread, string(b), clampClaps(e.Claps),
	)
	return err
}
func idNum(id string) int64 {
	n, _ := strconv.ParseInt(strings.TrimLeft(id, "abcdefghijklmnopqrstuvwxyz"), 10, 64)
	return n
}

func loadRows[T any](db *sql.DB, q string, into func(*T), args ...any) error {
	rows, err := db.Query(q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return err
		}
		v := new(T)
		if json.Unmarshal([]byte(data), v) == nil {
			into(v)
		}
	}
	return rows.Err()
}

func loadState(db *sql.DB, home string) (*State, error) {
	st, _, err := loadStateWithMeta(db, home)
	return st, err
}

// initState loads durable state from db and, only on first initialization when
// no persisted "settings" row exists yet, auto-detects default agent settings
// and persists them without ever overwriting existing settings.
func initState(db *sql.DB, home string, detect func(string) Settings) (*State, error) {
	st, hasSettings, err := loadStateWithMeta(db, home)
	if err != nil {
		return nil, err
	}
	if !hasSettings {
		if detect == nil {
			detect = detectDefaultSettings
		}
		st.Settings = detect(home)
		b, err := json.Marshal(st.Settings)
		if err != nil {
			return nil, err
		}
		if _, err := db.Exec(`INSERT INTO kv(k,v) VALUES('settings',?) ON CONFLICT(k) DO NOTHING`, string(b)); err != nil {
			return nil, err
		}
	}
	return st, nil
}

func loadStateWithMeta(db *sql.DB, home string) (*State, bool, error) {
	st := defaultState(home)
	kv := map[string]string{}
	rows, err := db.Query(`SELECT k, v FROM kv`)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var k, v string
		rows.Scan(&k, &v)
		kv[k] = v
	}
	rows.Close()
	if v, ok := kv["seq"]; ok {
		st.Seq, _ = strconv.Atoi(v)
	}
	v, hasSettings := kv["settings"]
	if hasSettings {
		json.Unmarshal([]byte(v), &st.Settings)
		st.Settings.migrateReply()
	}
	if v, ok := kv["policies"]; ok {
		json.Unmarshal([]byte(v), &st.Policies)
	}
	if v, ok := kv["dismissed"]; ok {
		var ids []string
		if json.Unmarshal([]byte(v), &ids) == nil {
			for _, id := range ids {
				if id != "" {
					st.Dismissed[id] = true
				}
			}
		}
	}
	if v, ok := kv["pinned"]; ok {
		var ids []string
		if json.Unmarshal([]byte(v), &ids) == nil {
			seenPin := map[string]bool{}
			for _, id := range ids {
				id = strings.TrimSpace(id)
				if id != "" && !seenPin[id] {
					seenPin[id] = true
					st.Pinned = append(st.Pinned, id)
				}
			}
			if n := len(st.Pinned); n > maxPinned {
				st.Pinned = append([]string(nil), st.Pinned[n-maxPinned:]...)
			}
		}
	}
	if v, ok := kv["last_read_entry_id"]; ok {
		v = strings.TrimSpace(v)
		if isValidEntryID(v) {
			st.LastReadEntryID = v
		}
	}

	var trig []*Trigger
	err = loadRows(db, `SELECT data FROM (SELECT data, n FROM triggers ORDER BY n DESC LIMIT 600) ORDER BY n`, func(t *Trigger) { trig = append(trig, t) })
	if err != nil {
		return nil, false, err
	}
	// Anything still queued but older than the working set is pulled in too.
	seen := map[string]bool{}
	for _, t := range trig {
		seen[t.ID] = true
	}
	loadRows(db, `SELECT data FROM triggers WHERE status='queued' ORDER BY n`, func(t *Trigger) {
		if !seen[t.ID] {
			trig = append([]*Trigger{t}, trig...)
		}
	})
	st.Triggers = trig
	loadEntryRows(db, `SELECT data, claps FROM (SELECT data, claps, n FROM entries ORDER BY n DESC LIMIT 600) ORDER BY n`, func(e *Entry) { st.Entries = append(st.Entries, e) })
	loadRows(db, `SELECT data FROM threads ORDER BY last_at DESC LIMIT 500`, func(t *Thread) { st.Threads[t.ID] = t })
	if err := loadWorkers(db, func(w *Worker) { st.Workers[w.Name] = w }); err != nil {
		return nil, false, err
	}
	loadRows(db, `SELECT data FROM timers`, func(t *Timer) { st.Timers = append(st.Timers, t) })
	return st, hasSettings, nil
}

// flushCache remembers the last JSON payload persisted to SQLite for each row
// in a Super's working set so frequent flushes only upsert rows that changed.
type flushCache struct {
	kv       map[string]string
	triggers map[string]string
	threads  map[string]string
	workers  map[string]string
	entries  map[string]string
	timers   map[string]string
}

func newFlushCache() *flushCache {
	return &flushCache{
		kv:       map[string]string{},
		triggers: map[string]string{},
		threads:  map[string]string{},
		workers:  map[string]string{},
		entries:  map[string]string{},
		timers:   map[string]string{},
	}
}

func (c *flushCache) prune(st *State) {
	if c == nil || st == nil {
		return
	}
	if len(c.triggers) > len(st.Triggers) {
		keep := make(map[string]string, len(st.Triggers))
		for _, t := range st.Triggers {
			if raw, ok := c.triggers[t.ID]; ok {
				keep[t.ID] = raw
			}
		}
		c.triggers = keep
	}
	if len(c.threads) > len(st.Threads) {
		keep := make(map[string]string, len(st.Threads))
		for id := range st.Threads {
			if raw, ok := c.threads[id]; ok {
				keep[id] = raw
			}
		}
		c.threads = keep
	}
	if len(c.workers) > len(st.Workers) {
		keep := make(map[string]string, len(st.Workers))
		for name := range st.Workers {
			if raw, ok := c.workers[name]; ok {
				keep[name] = raw
			}
		}
		c.workers = keep
	}
	if len(c.entries) > len(st.Entries) {
		keep := make(map[string]string, len(st.Entries))
		for _, e := range st.Entries {
			if raw, ok := c.entries[e.ID]; ok {
				keep[e.ID] = raw
			}
		}
		c.entries = keep
	}
	if len(c.timers) > len(st.Timers) {
		keep := make(map[string]string, len(st.Timers))
		for _, t := range st.Timers {
			if raw, ok := c.timers[t.ID]; ok {
				keep[t.ID] = raw
			}
		}
		c.timers = keep
	}
}

// saveState upserts the in-memory working set in one transaction.
func saveState(db *sql.DB, st *State, deletedTimers []string) error {
	return saveStateCached(db, st, deletedTimers, nil)
}

func saveStateCached(db *sql.DB, st *State, deletedTimers []string, cache *flushCache) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	js := func(v any) string { b, _ := json.Marshal(v); return string(b) }

	var nextKV, nextTriggers, nextThreads, nextWorkers, nextEntries, nextTimers map[string]string
	if cache != nil {
		nextKV = map[string]string{}
		nextTriggers = map[string]string{}
		nextThreads = map[string]string{}
		nextWorkers = map[string]string{}
		nextEntries = map[string]string{}
		nextTimers = map[string]string{}
	}

	var kvStmt, trigStmt, threadStmt, workerStmt, entryStmt, timerStmt, delTimerStmt *sql.Stmt
	closeStmts := func() {
		for _, s := range []*sql.Stmt{kvStmt, trigStmt, threadStmt, workerStmt, entryStmt, timerStmt, delTimerStmt} {
			if s != nil {
				_ = s.Close()
			}
		}
	}
	defer closeStmts()

	putKV := func(k, v string) {
		if err != nil {
			return
		}
		if cache != nil && cache.kv[k] == v {
			return
		}
		if kvStmt == nil {
			kvStmt, err = tx.Prepare(`INSERT INTO kv(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`)
			if err != nil {
				return
			}
		}
		if _, err = kvStmt.Exec(k, v); err == nil && nextKV != nil {
			nextKV[k] = v
		}
	}

	putKV("seq", strconv.Itoa(st.Seq))
	putKV("settings", js(st.Settings))
	putKV("policies", js(st.Policies))
	dismissed := make([]string, 0, len(st.Dismissed))
	for id, on := range st.Dismissed {
		if on && id != "" {
			dismissed = append(dismissed, id)
		}
	}
	sort.Slice(dismissed, func(i, j int) bool {
		ni, nj := idNum(dismissed[i]), idNum(dismissed[j])
		if ni != nj {
			return ni < nj
		}
		return dismissed[i] < dismissed[j]
	})
	putKV("dismissed", js(dismissed))
	pinned := st.Pinned
	if pinned == nil {
		pinned = []string{}
	}
	if len(pinned) > maxPinned {
		pinned = pinned[len(pinned)-maxPinned:]
	}
	putKV("pinned", js(pinned))
	putKV("last_read_entry_id", st.LastReadEntryID)

	for _, t := range st.Triggers {
		if err != nil {
			break
		}
		raw := js(t)
		if cache != nil && cache.triggers[t.ID] == raw {
			continue
		}
		if trigStmt == nil {
			trigStmt, err = tx.Prepare(`INSERT INTO triggers(id,n,at,status,source,thread,data) VALUES(?,?,?,?,?,?,?)
		     ON CONFLICT(id) DO UPDATE SET status=excluded.status, data=excluded.data`)
			if err != nil {
				break
			}
		}
		if _, err = trigStmt.Exec(t.ID, idNum(t.ID), t.At.Unix(), t.Status, t.Source, t.Thread, raw); err == nil && nextTriggers != nil {
			nextTriggers[t.ID] = raw
		}
	}

	for _, th := range st.Threads {
		if err != nil {
			break
		}
		raw := js(th)
		if cache != nil && cache.threads[th.ID] == raw {
			continue
		}
		if threadStmt == nil {
			threadStmt, err = tx.Prepare(`INSERT INTO threads(id,last_at,data) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET last_at=excluded.last_at, data=excluded.data`)
			if err != nil {
				break
			}
		}
		if _, err = threadStmt.Exec(th.ID, th.LastAt.Unix(), raw); err == nil && nextThreads != nil {
			nextThreads[th.ID] = raw
		}
	}

	for _, w := range st.Workers {
		if err != nil {
			break
		}
		raw := js(w)
		if cache != nil && cache.workers[w.Name] == raw {
			continue
		}
		if workerStmt == nil {
			workerStmt, err = tx.Prepare(`INSERT INTO workers(name,created_at,label,data) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET label=excluded.label, data=excluded.data`)
			if err != nil {
				break
			}
		}
		if _, err = workerStmt.Exec(w.Name, w.CreatedAt.Unix(), w.Label, raw); err == nil && nextWorkers != nil {
			nextWorkers[w.Name] = raw
		}
	}

	for _, e := range st.Entries {
		if err != nil {
			break
		}
		raw := js(e)
		if cache != nil && cache.entries[e.ID] == raw {
			continue
		}
		if entryStmt == nil {
			entryStmt, err = tx.Prepare(`INSERT INTO entries(id,n,at,thread,data,claps) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data, claps=excluded.claps`)
			if err != nil {
				break
			}
		}
		if _, err = entryStmt.Exec(e.ID, idNum(e.ID), e.At.Unix(), e.Thread, raw, clampClaps(e.Claps)); err == nil && nextEntries != nil {
			nextEntries[e.ID] = raw
		}
	}

	for _, t := range st.Timers {
		if err != nil {
			break
		}
		raw := js(t)
		if cache != nil && cache.timers[t.ID] == raw {
			continue
		}
		if timerStmt == nil {
			timerStmt, err = tx.Prepare(`INSERT INTO timers(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`)
			if err != nil {
				break
			}
		}
		if _, err = timerStmt.Exec(t.ID, raw); err == nil && nextTimers != nil {
			nextTimers[t.ID] = raw
		}
	}

	for _, id := range deletedTimers {
		if err != nil {
			break
		}
		if delTimerStmt == nil {
			delTimerStmt, err = tx.Prepare(`DELETE FROM timers WHERE id=?`)
			if err != nil {
				break
			}
		}
		_, err = delTimerStmt.Exec(id)
	}
	closeStmts()
	kvStmt, trigStmt, threadStmt, workerStmt, entryStmt, timerStmt, delTimerStmt = nil, nil, nil, nil, nil, nil, nil
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if cache != nil {
		for k, v := range nextKV {
			cache.kv[k] = v
		}
		for k, v := range nextTriggers {
			cache.triggers[k] = v
		}
		for k, v := range nextThreads {
			cache.threads[k] = v
		}
		for k, v := range nextWorkers {
			cache.workers[k] = v
		}
		for k, v := range nextEntries {
			cache.entries[k] = v
		}
		for k, v := range nextTimers {
			cache.timers[k] = v
		}
		for _, id := range deletedTimers {
			delete(cache.timers, id)
		}
	}
	return nil
}

// threadHistory reads older triggers of a thread straight from the DB, for
// recall beyond the in-memory working set.
func threadHistory(db *sql.DB, thread string, before int64, limit int) []*Trigger {
	var out []*Trigger
	loadRows(db, `SELECT data FROM triggers WHERE thread = ? AND n < ? ORDER BY n DESC LIMIT ?`,
		func(t *Trigger) { out = append(out, t) }, thread, before, limit)
	return out
}

// entryHistory reads a thread's chat from before entry number `before`,
// newest first, straight from the DB (memory keeps only the recent ones).
func entryHistory(db *sql.DB, thread string, before int64, limit int) []*Entry {
	var out []*Entry
	loadEntryRows(db, `SELECT data, claps FROM entries WHERE (thread = ? OR (? = 'chat:main' AND thread = '')) AND n < ? ORDER BY n DESC LIMIT ?`,
		func(e *Entry) { out = append(out, e) }, thread, thread, before, limit)
	return out
}

// entryHistoryUntil reads a thread's chat from before `before` down to `until`
// (inclusive) plus up to `pad` older context entries, newest first, capped at maxRows.
func entryHistoryUntil(db *sql.DB, thread string, before, until int64, pad, maxRows int) ([]*Entry, bool) {
	var out []*Entry
	loadEntryRows(db, `SELECT data, claps FROM entries WHERE (thread = ? OR (? = 'chat:main' AND thread = '')) AND n < ? AND n >= ? ORDER BY n DESC LIMIT ?`,
		func(e *Entry) { out = append(out, e) }, thread, thread, before, until, maxRows+1)
	if len(out) > maxRows {
		return out[:maxRows], true
	}
	if pad < 0 {
		pad = 0
	}
	tail := entryHistory(db, thread, until, pad+1)
	more := len(tail) > pad
	if more {
		tail = tail[:pad]
	}
	out = append(out, tail...)
	return out, more
}

// threadEntries reads all stored entries of a thread from SQLite, newest first.
func threadEntries(db *sql.DB, thread string) []*Entry {
	var out []*Entry
	loadEntryRows(db, `SELECT data, claps FROM entries WHERE (thread = ? OR (? = 'chat:main' AND thread = '')) ORDER BY n DESC`,
		func(e *Entry) { out = append(out, e) }, thread, thread)
	return out
}

// loadAgentRuns restores the models agents were started with (Herdr.runs).
func loadAgentRuns(db *sql.DB, h *Herdr) {
	m := map[string][2]string{}
	var raw string
	if db.QueryRow(`SELECT v FROM kv WHERE k='agent_runs'`).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &m)
	}
	h.LoadRuns(m, func(m map[string][2]string) {
		b, _ := json.Marshal(m)
		if _, err := db.Exec(`INSERT INTO kv(k,v) VALUES('agent_runs',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(b)); err != nil {
			log.Printf("save agent runs: %v", err)
		}
	})
}

// loadAgentNames restores the names Collie gave its agents (see Herdr.named)
// and keeps them saved in the kv table.
func loadAgentNames(db *sql.DB, h *Herdr) {
	m := map[string]string{}
	var raw string
	if db.QueryRow(`SELECT v FROM kv WHERE k='agent_names'`).Scan(&raw) == nil {
		json.Unmarshal([]byte(raw), &m)
	}
	h.LoadNames(m, func(m map[string]string) {
		b, _ := json.Marshal(m)
		if _, err := db.Exec(`INSERT INTO kv(k,v) VALUES('agent_names',?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, string(b)); err != nil {
			log.Printf("save agent names: %v", err)
		}
	})
}

// ---------------------------------------------------------------- integrations storage

type IntegrationRecord struct {
	Kind      string
	Enabled   bool
	Config    json.RawMessage
	Cursor    json.RawMessage
	UpdatedAt int64
}

type IntegrationMessage struct {
	EntryID    string
	Kind       string
	ChatID     string
	ExternalID string
	Resolved   bool
	CreatedAt  int64
}

func loadIntegrations(db *sql.DB) (map[string]IntegrationRecord, error) {
	out := map[string]IntegrationRecord{}
	if db == nil {
		return out, nil
	}
	rows, err := db.Query(`SELECT kind, enabled, config, cursor, updated_at FROM integrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rec IntegrationRecord
		var enabled int
		var cfg, cur string
		if err := rows.Scan(&rec.Kind, &enabled, &cfg, &cur, &rec.UpdatedAt); err != nil {
			return nil, err
		}
		rec.Enabled = enabled != 0
		if strings.TrimSpace(cfg) == "" {
			cfg = "{}"
		}
		if strings.TrimSpace(cur) == "" {
			cur = "{}"
		}
		rec.Config = json.RawMessage(cfg)
		rec.Cursor = json.RawMessage(cur)
		out[rec.Kind] = rec
	}
	return out, rows.Err()
}

func saveIntegration(db *sql.DB, rec IntegrationRecord) error {
	if db == nil {
		return nil
	}
	cfg := strings.TrimSpace(string(rec.Config))
	if cfg == "" {
		cfg = "{}"
	}
	cur := strings.TrimSpace(string(rec.Cursor))
	if cur == "" {
		cur = "{}"
	}
	enabled := 0
	if rec.Enabled {
		enabled = 1
	}
	if rec.UpdatedAt == 0 {
		rec.UpdatedAt = time.Now().Unix()
	}
	_, err := db.Exec(`
		INSERT INTO integrations(kind, enabled, config, cursor, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(kind) DO UPDATE SET
			enabled=excluded.enabled,
			config=excluded.config,
			cursor=CASE WHEN excluded.cursor='{}' AND integrations.cursor!='{}' THEN integrations.cursor ELSE excluded.cursor END,
			updated_at=excluded.updated_at`,
		rec.Kind, enabled, cfg, cur, rec.UpdatedAt)
	return err
}

func saveIntegrationCursor(db *sql.DB, kind string, cursor json.RawMessage) error {
	if db == nil {
		return nil
	}
	cur := strings.TrimSpace(string(cursor))
	if cur == "" {
		cur = "{}"
	}
	_, err := db.Exec(`
		INSERT INTO integrations(kind, enabled, config, cursor, updated_at)
		VALUES(?, 0, '{}', ?, ?)
		ON CONFLICT(kind) DO UPDATE SET cursor=excluded.cursor, updated_at=excluded.updated_at`,
		kind, cur, time.Now().Unix())
	return err
}

// dedupInsert atomically records (source, dedupKey) in SQLite. It returns true
// if the key is new and false if it was already processed.
func dedupInsert(db *sql.DB, source, dedupKey string, now time.Time) (bool, error) {
	if db == nil || source == "" || dedupKey == "" {
		return true, nil
	}
	res, err := db.Exec(`INSERT OR IGNORE INTO integration_dedup(source, dedup_key, seen_at) VALUES(?, ?, ?)`,
		source, dedupKey, now.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func dedupPrune(db *sql.DB, cutoff time.Time) (int64, error) {
	if db == nil {
		return 0, nil
	}
	res, err := db.Exec(`DELETE FROM integration_dedup WHERE seen_at < ?`, cutoff.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func saveIntegrationMessage(db *sql.DB, m IntegrationMessage) error {
	if db == nil || m.EntryID == "" || m.Kind == "" || m.ExternalID == "" {
		return nil
	}
	resolved := 0
	if m.Resolved {
		resolved = 1
	}
	if m.CreatedAt == 0 {
		m.CreatedAt = time.Now().Unix()
	}
	_, err := db.Exec(`
		INSERT INTO integration_messages(entry_id, kind, chat_id, external_id, resolved, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(entry_id, kind) DO UPDATE SET
			chat_id=excluded.chat_id,
			external_id=excluded.external_id,
			resolved=excluded.resolved`,
		m.EntryID, m.Kind, m.ChatID, m.ExternalID, resolved, m.CreatedAt)
	return err
}

func getIntegrationMessage(db *sql.DB, entryID, kind string) (*IntegrationMessage, error) {
	if db == nil || entryID == "" || kind == "" {
		return nil, nil
	}
	var m IntegrationMessage
	var resolved int
	err := db.QueryRow(`SELECT entry_id, kind, chat_id, external_id, resolved, created_at FROM integration_messages WHERE entry_id=? AND kind=?`,
		entryID, kind).Scan(&m.EntryID, &m.Kind, &m.ChatID, &m.ExternalID, &resolved, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Resolved = resolved != 0
	return &m, nil
}

func getIntegrationMessageByExternal(db *sql.DB, kind, externalID string) (*IntegrationMessage, error) {
	if db == nil || kind == "" || externalID == "" {
		return nil, nil
	}
	var m IntegrationMessage
	var resolved int
	err := db.QueryRow(`SELECT entry_id, kind, chat_id, external_id, resolved, created_at FROM integration_messages WHERE kind=? AND external_id=? LIMIT 1`,
		kind, externalID).Scan(&m.EntryID, &m.Kind, &m.ChatID, &m.ExternalID, &resolved, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	m.Resolved = resolved != 0
	return &m, nil
}

// markIntegrationMessageResolved flips resolved 0 -> 1 atomically and reports
// whether this call performed the transition.
func markIntegrationMessageResolved(db *sql.DB, entryID, kind string) (bool, error) {
	if db == nil || entryID == "" || kind == "" {
		return false, nil
	}
	res, err := db.Exec(`UPDATE integration_messages SET resolved=1 WHERE entry_id=? AND kind=? AND resolved=0`, entryID, kind)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
