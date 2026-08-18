package realtime

import (
	"log/slog"
	"testing"
)

func TestBroadcastKeepsLatestWhenSubscriberIsSlow(t *testing.T) {
	h := NewHub(nil, slog.Default())
	ch, cancel := h.Subscribe(1)
	defer cancel()

	h.broadcast([]byte("old"))
	h.broadcast([]byte("new"))

	select {
	case got := <-ch:
		if string(got) != "new" {
			t.Fatalf("got %q, want the latest event", got)
		}
	default:
		t.Fatal("subscriber received nothing")
	}
}
