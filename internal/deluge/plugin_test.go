package deluge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gdm85/go-rencode"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// pluginAPI extends fakeAPI with scriptable RPC responses.
type pluginAPI struct {
	*fakeAPI
	apiVersion      int
	prioritizeErr   error
	prioritizeFalse bool  // reply False: the plugin declined the window
	prioritizeArgs  []any // args of the last prioritize_range call
}

func (p *pluginAPI) RPC(_ context.Context, method string, args rencode.List, _ rencode.Dictionary) (rencode.List, error) {
	p.record("rpc %s", method)
	switch method {
	case "seedstream.api_version":
		return rencode.NewList(int64(p.apiVersion)), nil
	case "seedstream.prioritize_range":
		p.prioritizeArgs = args.Values()
		if p.prioritizeErr != nil {
			return rencode.List{}, p.prioritizeErr
		}
		if p.prioritizeFalse {
			return rencode.NewList(false), nil
		}
		return rencode.NewList(true), nil
	}
	return rencode.List{}, delugerpc.RPCError{ExceptionType: "AttributeError", ExceptionMessage: "unknown method"}
}

func newPluginClient(p *pluginAPI, at *time.Time) *client {
	return &client{
		rpc:       p,
		label:     "seedstrem",
		flagCache: map[string]flags{},
		now:       func() time.Time { return *at },
	}
}

func countRPC(f *fakeAPI, method string) int {
	n := 0
	for _, c := range f.calls {
		if strings.Contains(c, method) {
			n++
		}
	}
	return n
}

func TestPrioritizePiecesWithoutPlugin(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI()} // no plugins enabled
	c := newPluginClient(p, &now)

	err := c.PrioritizePieces(context.Background(), "abc123", 10, 20)
	if !errors.Is(err, downloader.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
	// The negative probe is cached: an immediate second call must not
	// re-list plugins.
	_ = c.PrioritizePieces(context.Background(), "abc123", 10, 20)
	if n := countRPC(p.fakeAPI, "api_version"); n != 0 {
		t.Errorf("api_version probed %d times without the plugin in the list", n)
	}

	// Past the TTL, enabling the plugin is picked up.
	p.plugins = []string{"Label", "Seedstream"}
	p.apiVersion = 1
	now = now.Add(pluginProbeTTL + time.Second)
	if err := c.PrioritizePieces(context.Background(), "abc123", 10, 20); err != nil {
		t.Fatalf("err = %v after enabling plugin", err)
	}
	if n := countRPC(p.fakeAPI, "prioritize_range"); n != 1 {
		t.Errorf("prioritize_range called %d times, want 1", n)
	}
}

func TestPrioritizePiecesDeclinedReportsHintDeclined(t *testing.T) {
	// A declined window (typically "torrent not registered yet", the
	// answer during the first moments after an add) must be reported as
	// ErrHintDeclined, not as a delivered hint — the caller retries on
	// its next poll instead of waiting out the re-hint interval. It is
	// not ErrNotSupported: the backend is capable, this call just missed.
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI(), apiVersion: 1, prioritizeFalse: true}
	p.plugins = []string{"Seedstream"}
	c := newPluginClient(p, &now)

	err := c.PrioritizePieces(context.Background(), "abc123", 10, 20)
	if !errors.Is(err, downloader.ErrHintDeclined) {
		t.Fatalf("err = %v, want ErrHintDeclined", err)
	}
	if errors.Is(err, downloader.ErrNotSupported) {
		t.Error("a declined window must not read as an unsupported backend")
	}
}

func TestPrioritizePiecesDeclinedKeepsConnection(t *testing.T) {
	// A decline is the daemon answering, not the transport failing: the
	// connection must survive it. Dropping it here reconnects on every
	// retry of the fast decline loop — right after an add, while the
	// availability poller shares the same connection and the playability
	// grace is ticking.
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI(), apiVersion: 1, prioritizeFalse: true}
	p.plugins = []string{"Seedstream"}
	c := newPluginClient(p, &now)

	for range 3 {
		if err := c.PrioritizePieces(context.Background(), "abc123", 10, 20); !errors.Is(err, downloader.ErrHintDeclined) {
			t.Fatalf("err = %v, want ErrHintDeclined", err)
		}
	}
	if n := countRPC(p.fakeAPI, "close"); n != 0 {
		t.Errorf("connection closed %d times by declined hints, want 0", n)
	}
	if n := countRPC(p.fakeAPI, "connect"); n != 1 {
		t.Errorf("connected %d times, want 1 (no reconnect churn)", n)
	}
}

