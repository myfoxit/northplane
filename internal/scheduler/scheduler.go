// Package scheduler drives active checks: a timing wheel with
// deterministic splay (SPEC §7.4). Start offsets derive from
// hash(object_id) → even load distribution, no check storms after
// restart; intervals 1 s … 24 h. Drift-free: the next run is scheduled
// from the planned time, not from completion.
package scheduler

import (
	"context"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/northplane/northplane/internal/catalog"
	"github.com/northplane/northplane/internal/model"
)

const (
	wheelSlots = 86400 // 1 s granularity, 24 h ring (max interval)
	tick       = 250 * time.Millisecond
)

// Job is one due check dispatched to the executor.
type Job struct {
	ObjectID string
	Planned  time.Time
	Priority bool // check-now lane (SPEC §7.4)
}

type entry struct {
	objectID string
	interval time.Duration
	due      time.Time
	slot     int
}

// Scheduler owns the wheel.
type Scheduler struct {
	cat *catalog.Catalog
	log *slog.Logger

	mu      sync.Mutex
	wheel   [][]*entry
	entries map[string]*entry
	cursor  int       // current slot
	curTime time.Time // wheel time at cursor

	Out      chan Job // due checks (consumed by executor)
	Priority chan Job // check-now lane

	statDispatched uint64
	statLagMS      int64
}

// New builds an empty scheduler.
func New(cat *catalog.Catalog, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		cat:      cat,
		log:      log,
		wheel:    make([][]*entry, wheelSlots),
		entries:  map[string]*entry{},
		Out:      make(chan Job, 4096),
		Priority: make(chan Job, 256),
	}
}

// splay derives the deterministic start offset within the interval
// (SPEC §7.4).
func splay(objectID string, interval time.Duration) time.Duration {
	h := fnv.New64a()
	h.Write([]byte(objectID))
	if interval <= 0 {
		interval = time.Minute
	}
	return time.Duration(h.Sum64() % uint64(interval))
}

// Upsert (re)schedules an object according to its effective spec.
// Non-active classes (passive/agent) get freshness probes when
// stalenessAfter is set, else they are unscheduled.
func (s *Scheduler) Upsert(e *catalog.Entry) {
	id := e.Object.ID
	eff := e.Effective

	active := e.Class == model.CommandBuiltin || e.Class == model.CommandExec
	if b := eff.EnableChecks; b != nil && !*b {
		active = false
	}
	interval := eff.Interval.D()
	if !active {
		if eff.StalenessAfter > 0 {
			interval = eff.StalenessAfter.D() // freshness probe cadence
		} else {
			s.Remove(id)
			return
		}
	}
	interval = interval.Truncate(time.Second)
	if interval < time.Second {
		interval = time.Second
	}
	if interval > 24*time.Hour {
		interval = 24 * time.Hour
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if old := s.entries[id]; old != nil {
		s.removeLocked(old)
	}
	// First due: align to splay grid — the next grid point ≥ now.
	// Due times are always whole seconds (the wheel's resolution);
	// fractional dues would defer entries by a full revolution.
	off := splay(id, interval).Truncate(time.Second)
	gridStart := now.Truncate(interval).Add(off).Truncate(time.Second)
	for gridStart.Before(now) {
		gridStart = gridStart.Add(interval)
	}
	en := &entry{objectID: id, interval: interval, due: gridStart.Truncate(time.Second)}
	s.insertLocked(en)
	s.entries[id] = en
}

// Remove unschedules an object.
func (s *Scheduler) Remove(objectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if en := s.entries[objectID]; en != nil {
		s.removeLocked(en)
		delete(s.entries, objectID)
	}
}

func (s *Scheduler) insertLocked(en *entry) {
	slot := int(en.due.Unix()) % wheelSlots
	en.slot = slot
	s.wheel[slot] = append(s.wheel[slot], en)
}

func (s *Scheduler) removeLocked(en *entry) {
	slot := s.wheel[en.slot]
	for i, x := range slot {
		if x == en {
			s.wheel[en.slot] = append(slot[:i], slot[i+1:]...)
			return
		}
	}
}

// CheckNow pushes an immediate run on the priority lane.
func (s *Scheduler) CheckNow(objectID string) {
	select {
	case s.Priority <- Job{ObjectID: objectID, Planned: time.Now(), Priority: true}:
	default:
		// lane full: fall back to normal queue, blocking is fine here
		s.Out <- Job{ObjectID: objectID, Planned: time.Now(), Priority: true}
	}
}

// Reschedule arms the next regular run (called by the pipeline after a
// result arrives for passive freshness re-arm).
func (s *Scheduler) Reschedule(objectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	en := s.entries[objectID]
	if en == nil {
		return
	}
	s.removeLocked(en)
	en.due = time.Now().Add(en.interval).Truncate(time.Second)
	s.insertLocked(en)
}

// NextDue exposes the planned next run (UI detail).
func (s *Scheduler) NextDue(objectID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if en := s.entries[objectID]; en != nil {
		return en.due, true
	}
	return time.Time{}, false
}

// Run ticks the wheel until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	s.mu.Lock()
	s.curTime = time.Now().Truncate(time.Second)
	s.cursor = int(s.curTime.Unix()) % wheelSlots
	s.mu.Unlock()

	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.advance(time.Now())
		}
	}
}

// advance processes all slots between curTime and now.
func (s *Scheduler) advance(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.curTime.Add(time.Second).After(now) {
		s.curTime = s.curTime.Add(time.Second)
		s.cursor = int(s.curTime.Unix()) % wheelSlots
		slot := s.wheel[s.cursor]
		if len(slot) == 0 {
			continue
		}
		var stay []*entry
		for _, en := range slot {
			if en.due.After(s.curTime) {
				stay = append(stay, en) // future revolution
				continue
			}
			lag := now.Sub(en.due)
			if lag.Milliseconds() > s.statLagMS {
				s.statLagMS = lag.Milliseconds()
			}
			job := Job{ObjectID: en.objectID, Planned: en.due}
			select {
			case s.Out <- job:
				s.statDispatched++
			default:
				// queue full — executor saturated; postpone by one
				// second rather than blocking the wheel
				en.due = en.due.Add(time.Second)
				stay = append(stay, en)
				continue
			}
			// drift-free reschedule from planned time
			en.due = en.due.Add(en.interval)
			for en.due.Before(s.curTime) { // catch up after long stalls
				en.due = en.due.Add(en.interval)
			}
			newSlot := int(en.due.Unix()) % wheelSlots
			if newSlot == s.cursor {
				stay = append(stay, en)
			} else {
				en.slot = newSlot
				s.wheel[newSlot] = append(s.wheel[newSlot], en)
			}
		}
		s.wheel[s.cursor] = stay
	}
}

// Stats for self-metrics (SPEC §15.4: scheduler lag, queue depths).
type Stats struct {
	Scheduled  int    `json:"scheduled"`
	QueueDepth int    `json:"queueDepth"`
	Dispatched uint64 `json:"dispatched"`
	MaxLagMS   int64  `json:"maxLagMs"`
}

// Stats snapshots counters (max lag resets on read).
func (s *Scheduler) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Scheduled: len(s.entries), QueueDepth: len(s.Out),
		Dispatched: s.statDispatched, MaxLagMS: s.statLagMS}
	s.statLagMS = 0
	return st
}
