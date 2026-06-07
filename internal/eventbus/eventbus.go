// Package eventbus provides the typed in-memory queues between
// subsystems (SPEC §7.2) with the defined backpressure policy:
// check results > events > notifications > AI jobs — under overload AI
// jobs are dropped first, check processing never.
package eventbus

import (
	"sync"
	"sync/atomic"

	"github.com/northplane/northplane/internal/model"
)

// Bus wires producers and consumers.
type Bus struct {
	// Results: executor → pipeline. Bounded but blocking — check
	// processing is never dropped (SPEC §7.2).
	Results chan *model.CheckResult

	// Events: pipeline/ingress → alerting. Blocking, generously sized.
	Events chan *model.Event

	// Notifications: escalation → notifier. Blocking (durability comes
	// from the outbox; this only smooths bursts).
	Notifications chan string // outbox item IDs

	// AI: low-priority job hints (correlation, summaries). Dropped on
	// overload.
	AI chan AIJob

	subMu   sync.RWMutex
	subs    map[int]*Subscriber
	nextSub int

	droppedAI     atomic.Uint64
	droppedSubMsg atomic.Uint64
}

// AIJob is a deferred AI task hint.
type AIJob struct {
	Kind       string // "incident-summary" | "anomaly-explain" | …
	TenantID   string
	IncidentID string
	AlertIDs   []string
}

// Subscriber receives the live event stream (SSE hub, correlation…).
// Slow subscribers lose messages and observe Resync (SPEC §7.6 drop
// policy: resync hint instead of unbounded buffering).
type Subscriber struct {
	C      chan *model.Event
	id     int
	bus    *Bus
	resync atomic.Bool
}

// NeedsResync reports and clears the overflow flag.
func (s *Subscriber) NeedsResync() bool { return s.resync.Swap(false) }

// Close unsubscribes.
func (s *Subscriber) Close() {
	s.bus.subMu.Lock()
	delete(s.bus.subs, s.id)
	s.bus.subMu.Unlock()
}

// New sizes the queues for the G1 profile.
func New() *Bus {
	return &Bus{
		Results:       make(chan *model.CheckResult, 8192),
		Events:        make(chan *model.Event, 16384),
		Notifications: make(chan string, 4096),
		AI:            make(chan AIJob, 256),
		subs:          map[int]*Subscriber{},
	}
}

// Subscribe attaches a live event consumer with its own buffer.
func (b *Bus) Subscribe(buffer int) *Subscriber {
	if buffer <= 0 {
		buffer = 256
	}
	b.subMu.Lock()
	defer b.subMu.Unlock()
	b.nextSub++
	s := &Subscriber{C: make(chan *model.Event, buffer), id: b.nextSub, bus: b}
	b.subs[s.id] = s
	return s
}

// PublishEvent fans an event out to the alerting queue and subscribers.
func (b *Bus) PublishEvent(e *model.Event) {
	b.Events <- e // blocking: events are not droppable
	b.fanout(e)
}

// FanoutOnly distributes to subscribers without alerting (used for
// events that already passed the alerting stage).
func (b *Bus) FanoutOnly(e *model.Event) { b.fanout(e) }

func (b *Bus) fanout(e *model.Event) {
	b.subMu.RLock()
	defer b.subMu.RUnlock()
	for _, s := range b.subs {
		select {
		case s.C <- e:
		default:
			s.resync.Store(true) // slow client: drop + resync hint
			b.droppedSubMsg.Add(1)
		}
	}
}

// TryAI enqueues an AI job, dropping under load (SPEC §7.2).
func (b *Bus) TryAI(job AIJob) bool {
	select {
	case b.AI <- job:
		return true
	default:
		b.droppedAI.Add(1)
		return false
	}
}

// Stats for self-metrics.
type Stats struct {
	ResultsDepth  int    `json:"resultsDepth"`
	EventsDepth   int    `json:"eventsDepth"`
	NotifyDepth   int    `json:"notifyDepth"`
	AIDepth       int    `json:"aiDepth"`
	Subscribers   int    `json:"subscribers"`
	DroppedAI     uint64 `json:"droppedAi"`
	DroppedSubMsg uint64 `json:"droppedSubscriberMessages"`
}

// Stats snapshots queue depths.
func (b *Bus) Stats() Stats {
	b.subMu.RLock()
	subs := len(b.subs)
	b.subMu.RUnlock()
	return Stats{
		ResultsDepth: len(b.Results), EventsDepth: len(b.Events),
		NotifyDepth: len(b.Notifications), AIDepth: len(b.AI),
		Subscribers: subs, DroppedAI: b.droppedAI.Load(),
		DroppedSubMsg: b.droppedSubMsg.Load(),
	}
}
