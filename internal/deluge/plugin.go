package deluge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/gdm85/go-rencode"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

const (
	// pluginName is the Seedstream plugin's name in Deluge's plugin list;
	// its RPC methods live under the lowercased prefix "seedstream.".
	pluginName = "Seedstream"
	// pluginProbeTTL is how long a positive probe result is trusted
	// before the plugin list is re-checked, so disabling the plugin is
	// picked up without restarting seedstrem.
	pluginProbeTTL = 60 * time.Second
	// pluginProbeNegTTL is the (shorter) trust window for a negative
	// probe: a stale "no plugin" answer turns the one prioritize call a
	// blocking seek gets to make into a silent no-op, so enabling the
	// plugin must be noticed while such a wait is still running.
	pluginProbeNegTTL = 10 * time.Second

	// prioritizeDeadlineMS is the deadline requested for the first piece
	// of a prioritized window, with prioritizeStepMS added per subsequent
	// piece so they arrive in playback order. Tighter than the plugin's
	// 3s default: a seek window must cut ahead of the sequential flood's
	// inflight backlog, and libtorrent orders its time-critical queue by
	// deadline. Accepted by plugin api_version >= 1 (extra positional
	// args map onto the exported method's defaults).
	prioritizeDeadlineMS = 500
	prioritizeStepMS     = 50
)

// pluginAvailable reports whether the Seedstream plugin is enabled and
// speaks a compatible API version, caching the answer for
// pluginProbeTTL. Called while do() holds the client mutex (probing
// performs RPC calls on the shared connection).
func (c *client) pluginAvailable(ctx context.Context) bool {
	c.pluginMu.Lock()
	ttl := pluginProbeTTL
	if !c.pluginOK {
		ttl = pluginProbeNegTTL
	}
	if !c.pluginChecked.IsZero() && c.now().Sub(c.pluginChecked) < ttl {
		ok := c.pluginOK
		c.pluginMu.Unlock()
		return ok
	}
	c.pluginMu.Unlock()

	ok := c.probePlugin(ctx)
	c.pluginMu.Lock()
	c.pluginOK, c.pluginChecked = ok, c.now()
	c.pluginMu.Unlock()
	return ok
}

func (c *client) invalidatePluginProbe() {
	c.pluginMu.Lock()
	c.pluginChecked = time.Time{}
	c.pluginMu.Unlock()
}

func (c *client) probePlugin(ctx context.Context) bool {
	plugins, err := c.rpc.GetEnabledPlugins(ctx)
	if err != nil || !slices.Contains(plugins, pluginName) {
		return false
	}
	res, err := c.rpc.RPC(ctx, "seedstream.api_version", rencode.List{}, rencode.Dictionary{})
	if err != nil {
		return false
	}
	values := res.Values()
	if len(values) == 0 {
		return false
	}
	v, err := toInt(values[0])
	return err == nil && v >= 1
}

// prioritizeAccepted reports whether prioritize_range returned truthy.
// An empty reply counts as accepted: only an explicit False means the
// plugin declined the window.
func prioritizeAccepted(res rencode.List) bool {
	values := res.Values()
	if len(values) == 0 {
		return true
	}
	ok, isBool := values[0].(bool)
	return !isBool || ok
}

// toInt widens the integer types go-rencode may decode a Python int into.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int8:
		return int(n), nil
	case int16:
		return int(n), nil
	case int32:
		return int(n), nil
	case int64:
		return int(n), nil
	case int:
		return n, nil
	default:
		return 0, fmt.Errorf("not an integer: %T", v)
	}
}

// PrioritizePieces asks the Seedstream plugin to deadline-fetch pieces
// [first, last]. Without the plugin it reports ErrNotSupported so the
// stream layer backs off (and re-probes after the TTL).
func (c *client) PrioritizePieces(ctx context.Context, hash string, first, last int) error {
	return c.do(ctx, func(ctx context.Context) error {
		if !c.pluginAvailable(ctx) {
			return downloader.ErrNotSupported
		}
		args := rencode.NewList(strings.ToLower(hash), first, last, prioritizeDeadlineMS, prioritizeStepMS)
		res, err := c.rpc.RPC(ctx, "seedstream.prioritize_range", args, rencode.Dictionary{})
		if err == nil && !prioritizeAccepted(res) {
			// The daemon answered, but the plugin declined the window
			// (unknown torrent, metadata not in yet, per-piece failure).
			// Reported as ErrHintDeclined rather than success so the
			// stream layer retries on its next poll: treating it as
			// delivered cost a declined first hint the full re-hint
			// interval, which is most of a play's playability grace.
			slog.Default().Debug("deluge: plugin declined piece prioritization",
				"hash", hash, "first", first, "last", last)
			return fmt.Errorf("deluge prioritize pieces %s: %w", hash, downloader.ErrHintDeclined)
		}
		if err != nil {
			if errors.As(err, new(delugerpc.RPCError)) {
				// The daemon answered but the call failed — most likely
				// the plugin was disabled since the last probe. Re-probe
				// on the next call and tell the caller to back off.
				c.invalidatePluginProbe()
				return fmt.Errorf("deluge prioritize pieces %s: %v: %w", hash, err, downloader.ErrNotSupported)
			}
			return fmt.Errorf("deluge prioritize pieces %s: %w", hash, err)
		}
		return nil
	})
}
