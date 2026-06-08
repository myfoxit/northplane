package tsdb

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Window and tier geometry (SPEC §7.3).
const (
	rawWindow  = 2 * time.Hour
	flushGrace = 5 * time.Minute
	aggWindow  = 24 * time.Hour
	bucket5m   = 5 * time.Minute
	bucket1h   = time.Hour
)

// Retention defaults (SPEC §7.3: raw 30 d → 5-min 400 d → 1-h 5 a).
type Retention struct {
	Raw   time.Duration
	Agg5m time.Duration
	Agg1h time.Duration
}

var DefaultRetention = Retention{
	Raw:   30 * 24 * time.Hour,
	Agg5m: 400 * 24 * time.Hour,
	Agg1h: 5 * 365 * 24 * time.Hour,
}

// DefaultMaxSeries bounds registry cardinality (SPEC §7.3): a misbehaving
// integration that emits unbounded label combinations would otherwise grow
// the in-memory maps without limit. Past the cap, new series are dropped and
// counted (Stats.SeriesDropped). Override per instance with SetMaxSeries.
const DefaultMaxSeries = 100_000

// SeriesKey identifies a series (SPEC §7.3): object + metric + unit +
// labels hash.
type SeriesKey struct {
	ObjectID string `json:"objectId"`
	Metric   string `json:"metric"`
	Unit     string `json:"unit,omitempty"`
	LabelsH  uint64 `json:"labelsHash,omitempty"`
}

