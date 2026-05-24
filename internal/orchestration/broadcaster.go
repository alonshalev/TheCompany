// Package orchestration — SSE event broadcaster.
//
// Broadcaster maintains a fan-out map of run ID → subscriber channels.
// The orchestration engine publishes SSEEvents; the SSE HTTP handler
// subscribes and streams them to the client.
package orchestration

import (
	"sync"

	"github.com/google/uuid"
)

// SSEEvent is a single server-sent event emitted during a run.
type SSEEvent struct {
	Type  string         `json:"type"`
	RunID uuid.UUID      `json:"run_id"`
	Data  map[string]any `json:"data,omitempty"`
}

// Broadcaster fan-outs SSEEvents to all subscribers for a given run.
type Broadcaster struct {
	mu   sync.RWMutex
	subs map[uuid.UUID][]chan SSEEvent
}

// NewBroadcaster creates a Broadcaster.
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[uuid.UUID][]chan SSEEvent)}
}

// Subscribe returns a channel that receives events for runID.
// Call Unsubscribe(runID, ch) when done.
func (b *Broadcaster) Subscribe(runID uuid.UUID) chan SSEEvent {
	ch := make(chan SSEEvent, 64)
	b.mu.Lock()
	b.subs[runID] = append(b.subs[runID], ch)
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel for runID.
func (b *Broadcaster) Unsubscribe(runID uuid.UUID, ch chan SSEEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := b.subs[runID]
	for i, c := range list {
		if c == ch {
			b.subs[runID] = append(list[:i], list[i+1:]...)
			close(ch)
			break
		}
	}
	if len(b.subs[runID]) == 0 {
		delete(b.subs, runID)
	}
}

// Publish sends an event to all subscribers for runID (non-blocking).
func (b *Broadcaster) Publish(runID uuid.UUID, event SSEEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs[runID] {
		select {
		case ch <- event:
		default:
			// Subscriber is slow — drop the event rather than block the engine.
		}
	}
}