func TestPrioritizePiecesPassesDeadlineParams(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI(), apiVersion: 1}
	p.plugins = []string{"Seedstream"}
	c := newPluginClient(p, &now)

	if err := c.PrioritizePieces(context.Background(), "ABC123", 10, 20); err != nil {
		t.Fatal(err)
	}
	if n := len(p.prioritizeArgs); n != 5 {
		t.Fatalf("prioritize_range got %d args (%v), want 5 (hash, first, last, deadline_ms, step_ms)", n, p.prioritizeArgs)
	}
	if got := p.prioritizeArgs[0]; got != "abc123" {
		t.Errorf("hash arg = %v, want lowercased abc123", got)
	}
	assertArg := func(i, want int) {
		t.Helper()
		got, err := toInt(p.prioritizeArgs[i])
		if err != nil || got != want {
			t.Errorf("arg[%d] = %v (%v), want %d", i, p.prioritizeArgs[i], err, want)
		}
	}
	assertArg(1, 10)
	assertArg(2, 20)
	assertArg(3, prioritizeDeadlineMS)
	assertArg(4, prioritizeStepMS)
}

func TestPrioritizePiecesNegativeProbeExpiresSooner(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI()} // no plugins enabled
	c := newPluginClient(p, &now)

	if err := c.PrioritizePieces(context.Background(), "abc123", 0, 5); !errors.Is(err, downloader.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}

	// The plugin is enabled while a piece wait is running: the negative
	// probe must expire on its shorter TTL — well before the positive
	// pluginProbeTTL — so the wait's periodic re-hint gets through.
	p.plugins = []string{"Seedstream"}
	p.apiVersion = 1
	now = now.Add(pluginProbeNegTTL + time.Second)
	if err := c.PrioritizePieces(context.Background(), "abc123", 0, 5); err != nil {
		t.Fatalf("err = %v after enabling plugin inside pluginProbeTTL", err)
	}
	if n := countRPC(p.fakeAPI, "prioritize_range"); n != 1 {
		t.Errorf("prioritize_range called %d times, want 1", n)
	}
}

func TestPrioritizePiecesCachesPositiveProbe(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI(), apiVersion: 1}
	p.plugins = []string{"Seedstream"}
	c := newPluginClient(p, &now)

	for range 3 {
		if err := c.PrioritizePieces(context.Background(), "abc123", 0, 5); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRPC(p.fakeAPI, "api_version"); n != 1 {
		t.Errorf("api_version probed %d times, want 1 (cached)", n)
	}
	if n := countRPC(p.fakeAPI, "prioritize_range"); n != 3 {
		t.Errorf("prioritize_range called %d times, want 3", n)
	}
}

func TestPrioritizePiecesRPCErrorInvalidatesProbe(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI(), apiVersion: 1}
	p.plugins = []string{"Seedstream"}
	c := newPluginClient(p, &now)

	if err := c.PrioritizePieces(context.Background(), "abc123", 0, 5); err != nil {
		t.Fatal(err)
	}
	// The plugin gets disabled mid-flight: the daemon-side error must
	// surface as ErrNotSupported and force a fresh probe next call.
	p.prioritizeErr = delugerpc.RPCError{ExceptionType: "AttributeError", ExceptionMessage: "unknown method"}
	p.plugins = nil
	err := c.PrioritizePieces(context.Background(), "abc123", 0, 5)
	if !errors.Is(err, downloader.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
	probes := countRPC(p.fakeAPI, "api_version")
	err = c.PrioritizePieces(context.Background(), "abc123", 0, 5)
	if !errors.Is(err, downloader.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported after re-probe", err)
	}
	if got := len(p.calls); got == 0 {
		t.Fatal("no calls recorded")
	}
	// Re-probe happened: GetEnabledPlugins consulted again (api_version
	// count unchanged since the plugin vanished from the list).
	if n := countRPC(p.fakeAPI, "api_version"); n != probes {
		t.Errorf("api_version count changed unexpectedly: %d -> %d", probes, n)
	}
}

func TestPrioritizePiecesOldAPIVersionUnsupported(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	p := &pluginAPI{fakeAPI: newFakeAPI(), apiVersion: 0}
	p.plugins = []string{"Seedstream"}
	c := newPluginClient(p, &now)

	if err := c.PrioritizePieces(context.Background(), "abc123", 0, 5); !errors.Is(err, downloader.ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported for api_version 0", err)
	}
}

func TestPrioritizeAcceptedReadsPluginReply(t *testing.T) {
	tests := []struct {
		name string
		res  rencode.List
		want bool
	}{
		{"accepted", rencode.NewList(true), true},
		{"declined", rencode.NewList(false), false},
		// A reply that isn't a bool at all (older or patched plugin)
		// must not be read as a refusal.
		{"empty", rencode.List{}, true},
		{"non-bool", rencode.NewList(int64(1)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prioritizeAccepted(tt.res); got != tt.want {
				t.Fatalf("prioritizeAccepted() = %v, want %v", got, tt.want)
			}
		})
	}
}