// SeriesMeta is the registry entry.
type SeriesMeta struct {
	ID       uint64            `json:"id"`
	ObjectID string            `json:"objectId"`
	Metric   string            `json:"metric"`
	Unit     string            `json:"unit,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Warn     string            `json:"warn,omitempty"` // Nagios range spec
	Crit     string            `json:"crit,omitempty"`
	Min      *float64          `json:"min,omitempty"`
	Max      *float64          `json:"max,omitempty"`
	// Deleted marks a tombstone record in the append-only journal: a later
	// line with this set removes the series from the registry on replay (see
	// DropObject / loadSeries). Set on the journal line only, never on a live
	// registry entry.
	Deleted bool `json:"deleted,omitempty"`
}

func labelsHash(labels map[string]string) uint64 {
	if len(labels) == 0 {
		return 0
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(labels[k]))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

// headSeries holds the open windows of one series.
type headSeries struct {
	windows map[int64]*ChunkAppender // window start ms → appender
	lastT   int64
}

// DB is the NP-TSDB instance.
type DB struct {
	dir string
	log *slog.Logger
	ret Retention

	mu       sync.RWMutex
	nextID   uint64
	byKey    map[SeriesKey]uint64
	byID     map[uint64]*SeriesMeta
	byObject map[string][]uint64
	heads    map[uint64]*headSeries

	// maxSeries caps registry cardinality; new series past it are dropped
	// (statSeriesDropped++) so a runaway label explosion can't grow the maps
	// unboundedly. 0 = unlimited.
	maxSeries int

	// blockStarts caches the sorted window starts on disk so seriesRange does
	// not glob the blocks dir on every call. Guarded by blockMu; invalidated
	// (set blockCacheOK=false) whenever a block is written or retention runs.
	blockMu      sync.Mutex
	blockStarts  []int64
	blockCacheOK bool

	seriesF *os.File
	seriesW *bufio.Writer

	walMu sync.Mutex
	walF  *os.File
	walW  *bufio.Writer

	stopFsync chan struct{}
	fsyncWG   sync.WaitGroup
	closeOnce sync.Once

	statSamples       uint64
	statDropped       uint64
	statSeriesDropped uint64
}

// SetMaxSeries overrides the cardinality cap (0 = unlimited). Safe to call
// after Open; existing series are kept, only new registrations are bounded.
func (db *DB) SetMaxSeries(n int) {
	db.mu.Lock()
	db.maxSeries = n
	db.mu.Unlock()
}

// Open loads (or initialises) the engine at dir.
func Open(dir string, log *slog.Logger, ret Retention) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	if ret.Raw == 0 {
		ret = DefaultRetention
	}
	for _, sub := range []string{"", "blocks", "agg"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o750); err != nil {
			return nil, err
		}
	}
	db := &DB{
		dir: dir, log: log, ret: ret,
		byKey: map[SeriesKey]uint64{}, byID: map[uint64]*SeriesMeta{},
		byObject: map[string][]uint64{}, heads: map[uint64]*headSeries{},
		nextID: 1, stopFsync: make(chan struct{}),
		maxSeries: DefaultMaxSeries,
	}
	if err := db.loadSeries(); err != nil {
		return nil, err
	}
	if err := db.openWAL(); err != nil {
		return nil, err
	}
	if err := db.replayWAL(); err != nil {
		return nil, err
	}
	// fsync batching 1 s (SPEC §7.3).
	db.fsyncWG.Add(1)
	go func() {
		defer db.fsyncWG.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				db.syncWAL()
			case <-db.stopFsync:
				db.syncWAL()
				return
			}
		}
	}()
	return db, nil
}

// --- series registry (append-only JSONL journal) ---

func (db *DB) seriesPath() string { return filepath.Join(db.dir, "series.jsonl") }

func (db *DB) loadSeries() error {
	f, err := os.Open(db.seriesPath())
	if os.IsNotExist(err) {
		return db.openSeriesAppend()
	}
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var m SeriesMeta
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // tolerate torn tail line
		}
		key := SeriesKey{ObjectID: m.ObjectID, Metric: m.Metric, Unit: m.Unit, LabelsH: labelsHash(m.Labels)}
		if m.Deleted {
			// Tombstone: drop the series resurrected by earlier lines.
			if old, ok := db.byKey[key]; ok {
				delete(db.byKey, key)
				delete(db.byID, old)
				removeID(db.byObject, m.ObjectID, old)
			}
			continue
		}
		if old, ok := db.byKey[key]; ok {
			// later line updates metadata in place (thresholds)
			meta := db.byID[old]
			meta.Warn, meta.Crit, meta.Min, meta.Max = m.Warn, m.Crit, m.Min, m.Max
			continue
		}
		mm := m
		db.byKey[key] = m.ID
		db.byID[m.ID] = &mm
		db.byObject[m.ObjectID] = append(db.byObject[m.ObjectID], m.ID)
		if m.ID >= db.nextID {
			db.nextID = m.ID + 1
		}
	}
	f.Close()
	if err := sc.Err(); err != nil {
		return err
	}
	return db.openSeriesAppend()
}

func (db *DB) openSeriesAppend() error {
	f, err := os.OpenFile(db.seriesPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	db.seriesF, db.seriesW = f, bufio.NewWriter(f)
	return nil
}

// series resolves/creates the series for an append. Caller holds db.mu.
func (db *DB) series(objectID, metric, unit string, labels map[string]string, warn, crit string, min, max *float64) *SeriesMeta {
	key := SeriesKey{ObjectID: objectID, Metric: metric, Unit: unit, LabelsH: labelsHash(labels)}
	if id, ok := db.byKey[key]; ok {
		meta := db.byID[id]
		if meta.Warn != warn || meta.Crit != crit {
			meta.Warn, meta.Crit = warn, crit
			meta.Min, meta.Max = min, max
			line, _ := json.Marshal(meta)
			db.seriesW.Write(line)
			db.seriesW.WriteByte('\n')
		}
		return meta
	}
	// Cardinality cap: refuse to register a brand-new series once the registry
	// is full. Returning nil makes Append drop the sample (statSeriesDropped++)
	// instead of growing byKey/byID/byObject without bound.
	if db.maxSeries > 0 && len(db.byID) >= db.maxSeries {
		db.statSeriesDropped++
		return nil
	}
	meta := &SeriesMeta{ID: db.nextID, ObjectID: objectID, Metric: metric, Unit: unit,
		Labels: labels, Warn: warn, Crit: crit, Min: min, Max: max}
	db.nextID++
	db.byKey[key] = meta.ID
	db.byID[meta.ID] = meta
	db.byObject[objectID] = append(db.byObject[objectID], meta.ID)
	line, _ := json.Marshal(meta)
	db.seriesW.Write(line)
	db.seriesW.WriteByte('\n')
	return meta
}

// --- WAL ---

func (db *DB) walPath() string { return filepath.Join(db.dir, "wal.log") }

func (db *DB) openWAL() error {
	f, err := os.OpenFile(db.walPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	db.walF, db.walW = f, bufio.NewWriterSize(f, 64<<10)
	return nil
}

func (db *DB) syncWAL() {
	db.walMu.Lock()
	db.walW.Flush()
	db.walF.Sync()
	db.walMu.Unlock()
	db.mu.Lock()
	db.seriesW.Flush()
	db.mu.Unlock()
}

func (db *DB) walAppend(seriesID uint64, t int64, vbits uint64) {
	var rec [25]byte
	rec[0] = 1
	binary.BigEndian.PutUint64(rec[1:], seriesID)
	binary.BigEndian.PutUint64(rec[9:], uint64(t))
	binary.BigEndian.PutUint64(rec[17:], vbits)
	db.walMu.Lock()
	db.walW.Write(rec[:])
	db.walMu.Unlock()
}

// replayWAL re-populates heads after a crash (skipping samples whose
// window block already exists on disk).
func (db *DB) replayWAL() error {
	f, err := os.Open(db.walPath())
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var rec [25]byte
	n := 0
	for {
		if _, err := io.ReadFull(r, rec[:1]); err != nil {
			break // EOF or torn record: stop replay
		}
		if rec[0] != 1 {
			break
		}
		if _, err := io.ReadFull(r, rec[1:]); err != nil {
			break
		}
		seriesID := binary.BigEndian.Uint64(rec[1:9])
		t := int64(binary.BigEndian.Uint64(rec[9:17]))
		v := binary.BigEndian.Uint64(rec[17:25])
		ws := windowStart(t)
		if db.blockExists(ws) {
			continue
		}
		db.headAppend(seriesID, t, mathFloat64frombits(v))
		n++
	}
	if n > 0 {
		db.log.Info("tsdb: wal replayed", "samples", n)
	}
	return nil
}

func windowStart(tms int64) int64 {
	w := rawWindow.Milliseconds()
	return tms - (tms % w)
}

func (db *DB) blockPath(ws int64) string {
	return filepath.Join(db.dir, "blocks", fmt.Sprintf("block-%013d.npb", ws))
}

func (db *DB) blockExists(ws int64) bool {
	_, err := os.Stat(db.blockPath(ws))
	return err == nil
}

// headAppend adds to the in-memory head. Caller need not hold locks.
func (db *DB) headAppend(seriesID uint64, t int64, v float64) bool {
	db.mu.Lock()
	defer db.mu.Unlock()
	hs := db.heads[seriesID]
	if hs == nil {
		hs = &headSeries{windows: map[int64]*ChunkAppender{}}
		db.heads[seriesID] = hs
	}
	if t <= hs.lastT {
		db.statDropped++
		return false
	}
	ws := windowStart(t)
	app := hs.windows[ws]
	if app == nil {
		// Never reopen a window that has already been flushed to a block:
		// a fresh appender here would, on the next Flush, overwrite the
		// existing block with only the late sample(s). Drop instead.
		if db.blockExists(ws) {
			db.statDropped++
			return false
		}
		app = NewChunkAppender()
		hs.windows[ws] = app
	}
	if !app.Append(t, v) {
		db.statDropped++
		return false
	}
	hs.lastT = t
	db.statSamples++
	return true
}

// Append ingests one sample with series metadata (perfdata thresholds
// ride along and update the registry).
func (db *DB) Append(objectID, metric, unit string, labels map[string]string,
	warn, crit string, min, max *float64, ts time.Time, v float64) {
	// Reject non-finite values: NaN/Inf poison min/max/avg aggregation and
	// break JSON encoding when the series is later served via the API.
	if mathIsNaN(v) || mathIsInf(v) {
		db.mu.Lock()
		db.statDropped++
		db.mu.Unlock()
		return
	}
	db.mu.Lock()
	meta := db.series(objectID, metric, unit, labels, warn, crit, min, max)
	db.mu.Unlock()
	if meta == nil {
		return // cardinality cap reached (counted in series())
	}
	id := meta.ID

	t := ts.UnixMilli()
	if db.headAppend(id, t, v) {
		db.walAppend(id, t, mathFloat64bits(v))
	}
}

// flushEntry pairs a series with its closed-window appender.
type flushEntry struct {
	seriesID uint64
	app      *ChunkAppender
}

// Flush writes all closed windows to block files and compacts the WAL.
// now is injectable for tests.
func (db *DB) Flush(now time.Time) error {
	cutoff := now.Add(-flushGrace).UnixMilli()
	closed := map[int64][]flushEntry{}

	db.mu.Lock()
	for id, hs := range db.heads {
		for ws, app := range hs.windows {
			if ws+rawWindow.Milliseconds() <= cutoff && app.Count() > 0 {
				closed[ws] = append(closed[ws], flushEntry{id, app})
				delete(hs.windows, ws)
			}
		}
	}
	db.mu.Unlock()

	for ws, list := range closed {
		if err := db.writeBlock(ws, list); err != nil {
			return err
		}
	}
	if len(closed) > 0 {
		if err := db.rewriteWAL(); err != nil {
			return err
		}
	}
	return nil
}

type blockEntry struct {
	seriesID uint64
	payload  []byte
	count    uint32
}

func (db *DB) writeBlock(ws int64, list []flushEntry) error {
	entries := make([]blockEntry, 0, len(list))
	for _, p := range list {
		entries = append(entries, blockEntry{p.seriesID, p.app.Bytes(), p.app.Count()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seriesID < entries[j].seriesID })
	if err := writeBlockFile(db.blockPath(ws), ws, ws+rawWindow.Milliseconds(), entries); err != nil {
		return err
	}
	db.invalidateBlockCache() // a new block start is now on disk
	return nil
}

// rewriteWAL re-logs only still-open head windows (atomically).
func (db *DB) rewriteWAL() error {
	tmp := db.walPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)

	// Snapshot each open window's encoded bytes+count *under the lock* —
	// decoding the live appender after releasing the lock races with
	// concurrent Append (torn reads, lost recent samples).
	db.mu.RLock()
	type openWin struct {
		id    uint64
		bytes []byte
		count uint32
	}
	var open []openWin
	for id, hs := range db.heads {
		for _, app := range hs.windows {
			src := app.Bytes()
			cp := make([]byte, len(src))
			copy(cp, src)
			open = append(open, openWin{id, cp, app.Count()})
		}
	}
	db.mu.RUnlock()

	var rec [25]byte
	rec[0] = 1
	for _, ow := range open {
		samples, err := DecodeChunk(ow.bytes, ow.count)
		if err != nil {
			continue
		}
		for _, s := range samples {
			binary.BigEndian.PutUint64(rec[1:], ow.id)
			binary.BigEndian.PutUint64(rec[9:], uint64(s.T))
			binary.BigEndian.PutUint64(rec[17:], mathFloat64bits(s.V))
			w.Write(rec[:])
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	f.Close()

	db.walMu.Lock()
	defer db.walMu.Unlock()
	db.walW.Flush()
	db.walF.Close()
	if err := os.Rename(tmp, db.walPath()); err != nil {
		return err
	}
	if err := syncDir(db.walPath()); err != nil {
		return err
	}
	nf, err := os.OpenFile(db.walPath(), os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	db.walF, db.walW = nf, bufio.NewWriterSize(nf, 64<<10)
	return nil
}

// Close flushes everything (treating all windows as closed). It is
// idempotent — a second call is a no-op rather than a panic on the
// already-closed stopFsync channel.
func (db *DB) Close() error {
	var err error
	closed := false
	db.closeOnce.Do(func() {
		closed = true
		close(db.stopFsync)
		db.fsyncWG.Wait()
		// flush every non-empty window regardless of age
		err = db.Flush(time.Now().Add(rawWindow + flushGrace))
		db.walMu.Lock()
		db.walW.Flush()
		db.walF.Sync()
		db.walF.Close()
		db.walMu.Unlock()
		db.mu.Lock()
		db.seriesW.Flush()
		db.seriesF.Close()
		db.mu.Unlock()
	})
	_ = closed
	return err
}

// SeriesForObject lists series metadata of an object.
func (db *DB) SeriesForObject(objectID string) []*SeriesMeta {
	db.mu.RLock()
	defer db.mu.RUnlock()
	ids := db.byObject[objectID]
	out := make([]*SeriesMeta, 0, len(ids))
	for _, id := range ids {
		if m := db.byID[id]; m != nil {
			c := *m
			out = append(out, &c)
		}
	}
	return out
}

// removeID deletes one series ID from m[objectID], pruning the key when the
// last ID is gone. Caller holds db.mu (write).
func removeID(m map[string][]uint64, objectID string, id uint64) {
	ids := m[objectID]
	for i, v := range ids {
		if v == id {
			ids = append(ids[:i], ids[i+1:]...)
			break
		}
	}
	if len(ids) == 0 {
		delete(m, objectID)
	} else {
		m[objectID] = ids
	}
}

// DropObject tombstones and removes every series belonging to the given
// object IDs so an object deletion can reclaim TSDB space. It clears the
// in-memory registry (byKey/byID/byObject) and any open head windows, and
// appends a tombstone line per series to the journal so the removal survives
// a restart. On-disk block/agg files are left to retention (they are pruned
// by age and a dropped series simply stops being read).
//
// Wiring this into object-deletion call sites is the caller's job; this only
// provides the reclaim primitive. Safe for concurrent use.
func (db *DB) DropObject(objectIDs ...string) {
	if len(objectIDs) == 0 {
		return
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	for _, objectID := range objectIDs {
		ids := db.byObject[objectID]
		if len(ids) == 0 {
			continue
		}
		for _, id := range ids {
			meta := db.byID[id]
			if meta != nil {
				key := SeriesKey{ObjectID: meta.ObjectID, Metric: meta.Metric,
					Unit: meta.Unit, LabelsH: labelsHash(meta.Labels)}
				delete(db.byKey, key)
				// Append a tombstone so replay drops it after a restart.
				tomb := SeriesMeta{ObjectID: meta.ObjectID, Metric: meta.Metric,
					Unit: meta.Unit, Labels: meta.Labels, Deleted: true}
				if line, err := json.Marshal(tomb); err == nil {
					db.seriesW.Write(line)
					db.seriesW.WriteByte('\n')
				}
			}
			delete(db.byID, id)
			delete(db.heads, id)
		}
		delete(db.byObject, objectID)
	}
	db.seriesW.Flush()
}

// Stats for self-monitoring (SPEC §15.4).
type Stats struct {
	Series        int    `json:"series"`
	Samples       uint64 `json:"samplesIngested"`
	Dropped       uint64 `json:"samplesDropped"`
	SeriesDropped uint64 `json:"seriesDropped"`
	Blocks        int    `json:"blocks"`
	WALBytes      int64  `json:"walBytes"`
}

// Stats snapshots counters.
func (db *DB) Stats() Stats {
	db.mu.RLock()
	st := Stats{Series: len(db.byID), Samples: db.statSamples,
		Dropped: db.statDropped, SeriesDropped: db.statSeriesDropped}
	db.mu.RUnlock()
	if fi, err := os.Stat(db.walPath()); err == nil {
		st.WALBytes = fi.Size()
	}
	if matches, err := filepath.Glob(filepath.Join(db.dir, "blocks", "block-*.npb")); err == nil {
		st.Blocks = len(matches)
	}
	return st
}

// Maintain runs flush + downsampling + retention; meant for a periodic
// (nightly for compaction, SPEC §7.3 / A-15.23) driver. Throttled by ctx.
func (db *DB) Maintain(ctx context.Context, now time.Time) error {
	if err := db.Flush(now); err != nil {
		return err
	}
	if err := db.compactAggregates(ctx, now); err != nil {
		return err
	}
	return db.enforceRetention(now)
}

func (db *DB) enforceRetention(now time.Time) error {
	drop := func(glob string, keep time.Duration, parse func(name string) (int64, bool)) error {
		matches, err := filepath.Glob(glob)
		if err != nil {
			return err
		}
		cut := now.Add(-keep).UnixMilli()
		for _, m := range matches {
			if start, ok := parse(filepath.Base(m)); ok && start < cut {
				if err := os.Remove(m); err != nil {
					return err
				}
			}
		}
		return nil
	}
	parseBlock := func(name string) (int64, bool) {
		var ws int64
		_, err := fmt.Sscanf(name, "block-%d.npb", &ws)
		return ws, err == nil
	}
	parseAgg := func(name string) (int64, bool) {
		var ws int64
		var tier string
		_, err := fmt.Sscanf(name, "agg-%d-%s", &ws, &tier)
		return ws, err == nil
	}
	if err := drop(filepath.Join(db.dir, "blocks", "block-*.npb"), db.ret.Raw, parseBlock); err != nil {
		return err
	}
	db.invalidateBlockCache() // raw blocks may have been removed above
	if err := drop(filepath.Join(db.dir, "agg", "agg-*-5m.npa"), db.ret.Agg5m, parseAgg); err != nil {
		return err
	}
	return drop(filepath.Join(db.dir, "agg", "agg-*-1h.npa"), db.ret.Agg1h, parseAgg)
}

func mathFloat64bits(v float64) uint64     { return math.Float64bits(v) }
func mathFloat64frombits(b uint64) float64 { return math.Float64frombits(b) }
func mathIsNaN(v float64) bool             { return math.IsNaN(v) }
func mathIsInf(v float64) bool             { return math.IsInf(v, 0) }

// listBlockStarts returns sorted window starts present on disk. The result is
// cached (seriesRange calls this per query): the blocks dir only changes when
// a block is written (Flush→writeBlock) or removed (enforceRetention), both of
// which call invalidateBlockCache. Callers must not mutate the returned slice.
func (db *DB) listBlockStarts() []int64 {
	db.blockMu.Lock()
	defer db.blockMu.Unlock()
	if db.blockCacheOK {
		return db.blockStarts
	}
	matches, _ := filepath.Glob(filepath.Join(db.dir, "blocks", "block-*.npb"))
	out := make([]int64, 0, len(matches))
	for _, m := range matches {
		var ws int64
		if _, err := fmt.Sscanf(filepath.Base(m), "block-%d.npb", &ws); err == nil {
			out = append(out, ws)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	db.blockStarts, db.blockCacheOK = out, true
	return out
}

// invalidateBlockCache forces the next listBlockStarts to re-glob the dir.
func (db *DB) invalidateBlockCache() {
	db.blockMu.Lock()
	db.blockCacheOK = false
	db.blockStarts = nil
	db.blockMu.Unlock()
}

func sanitizeMetric(m string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			return r
		}
		return '_'
	}, m)
}
