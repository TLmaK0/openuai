package eventbus

import (
	"context"
	"fmt"
	"sync"
	"time"

	"openuai/internal/logger"
)

const (
	defaultQueueSize = 256  // buffered channel size for backpressure
	defaultWorkers   = 4    // concurrent event processors
)

// Bus is the central event bus that routes events from sources to handlers.
type Bus struct {
	mu       sync.RWMutex
	handlers map[EventType][]Handler // handlers per event type
	catchAll []Handler               // handlers for all events

	sources  map[string]EventSource
	events   chan Event
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	queueSize int
	workers   int

	stats Stats
}

// Stats tracks event bus metrics.
type Stats struct {
	mu             sync.Mutex
	EventsReceived int64            `json:"events_received"`
	EventsHandled  int64            `json:"events_handled"`
	EventsDropped  int64            `json:"events_dropped"`
	Errors         int64            `json:"errors"`
	BySource       map[string]int64 `json:"by_source"`
	ByType         map[string]int64 `json:"by_type"`
}

// Option configures the event bus.
type Option func(*Bus)

// WithQueueSize sets the internal event queue size (backpressure threshold).
func WithQueueSize(size int) Option {
	return func(b *Bus) {
		b.queueSize = size
	}
}

// WithWorkers sets the number of concurrent event processing goroutines.
func WithWorkers(n int) Option {
	return func(b *Bus) {
		b.workers = n
	}
}

// New creates a new event bus.
func New(opts ...Option) *Bus {
	b := &Bus{
		handlers:  make(map[EventType][]Handler),
		sources:   make(map[string]EventSource),
		queueSize: defaultQueueSize,
		workers:   defaultWorkers,
		stats: Stats{
			BySource: make(map[string]int64),
			ByType:   make(map[string]int64),
		},
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

// On registers a handler for a specific event type.
func (b *Bus) On(eventType EventType, handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}

// OnAny registers a handler that receives all events.
func (b *Bus) OnAny(handler Handler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.catchAll = append(b.catchAll, handler)
}

// RegisterSource adds an event source to the bus.
// If the bus is already running, the source is started immediately.
func (b *Bus) RegisterSource(source EventSource) error {
	b.mu.Lock()
	name := source.Name()
	if _, exists := b.sources[name]; exists {
		b.mu.Unlock()
		return fmt.Errorf("source already registered: %s", name)
	}
	b.sources[name] = source
	running := b.events != nil // bus is running if events channel exists
	b.mu.Unlock()

	logger.Info("EventBus: registered source %q", name)

	if running {
		b.startSource(source)
	}
	return nil
}

// UnregisterSource removes and stops an event source.
func (b *Bus) UnregisterSource(name string) error {
	b.mu.Lock()
	source, exists := b.sources[name]
	if !exists {
		b.mu.Unlock()
		return fmt.Errorf("source not found: %s", name)
	}
	delete(b.sources, name)
	b.mu.Unlock()

	logger.Info("EventBus: unregistering source %q", name)
	return source.Stop()
}

// Start initializes the event bus: starts worker goroutines and all registered sources.
func (b *Bus) Start(ctx context.Context) {
	b.ctx, b.cancel = context.WithCancel(ctx)
	b.events = make(chan Event, b.queueSize)

	// Start worker pool
	for i := 0; i < b.workers; i++ {
		b.wg.Add(1)
		go b.worker(i)
	}
	logger.Info("EventBus: started with %d workers, queue size %d", b.workers, b.queueSize)

	// Start all registered sources
	b.mu.RLock()
	for _, source := range b.sources {
		b.startSource(source)
	}
	b.mu.RUnlock()
}

// Stop gracefully shuts down the event bus and all sources.
func (b *Bus) Stop() {
	logger.Info("EventBus: stopping...")

	// Stop all sources
	b.mu.RLock()
	for name, source := range b.sources {
		logger.Info("EventBus: stopping source %q", name)
		if err := source.Stop(); err != nil {
			logger.Error("EventBus: error stopping source %q: %s", name, err.Error())
		}
	}
	b.mu.RUnlock()

	// Cancel context and drain
	b.cancel()
	close(b.events)
	b.wg.Wait()

	logger.Info("EventBus: stopped")
}

// Publish sends an event directly into the bus (useful for programmatic events).
func (b *Bus) Publish(event Event) bool {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.ID == "" {
		event.ID = generateID()
	}

	select {
	case b.events <- event:
		b.stats.mu.Lock()
		b.stats.EventsReceived++
		b.stats.BySource[event.Source]++
		b.stats.ByType[string(event.Type)]++
		b.stats.mu.Unlock()
		return true
	default:
		// Queue full — backpressure: drop event
		b.stats.mu.Lock()
		b.stats.EventsDropped++
		b.stats.mu.Unlock()
		logger.Error("EventBus: queue full, dropped event from %q type=%s", event.Source, event.Type)
		return false
	}
}

// GetStats returns a snapshot of bus statistics.
func (b *Bus) GetStats() Stats {
	b.stats.mu.Lock()
	defer b.stats.mu.Unlock()
	// Copy maps
	bySource := make(map[string]int64, len(b.stats.BySource))
	for k, v := range b.stats.BySource {
		bySource[k] = v
	}
	byType := make(map[string]int64, len(b.stats.ByType))
	for k, v := range b.stats.ByType {
		byType[k] = v
	}
	return Stats{
		EventsReceived: b.stats.EventsReceived,
		EventsHandled:  b.stats.EventsHandled,
		EventsDropped:  b.stats.EventsDropped,
		Errors:         b.stats.Errors,
		BySource:       bySource,
		ByType:         byType,
	}
}

// Sources returns the names of all registered sources.
func (b *Bus) Sources() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	names := make([]string, 0, len(b.sources))
	for name := range b.sources {
		names = append(names, name)
	}
	return names
}

// startSource launches a goroutine that runs the source and feeds events into the bus.
func (b *Bus) startSource(source EventSource) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		name := source.Name()
		logger.Info("EventBus: starting source %q", name)
		if err := source.Start(b.events); err != nil {
			logger.Error("EventBus: source %q exited with error: %s", name, err.Error())
		} else {
			logger.Info("EventBus: source %q stopped", name)
		}
	}()
}

