package eventbus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/northplane/northplane/internal/model"
)

// recvTimeout is the generous deadline used to turn an inherently async
// delivery into a deterministic assertion without sleeping.
const recvTimeout = 2 * time.Second

// recvEvent blocks until an event arrives on c or the deadline fires, in
// which case it fails the test. Used instead of time.Sleep so the tests
// stay race-clean and fast.
func recvEvent(t *testing.T, c <-chan *model.Event) *model.Event {
	t.Helper()
	select {
	case e := <-c:
		return e
	case <-time.After(recvTimeout):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func ev(id string) *model.Event { return &model.Event{ID: id, Type: model.EventType("test")} }

// TestPublishEventDeliversToAlertingAndSubscribers verifies a published
// event reaches both the alerting queue (Events) and every live
// subscriber's channel.
func TestPublishEventDeliversToAlertingAndSubscribers(t *testing.T) {
	b := New()
	s1 := b.Subscribe(4)
	defer s1.Close()
	s2 := b.Subscribe(4)
	defer s2.Close()

	e := ev("e1")
	b.PublishEvent(e)

	// Alerting queue received it.
	select {
	case got := <-b.Events:
		if got != e {
			t.Fatalf("Events queue got %+v, want %+v", got, e)
		}
	case <-time.After(recvTimeout):
		t.Fatal("alerting queue did not receive event")
	}

	if got := recvEvent(t, s1.C); got != e {
		t.Fatalf("s1 got %+v, want %+v", got, e)
	}
	if got := recvEvent(t, s2.C); got != e {
		t.Fatalf("s2 got %+v, want %+v", got, e)
	}
}

// TestPublishEventCtxAbandonsOnCancel proves the request-path publish
// gives up when the alerting queue is full and ctx is cancelled, returning
// ctx.Err() instead of blocking forever.
func TestPublishEventCtxAbandonsOnCancel(t *testing.T) {
	b := New()
	// Saturate the Events channel so the next send must block.
	for len(b.Events) < cap(b.Events) {
		b.Events <- ev("filler")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled → select takes the ctx.Done branch

	if err := b.PublishEventCtx(ctx, ev("x")); err != context.Canceled {
		t.Fatalf("PublishEventCtx err = %v, want context.Canceled", err)
	}
}

// TestPublishEventCtxSucceedsWhenSpace confirms the happy path also fans
// out to subscribers and returns nil.
func TestPublishEventCtxSucceedsWhenSpace(t *testing.T) {
	b := New()
	s := b.Subscribe(4)
	defer s.Close()

	e := ev("ok")
	if err := b.PublishEventCtx(context.Background(), e); err != nil {
		t.Fatalf("PublishEventCtx err = %v, want nil", err)
	}
	if got := <-b.Events; got != e {
		t.Fatalf("Events got %+v, want %+v", got, e)
	}
	if got := recvEvent(t, s.C); got != e {
		t.Fatalf("subscriber got %+v, want %+v", got, e)
	}
}

// TestFanoutOnlySkipsAlerting verifies FanoutOnly distributes to
// subscribers without touching the alerting queue.
func TestFanoutOnlySkipsAlerting(t *testing.T) {
	b := New()
	s := b.Subscribe(4)
	defer s.Close()

	e := ev("f1")
	b.FanoutOnly(e)

	if got := recvEvent(t, s.C); got != e {
		t.Fatalf("subscriber got %+v, want %+v", got, e)
	}
	if len(b.Events) != 0 {
		t.Fatalf("FanoutOnly must not enqueue to Events, depth = %d", len(b.Events))
	}
}

// TestSubscriberBackpressureDropAndResync exercises the documented SPEC
// §7.6 drop policy: when a slow subscriber's buffer is full, further
// fanout drops the message, sets the resync hint and bumps the drop
// counter — it never blocks the publisher.
func TestSubscriberBackpressureDropAndResync(t *testing.T) {
	b := New()
	const buf = 2
	s := b.Subscribe(buf)
	defer s.Close()

	// Fill the subscriber buffer exactly.
	for i := 0; i < buf; i++ {
		b.FanoutOnly(ev("filler"))
	}
	if s.NeedsResync() {
		t.Fatal("no resync expected while buffer not yet overflowed")
	}

	// These overflow and must be dropped.
	const overflow = 5
	for i := 0; i < overflow; i++ {
		b.FanoutOnly(ev("dropped"))
	}

	if !s.NeedsResync() {
		t.Fatal("overflow must set the resync hint")
	}
	// NeedsResync swaps to false; calling again is now clear.
	if s.NeedsResync() {
		t.Fatal("NeedsResync must clear the flag after reading it")
	}

	if got := b.Stats().DroppedSubMsg; got != overflow {
		t.Fatalf("DroppedSubMsg = %d, want %d", got, overflow)
	}

	// The publisher was never blocked: exactly buf messages are buffered.
	if got := len(s.C); got != buf {
		t.Fatalf("subscriber buffer depth = %d, want %d", got, buf)
	}
}

// TestTryAIDropsUnderLoad covers the AI queue drop policy (SPEC §7.2):
// TryAI returns true while there is room and false (incrementing the drop
// counter) once the queue is full, without blocking.
func TestTryAIDropsUnderLoad(t *testing.T) {
	b := New()
	capacity := cap(b.AI)

	for i := 0; i < capacity; i++ {
		if !b.TryAI(AIJob{Kind: "incident-summary"}) {
			t.Fatalf("TryAI returned false before queue full (i=%d)", i)
		}
	}
	// Now full: subsequent enqueues are dropped.
	const extra = 3
	for i := 0; i < extra; i++ {
		if b.TryAI(AIJob{Kind: "anomaly-explain"}) {
			t.Fatalf("TryAI returned true on full queue (i=%d)", i)
		}
	}

	st := b.Stats()
	if st.DroppedAI != extra {
		t.Fatalf("DroppedAI = %d, want %d", st.DroppedAI, extra)
	}
	if st.AIDepth != capacity {
		t.Fatalf("AIDepth = %d, want %d", st.AIDepth, capacity)
	}
}

// TestUnsubscribeStopsDelivery confirms a Closed subscriber receives no
// further events and that the subscriber count in Stats reflects the
// lifecycle.
func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := New()
	s := b.Subscribe(4)

	if got := b.Stats().Subscribers; got != 1 {
		t.Fatalf("Subscribers = %d, want 1", got)
	}

	s.Close()

	if got := b.Stats().Subscribers; got != 0 {
		t.Fatalf("after Close Subscribers = %d, want 0", got)
	}

	// Fanout after Close must not deliver and must not panic.
	b.FanoutOnly(ev("after-close"))
	select {
	case e := <-s.C:
		t.Fatalf("unsubscribed channel received %+v", e)
	default:
	}
}

// TestDoubleCloseNoPanic ensures the unsubscribe path is idempotent.
func TestDoubleCloseNoPanic(t *testing.T) {
	b := New()
	s := b.Subscribe(1)
	s.Close()
	s.Close() // must not panic on double-unsubscribe
	if got := b.Stats().Subscribers; got != 0 {
		t.Fatalf("Subscribers = %d, want 0", got)
	}
}

// TestSubscribeDefaultBuffer verifies the non-positive buffer falls back
// to the documented default capacity.
func TestSubscribeDefaultBuffer(t *testing.T) {
	b := New()
	for _, n := range []int{0, -1} {
		s := b.Subscribe(n)
		if got := cap(s.C); got != 256 {
			t.Fatalf("Subscribe(%d) buffer cap = %d, want 256", n, got)
		}
		s.Close()
	}
}

// TestStatsDepths sanity-checks that Stats reports live queue depths.
func TestStatsDepths(t *testing.T) {
	b := New()
	b.Results <- &model.CheckResult{}
	b.Results <- &model.CheckResult{}
	b.Notifications <- "outbox-1"

	st := b.Stats()
	if st.ResultsDepth != 2 {
		t.Fatalf("ResultsDepth = %d, want 2", st.ResultsDepth)
	}
	if st.NotifyDepth != 1 {
		t.Fatalf("NotifyDepth = %d, want 1", st.NotifyDepth)
	}
	if st.EventsDepth != 0 || st.AIDepth != 0 {
		t.Fatalf("unexpected non-zero depth: %+v", st)
	}
}

// TestConcurrentFanoutRaceClean stresses the bus with many concurrent
// publishers and a draining subscriber plus concurrent Subscribe/Close
// churn and Stats reads. Synchronisation uses WaitGroups and channels —
// no sleeps — so it is meaningful under -race. The assertion is on
// conservation: every fanned-out event is either delivered or counted as
// dropped, and the bus never panics or deadlocks.
func TestConcurrentFanoutRaceClean(t *testing.T) {
	b := New()

	const (
		publishers = 8
		perPub     = 500
	)
	total := publishers * perPub

	// A subscriber with a big buffer drains continuously so we can count
	// delivered events; combined with DroppedSubMsg this lets us assert
	// conservation.
	main := b.Subscribe(total + 8)

	var delivered int64
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range main.C {
			delivered++
			if delivered >= int64(total) {
				return
			}
		}
	}()

	var pubWG sync.WaitGroup
	pubWG.Add(publishers)
	for p := 0; p < publishers; p++ {
		go func() {
			defer pubWG.Done()
			for i := 0; i < perPub; i++ {
				b.FanoutOnly(ev("c"))
			}
		}()
	}

	// Concurrent churn: subscribers coming and going while Stats is read.
	var churnWG sync.WaitGroup
	stop := make(chan struct{})
	churnWG.Add(2)
	go func() {
		defer churnWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s := b.Subscribe(1)
				_ = b.Stats()
				s.Close()
			}
		}
	}()
	go func() {
		defer churnWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = b.Stats()
			}
		}
	}()

	pubWG.Wait()

	// Wait for the drain to observe all delivered events, or fail on
	// timeout (the big buffer guarantees no drops on `main`, so all
	// `total` events must be delivered).
	select {
	case <-drainDone:
	case <-time.After(recvTimeout):
		t.Fatalf("drain timed out: delivered=%d dropped=%d want total=%d",
			delivered, b.Stats().DroppedSubMsg, total)
	}

	close(stop)
	churnWG.Wait()

	if delivered != int64(total) {
		t.Fatalf("delivered = %d, want %d (the wide-buffer subscriber must lose nothing)", delivered, total)
	}
}

// TestConcurrentTryAIDropAccounting runs many goroutines hammering TryAI
// on a small queue with no drainer; the number of successes must equal the
// queue capacity and successes+drops must equal the attempt count, with no
// lost updates under -race.
func TestConcurrentTryAIDropAccounting(t *testing.T) {
	b := New()
	capAI := cap(b.AI)

	const goroutines = 16
	const perG = 200
	attempts := goroutines * perG

	var succ int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			local := int64(0)
			for i := 0; i < perG; i++ {
				if b.TryAI(AIJob{Kind: "x"}) {
					local++
				}
			}
			mu.Lock()
			succ += local
			mu.Unlock()
		}()
	}
	wg.Wait()

	dropped := b.Stats().DroppedAI
	if succ != int64(capAI) {
		t.Fatalf("successful enqueues = %d, want capacity %d", succ, capAI)
	}
	if int64(dropped)+succ != int64(attempts) {
		t.Fatalf("succ(%d)+dropped(%d) = %d, want attempts %d",
			succ, dropped, int64(dropped)+succ, attempts)
	}
}
