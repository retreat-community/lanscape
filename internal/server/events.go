package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is a server-sent event.
type Event struct {
	Type string
	Data any
}

// Broker fans out events to SSE subscribers.
type Broker struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// NewBroker creates a broker.
func NewBroker() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Publish sends an event to all subscribers, dropping it for slow ones.
func (b *Broker) Publish(typ string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- Event{Type: typ, Data: data}:
		default:
		}
	}
}

func (b *Broker) subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *Broker) unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
}

// ServeHTTP streams events as text/event-stream.
func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := b.subscribe()
	defer b.unsubscribe(ch)
	_, _ = fmt.Fprint(w, "retry: 2000\n\n")
	fl.Flush()
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case ev := <-ch:
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			fl.Flush()
		}
	}
}
