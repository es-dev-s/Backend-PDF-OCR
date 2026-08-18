package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"ocr-backend/internal/redisx"
	"ocr-backend/internal/retry"
)

const Channel = "ocr.events"

type Event struct {
	Origin string          `json:"origin"`
	Type   string          `json:"type"`
	At     time.Time       `json:"at"`
	Data   json.RawMessage `json:"data,omitempty"`
}

type Hub struct {
	id   string
	log  *slog.Logger
	rdb  *redisx.Client
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
}

func NewHub(rdb *redisx.Client, log *slog.Logger) *Hub {
	return &Hub{
		id:   uuid.NewString(),
		log:  log,
		rdb:  rdb,
		subs: make(map[chan []byte]struct{}),
	}
}

func (h *Hub) ID() string { return h.id }

func (h *Hub) Subscribe(buf int) (<-chan []byte, func()) {
	if buf < 1 {
		buf = 32
	}
	ch := make(chan []byte, buf)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
		// Never close(ch): a concurrent broadcast could send on a closed channel.
	}
}

func sendLatest(ch chan []byte, raw []byte) {
	select {
	case ch <- raw:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- raw:
	default:
	}
}

func (h *Hub) Publish(ctx context.Context, eventType string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		h.log.Error("event marshal", "err", err)
		return
	}
	event := Event{
		Origin: h.id,
		Type:   eventType,
		At:     time.Now().UTC(),
		Data:   payload,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		h.log.Error("event marshal", "err", err)
		return
	}
	h.broadcast(raw)
	rdb := h.rdb.Handle()
	if rdb == nil {
		return
	}
	pubCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := rdb.Publish(pubCtx, Channel, raw).Err(); err != nil {
		h.log.Warn("redis publish failed", "err", err)
	}
}

func (h *Hub) ListenRedis(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		rdb := h.rdb.Handle()
		if rdb == nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry.Jitter(backoff, 10*time.Second)):
			}
			continue
		}
		if err := h.consume(ctx, rdb); err != nil && ctx.Err() == nil {
			h.log.Warn("redis pubsub dropped", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry.Jitter(backoff, 10*time.Second)):
		}
		if backoff < 10*time.Second {
			backoff *= 2
		} else {
			backoff = time.Second
		}
	}
}

func (h *Hub) consume(ctx context.Context, rdb *redis.Client) error {
	sub := rdb.Subscribe(ctx, Channel)
	defer func() { _ = sub.Close() }()
	if _, err := sub.Receive(ctx); err != nil {
		return err
	}
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var event Event
			if err := json.Unmarshal([]byte(msg.Payload), &event); err != nil {
				continue
			}
			if event.Origin == h.id {
				continue
			}
			h.broadcast([]byte(msg.Payload))
		}
	}
}

func (h *Hub) broadcast(raw []byte) {
	h.mu.RLock()
	subs := make([]chan []byte, 0, len(h.subs))
	for ch := range h.subs {
		subs = append(subs, ch)
	}
	h.mu.RUnlock()
	for _, ch := range subs {
		sendLatest(ch, raw)
	}
}