// worker processes events from the queue and dispatches to handlers.
func (b *Bus) worker(id int) {
	defer b.wg.Done()
	logger.Debug("EventBus: worker %d started", id)

	for event := range b.events {
		b.dispatch(event)
	}

	logger.Debug("EventBus: worker %d stopped", id)
}

// dispatch sends an event to all matching handlers.
func (b *Bus) dispatch(event Event) {
	b.mu.RLock()
	typeHandlers := b.handlers[event.Type]
	catchAll := b.catchAll
	b.mu.RUnlock()

	for _, h := range typeHandlers {
		if err := h(event); err != nil {
			b.stats.mu.Lock()
			b.stats.Errors++
			b.stats.mu.Unlock()
			logger.Error("EventBus: handler error for %s/%s: %s", event.Source, event.Type, err.Error())
		} else {
			b.stats.mu.Lock()
			b.stats.EventsHandled++
			b.stats.mu.Unlock()
		}
	}

	for _, h := range catchAll {
		if err := h(event); err != nil {
			b.stats.mu.Lock()
			b.stats.Errors++
			b.stats.mu.Unlock()
			logger.Error("EventBus: catch-all handler error for %s/%s: %s", event.Source, event.Type, err.Error())
		} else {
			b.stats.mu.Lock()
			b.stats.EventsHandled++
			b.stats.mu.Unlock()
		}
	}
}

// generateID creates a simple unique event ID.
func generateID() string {
	return fmt.Sprintf("evt_%d", time.Now().UnixNano())
}
