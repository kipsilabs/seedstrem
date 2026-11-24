package prowlarr

import "context"

type progressKey struct{}

// WithProgress returns a context whose searches report each resolved
// result to fn as soon as it is ready, before the whole search finishes.
// That lets a caller answer early with what has arrived so far while the
// search runs on (see stremio's progressive discovery search). fn must be
// safe for concurrent use: per-indexer searches run in parallel.
func WithProgress(ctx context.Context, fn func(Result)) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

// Report hands r to the progress function attached to ctx, if any.
func Report(ctx context.Context, r Result) {
	if fn, ok := ctx.Value(progressKey{}).(func(Result)); ok && fn != nil {
		fn(r)
	}
}
