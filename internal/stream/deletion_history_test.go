package stream

import (
	"context"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/store"
)

func TestCheckAbandonedRecordsDeletion(t *testing.T) {
	h, tor, _ := newAbandonEnv(t, 0.02, 0.05)

	h.checkAbandoned(tor)

	got, err := h.store.Deletions(context.Background())
	if err != nil {
		t.Fatalf("deletions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d deletion records, want 1", len(got))
	}
	d := got[0]
	if d.Reason != store.DeleteReasonAbandoned {
		t.Errorf("reason = %q, want %q", d.Reason, store.DeleteReasonAbandoned)
	}
	if d.Progress != 0.02 {
		t.Errorf("progress = %v, want 0.02", d.Progress)
	}
	if d.ProgressLimit != 0.05 {
		t.Errorf("progress limit = %v, want 0.05", d.ProgressLimit)
	}
}
