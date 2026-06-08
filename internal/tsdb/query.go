package tsdb

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// AggFunc selects bucket aggregation.
type AggFunc string

const (
	AggAvg   AggFunc = "avg"
	AggMin   AggFunc = "min"
	AggMax   AggFunc = "max"
	AggSum   AggFunc = "sum"
	AggLast  AggFunc = "last"
	AggCount AggFunc = "count"
)

// Query parameters. Either ObjectID+Metric or explicit SeriesIDs.
// MaxPoints drives server-side downsampling to pixel width (SPEC §7.3):
// step = ceil(range / MaxPoints) clamped to the natural tier.
type Query struct {
	ObjectID  string
	ObjectIDs []string // alternative: multiple objects (same metric)
	Metric    string   // "" = all metrics of the object(s)
	From, To  time.Time
	Step      time.Duration // 0 = derive from MaxPoints
	MaxPoints int           // default 500
	Agg       AggFunc       // default avg
}

// Result is one rendered series.
type Result struct {
	Series SeriesMeta `json:"series"`
	Points []Sample   `json:"points"`
}

// Query evaluates against head, raw blocks and aggregate tiers.
func (db *DB) Query(ctx context.Context, q Query) ([]Result, error) {
	if q.To.IsZero() {
		q.To = time.Now()
	}
	if q.From.IsZero() {
		q.From = q.To.Add(-24 * time.Hour)
	}
	if !q.From.Before(q.To) {
		return nil, fmt.Errorf("tsdb: empty time range")
	}
	if q.MaxPoints <= 0 || q.MaxPoints > 10000 {
		q.MaxPoints = 500
	}
	if q.Agg == "" {
		q.Agg = AggAvg
	}
	step := q.Step
	if step <= 0 {
		step = time.Duration(int64(q.To.Sub(q.From)) / int64(q.MaxPoints))
	}
	if step < time.Second {
		step = time.Second
	}

	ids := db.resolveSeries(q)
	if len(ids) == 0 {
		return nil, nil
	}

	fromMS, toMS := q.From.UnixMilli(), q.To.UnixMilli()
	stepMS := step.Milliseconds()

	var out []Result
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		db.mu.RLock()
		meta := db.byID[id]
		db.mu.RUnlock()
		if meta == nil {
			continue
		}
		points, err := db.seriesRange(id, fromMS, toMS, stepMS, q.Agg)
		if err != nil {
			return nil, err
		}
		out = append(out, Result{Series: *meta, Points: points})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Series.ObjectID != out[j].Series.ObjectID {
			return out[i].Series.ObjectID < out[j].Series.ObjectID
		}
		return out[i].Series.Metric < out[j].Series.Metric
	})
	return out, nil
}

