package eventbus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testSource is a simple EventSource for testing.
type testSource struct {
	name   string
	events []Event
	stopCh chan struct{}
}

func newTestSource(name string, events ...Event) *testSource {
	return &testSource{
		name:   name,
		events: events,
		stopCh: make(chan struct{}),
	}
}

func (s *testSource) Name() string { return s.name }

func (s *testSource) Start(out chan<- Event) error {
	for _, e := range s.events {
		e.Source = s.name
		if e.Timestamp.IsZero() {
			e.Timestamp = time.Now()
		}
		if e.ID == "" {
			e.ID = generateID()
		}
		select {
		case out <- e:
		case <-s.stopCh:
			return nil
		}
	}
	<-s.stopCh
	return nil
}

func (s *testSource) Stop() error {
	close(s.stopCh)
	return nil
}

func TestBusPublishAndSubscribe(t *testing.T) {
	bus := New(WithWorkers(1), WithQueueSize(16))

	var received atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2) // expect 2 message events

	bus.On(EventMessage, func(e Event) error {
		received.Add(1)
		wg.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)

	bus.Publish(Event{Source: "test", Type: EventMessage, Payload: "hello"})
	bus.Publish(Event{Source: "test", Type: EventMessage, Payload: "world"})
	bus.Publish(Event{Source: "test", Type: EventEmail, Payload: "should not match"})

	wg.Wait()

	if got := received.Load(); got != 2 {
		t.Errorf("expected 2 message events handled, got %d", got)
	}

	stats := bus.GetStats()
	if stats.EventsReceived != 3 {
		t.Errorf("expected 3 events received, got %d", stats.EventsReceived)
	}

	bus.Stop()
}

func TestBusCatchAll(t *testing.T) {
	bus := New(WithWorkers(1), WithQueueSize(16))

	var received atomic.Int64
	var wg sync.WaitGroup
	wg.Add(3)

	bus.OnAny(func(e Event) error {
		received.Add(1)
		wg.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)

	bus.Publish(Event{Source: "a", Type: EventMessage, Payload: "1"})
	bus.Publish(Event{Source: "b", Type: EventEmail, Payload: "2"})
	bus.Publish(Event{Source: "c", Type: EventWebhook, Payload: "3"})

	wg.Wait()

	if got := received.Load(); got != 3 {
		t.Errorf("expected 3 events, got %d", got)
	}

	bus.Stop()
}

func TestBusWithSource(t *testing.T) {
	bus := New(WithWorkers(1), WithQueueSize(16))

	var received atomic.Int64
	var wg sync.WaitGroup
	wg.Add(2)

	bus.On(EventMessage, func(e Event) error {
		received.Add(1)
		wg.Done()
		return nil
	})

	src := newTestSource("test-source",
		Event{Type: EventMessage, Payload: "msg1"},
		Event{Type: EventMessage, Payload: "msg2"},
	)

	if err := bus.RegisterSource(src); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)

	wg.Wait()

	if got := received.Load(); got != 2 {
		t.Errorf("expected 2 events from source, got %d", got)
	}

	bus.Stop()
}

func TestBusDuplicateSource(t *testing.T) {
	bus := New()

	src1 := newTestSource("dup")
	src2 := newTestSource("dup")

	if err := bus.RegisterSource(src1); err != nil {
		t.Fatal("first register should succeed:", err)
	}
	if err := bus.RegisterSource(src2); err == nil {
		t.Fatal("second register with same name should fail")
	}
}

func TestBusBackpressure(t *testing.T) {
	bus := New(WithWorkers(0), WithQueueSize(2)) // 0 workers = nobody reading
	bus.events = make(chan Event, 2)

	// Fill the queue
	bus.Publish(Event{Source: "a", Type: EventMessage, Payload: "1"})
	bus.Publish(Event{Source: "a", Type: EventMessage, Payload: "2"})

	// This should be dropped
	ok := bus.Publish(Event{Source: "a", Type: EventMessage, Payload: "3"})
	if ok {
		t.Error("expected event to be dropped due to backpressure")
	}

	stats := bus.GetStats()
	if stats.EventsDropped != 1 {
		t.Errorf("expected 1 dropped event, got %d", stats.EventsDropped)
	}
}

func TestBusStats(t *testing.T) {
	bus := New(WithWorkers(1), WithQueueSize(16))

	var wg sync.WaitGroup
	wg.Add(2)
	bus.On(EventMessage, func(e Event) error {
		wg.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus.Start(ctx)

	bus.Publish(Event{Source: "whatsapp", Type: EventMessage, Payload: "hi"})
	bus.Publish(Event{Source: "telegram", Type: EventMessage, Payload: "hey"})

	wg.Wait()
	// Small delay for stats to update
	time.Sleep(10 * time.Millisecond)

	stats := bus.GetStats()
	if stats.EventsReceived != 2 {
		t.Errorf("received: got %d, want 2", stats.EventsReceived)
	}
	if stats.BySource["whatsapp"] != 1 || stats.BySource["telegram"] != 1 {
		t.Errorf("by_source: %v", stats.BySource)
	}
	if stats.ByType["message"] != 2 {
		t.Errorf("by_type: %v", stats.ByType)
	}

	bus.Stop()
}
