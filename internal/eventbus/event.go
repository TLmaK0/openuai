package eventbus

import "time"

// EventType identifies the kind of event.
type EventType string

const (
	EventMessage      EventType = "message"       // incoming message (WhatsApp, Telegram, etc.)
	EventEmail        EventType = "email"          // incoming email
	EventWebhook      EventType = "webhook"        // incoming webhook call
	EventSchedule     EventType = "schedule"       // cron/scheduled trigger
	EventFileChange   EventType = "file_change"    // filesystem watcher
	EventClipboard    EventType = "clipboard"      // clipboard change
	EventNotification EventType = "notification"   // system notification
	EventCustom       EventType = "custom"         // user-defined
)

// Event is the generic event passed through the bus.
type Event struct {
	ID        string            `json:"id"`
	Source    string            `json:"source"`    // source identifier (e.g. "whatsapp", "email", "webhook")
	Type      EventType         `json:"type"`
	Payload   string            `json:"payload"`   // main content (message body, email body, etc.)
	Metadata  map[string]string `json:"metadata"`  // extra fields (sender, subject, channel, etc.)
	Timestamp time.Time         `json:"timestamp"`
}

// EventSource is implemented by all connectors that produce events.
type EventSource interface {
	// Name returns the source identifier (e.g. "whatsapp", "email").
	Name() string

	// Start begins producing events, sending them to the provided channel.
	// It blocks until ctx is cancelled or an unrecoverable error occurs.
	Start(events chan<- Event) error

	// Stop gracefully shuts down the source.
	Stop() error
}

// Handler processes an event. Return an error to signal processing failure.
type Handler func(event Event) error