func (db *DB) resolveSeries(q Query) []uint64 {
	db.mu.RLock()
	defer db.mu.RUnlock()
	objects := q.ObjectIDs
	if q.ObjectID != "" {
		objects = append(objects, q.ObjectID)
	}
	var ids []uint64
	for _, obj := range objects {
		for _, id := range db.byObject[obj] {
			if q.Metric == "" || db.byID[id].Metric == q.Metric {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// bucketAcc accumulates one output bucket.
type bucketAcc struct {
	count   uint64
	sum     float64
	min     float64
	max     float64
	last    float64
	lastT   int64
	touched bool
}

func (b *bucketAcc) addSample(t int64, v float64) {
	// Defensive: skip non-finite values that may exist in pre-fix on-disk
	// blocks so they cannot poison min/max/avg or break JSON encoding.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return
	}
	if !b.touched {
		b.min, b.max = v, v
		b.touched = true
	} else {
		if v < b.min {
			b.min = v
		}
		if v > b.max {
			b.max = v
		}
	}
	b.count++
	b.sum += v
	if t >= b.lastT {
		b.last, b.lastT = v, t
	}
}

func (b *bucketAcc) addAgg(rec AggRecord) {
	if !b.touched {
		b.min, b.max = rec.Min, rec.Max
		b.touched = true
	} else {
		if rec.Min < b.min {
			b.min = rec.Min
		}
		if rec.Max > b.max {
			b.max = rec.Max
		}
	}
	b.count += uint64(rec.Count)
	b.sum += rec.Sum
	if rec.Count > 0 {
		b.last = rec.Sum / float64(rec.Count) // best effort for tiers
	}
}

func (b *bucketAcc) value(fn AggFunc) (float64, bool) {
	if !b.touched || b.count == 0 {
		return 0, false
	}
	switch fn {
	case AggMin:
		return b.min, true
	case AggMax:
		return b.max, true
	case AggSum:
		return b.sum, true
	case AggLast:
		return b.last, true
	case AggCount:
		return float64(b.count), true
	default:
		return b.sum / float64(b.count), true
	}
}

// seriesRange gathers samples for [from,to) and reduces to step buckets.
// Tier choice: raw where available; otherwise 5-min, then 1-h aggregates
// (per sub-range, so queries spanning the raw horizon degrade gracefully).
func (db *DB) seriesRange(id uint64, fromMS, toMS, stepMS int64, fn AggFunc) ([]Sample, error) {
	nBuckets := int((toMS-fromMS)/stepMS) + 1
	if nBuckets <= 0 || nBuckets > 20000 {
		return nil, fmt.Errorf("tsdb: too many buckets")
	}
	buckets := make([]bucketAcc, nBuckets)
	put := func(t int64, v float64) {
		if t < fromMS || t >= toMS {
			return
		}
		buckets[(t-fromMS)/stepMS].addSample(t, v)
	}

	// 1) raw blocks overlapping the range
	covered := int64(math.MaxInt64) // earliest raw coverage seen
	for _, ws := range db.listBlockStarts() {
		we := ws + rawWindow.Milliseconds()
		if we <= fromMS || ws >= toMS {
			if ws < covered && we > fromMS {
				covered = ws
			}
			continue
		}
		path := db.blockPath(ws)
		idx, _, err := readBlockIndex(path)
		if err != nil {
			// The block-starts list is cached, so a start may name a file that
			// has since vanished (out-of-band removal / retention racing a
			// query). Skip it *without* marking the sub-range as raw-covered,
			// so the aggregate tiers still fill that gap below.
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		// Only now that the block is readable does it count as raw coverage.
		if ws < covered {
			covered = ws
		}
		samples, err := readBlockSeries(path, idx, blockHeaderSize(len(idx)), id)
		if err != nil {
			return nil, err
		}
		for _, s := range samples {
			put(s.T, s.V)
		}
	}

	// 2) head windows
	db.mu.RLock()
	var headChunks []struct {
		payload []byte
		count   uint32
	}
	if hs := db.heads[id]; hs != nil {
		for ws, app := range hs.windows {
			if ws+rawWindow.Milliseconds() <= fromMS || ws >= toMS {
				continue
			}
			if ws < covered {
				covered = ws
			}
			payload := make([]byte, len(app.Bytes()))
			copy(payload, app.Bytes())
			headChunks = append(headChunks, struct {
				payload []byte
				count   uint32
			}{payload, app.Count()})
		}
	}
	db.mu.RUnlock()
	for _, hc := range headChunks {
		samples, err := DecodeChunk(hc.payload, hc.count)
		if err != nil {
			return nil, err
		}
		for _, s := range samples {
			put(s.T, s.V)
		}
	}

	// 3) aggregate tiers for the sub-range before raw coverage
	aggEnd := toMS
	if covered != math.MaxInt64 && covered < aggEnd {
		aggEnd = covered
	}
	if aggEnd > fromMS {
		if err := db.fillFromAggregates(id, fromMS, aggEnd, stepMS, buckets); err != nil {
			return nil, err
		}
	}

	out := make([]Sample, 0, nBuckets)
	for i := range buckets {
		if v, ok := buckets[i].value(fn); ok {
			out = append(out, Sample{T: fromMS + int64(i)*stepMS, V: v})
		}
	}
	return out, nil
}

// fillFromAggregates pulls 5m (preferred) and 1h tier data.
func (db *DB) fillFromAggregates(id uint64, fromMS, toMS, stepMS int64, buckets []bucketAcc) error {
	for _, tier := range []struct {
		suffix   string
		bucketMS int64
	}{{"5m", bucket5m.Milliseconds()}, {"1h", bucket1h.Milliseconds()}} {
		matches, _ := filepath.Glob(filepath.Join(db.dir, "agg", "agg-*-"+tier.suffix+".npa"))
		found := false
		for _, m := range matches {
			var dayStart int64
			if _, err := fmt.Sscanf(filepath.Base(m), "agg-%d-"+tier.suffix+".npa", &dayStart); err != nil {
				continue
			}
			dayEnd := dayStart + aggWindow.Milliseconds()
			if dayEnd <= fromMS || dayStart >= toMS {
				continue
			}
			idx, ds, _, err := readAggIndex(m)
			if err != nil {
				continue
			}
			recs, err := readAggSeries(m, idx, aggHeaderSize(len(idx)), id)
			if err != nil {
				return err
			}
			for _, rec := range recs {
				t := ds + int64(rec.BucketIdx)*tier.bucketMS
				if t < fromMS || t >= toMS {
					continue
				}
				buckets[(t-fromMS)/stepMS].addAgg(rec)
				found = true
			}
		}
		if found {
			return nil // finest tier with data wins
		}
	}
	return nil
}

// compactAggregates builds daily 5m/1h files for fully-elapsed days from
// raw blocks (nightly batch, drosselbar via ctx — SPEC §7.3/A-15.23).
func (db *DB) compactAggregates(ctx context.Context, now time.Time) error {
	dayMS := aggWindow.Milliseconds()
	today := now.UTC().UnixMilli() - (now.UTC().UnixMilli() % dayMS)
	blocks := db.listBlockStarts()
	byDay := map[int64][]int64{}
	for _, ws := range blocks {
		day := ws - (ws % dayMS)
		if day < today { // only completed days
			byDay[day] = append(byDay[day], ws)
		}
	}
	for day, wss := range byDay {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		for _, tier := range []struct {
			suffix   string
			bucketMS int64
		}{{"5m", bucket5m.Milliseconds()}, {"1h", bucket1h.Milliseconds()}} {
			path := filepath.Join(db.dir, "agg", fmt.Sprintf("agg-%013d-%s.npa", day, tier.suffix))
			if _, err := os.Stat(path); err == nil {
				continue // already compacted
			}
			acc := map[uint64]map[uint32]*bucketAcc{}
			for _, ws := range wss {
				bp := db.blockPath(ws)
				idx, _, err := readBlockIndex(bp)
				if err != nil {
					continue
				}
				hs := blockHeaderSize(len(idx))
				for seriesID := range idx {
					samples, err := readBlockSeries(bp, idx, hs, seriesID)
					if err != nil {
						return err
					}
					sm := acc[seriesID]
					if sm == nil {
						sm = map[uint32]*bucketAcc{}
						acc[seriesID] = sm
					}
					for _, s := range samples {
						bi := uint32((s.T - day) / tier.bucketMS)
						b := sm[bi]
						if b == nil {
							b = &bucketAcc{}
							sm[bi] = b
						}
						b.addSample(s.T, s.V)
					}
				}
			}
			var entries []aggEntry
			for seriesID, sm := range acc {
				var recs []AggRecord
				for bi, b := range sm {
					recs = append(recs, AggRecord{
						BucketIdx: bi, Count: uint32(b.count),
						Sum: b.sum, Min: b.min, Max: b.max,
					})
				}
				sort.Slice(recs, func(i, j int) bool { return recs[i].BucketIdx < recs[j].BucketIdx })
				entries = append(entries, aggEntry{seriesID, recs})
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].seriesID < entries[j].seriesID })
			if len(entries) > 0 {
				if err := writeAggFile(path, day, uint32(tier.bucketMS), entries); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
