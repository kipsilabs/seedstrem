// Package prowlarr is a small client for Prowlarr's search API plus pure
// ranking/filtering helpers over the results.
package prowlarr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kipsilabs/seedstrem/internal/metainfo"
)

const defaultTimeout = 30 * time.Second

// Result is a normalized Prowlarr search release. Every Result returned
// by Search has both a non-empty InfoHash and a usable MagnetURL (a bare
// infohash is expanded into a magnet); releases with neither are dropped.
type Result struct {
	Title     string
	InfoHash  string // lowercase hex
	MagnetURL string
	// TorrentFile holds the raw .torrent bytes when the release was
	// resolved by downloading a .torrent (magnet-less indexers, typical of
	// private trackers). Adding these directly skips qBittorrent's
	// metadata fetch, which is unreliable for private-tracker peers. Nil
	// when the release already had a magnet/infohash.
	TorrentFile []byte
	Size        int64
	Seeders     int
	Categories  []int
	Indexer     string
	// Freeleech is true when downloading the release does not count
	// against the user's ratio (indexer freeleech flag or a zero
	// download-volume factor). Preferred during ranking.
	Freeleech bool
}

// Client talks to a Prowlarr instance.
type Client struct {
	http    *http.Client
	baseURL string
	apiKey  string
}

// New builds a Client with a default HTTP timeout.
func New(baseURL, apiKey string) *Client {
	return NewWithClient(baseURL, apiKey, &http.Client{Timeout: defaultTimeout})
}

// NewWithClient builds a Client with a caller-supplied http.Client
// (injectable for tests).
func NewWithClient(baseURL, apiKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultTimeout}
	}
	return &Client{http: hc, baseURL: strings.TrimSuffix(baseURL, "/"), apiKey: apiKey}
}

// apiResult mirrors the fields we read from Prowlarr's /api/v1/search
// response. Categories are decoded leniently (see rawCategories).
type apiResult struct {
	Title       string          `json:"title"`
	InfoHash    string          `json:"infoHash"`
	MagnetURL   string          `json:"magnetUrl"`
	DownloadURL string          `json:"downloadUrl"`
	Size        int64           `json:"size"`
	Seeders     int             `json:"seeders"`
	Protocol    string          `json:"protocol"`
	Indexer     string          `json:"indexer"`
	Categories  json.RawMessage `json:"categories"`
	// IndexerFlags carries per-release flags like "freeleech".
	IndexerFlags []string `json:"indexerFlags"`
	// DownloadVolumeFactor is the ratio multiplier applied to the
	// download: 0 means freeleech. Pointer so an absent field (nil) is
	// distinguishable from an explicit 0.
	DownloadVolumeFactor *float64 `json:"downloadVolumeFactor"`
}

// isFreeleech reports whether a release counts as freeleech, via either a
// zero download-volume factor or a "freeleech"/"freeload" indexer flag.
func isFreeleech(r apiResult) bool {
	if r.DownloadVolumeFactor != nil && *r.DownloadVolumeFactor == 0 {
		return true
	}
	for _, f := range r.IndexerFlags {
		lf := strings.ToLower(f)
		if strings.Contains(lf, "freeleech") || strings.Contains(lf, "freeload") {
			return true
		}
	}
	return false
}

// Search queries Prowlarr for query across the given newznab categories.
// When indexerIDs is non-empty the search is scoped to those indexers;
// empty means search every enabled indexer. searchType selects Prowlarr's
// search mode ("search", "movie", or "tvsearch"); empty defaults to
// "search". For "movie"/"tvsearch", query is expected to carry Prowlarr's
// id tokens (e.g. "{ImdbId:tt1234567}{Season:01}{Episode:05}") so
// ID-capable indexers can match precisely instead of by free text — this
// mirrors how Radarr/Sonarr query Prowlarr.
func (c *Client) Search(ctx context.Context, query, searchType string, categories, indexerIDs []int) ([]Result, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("prowlarr: base URL not configured")
	}
	if searchType == "" {
		searchType = "search"
	}

	u, err := url.Parse(c.baseURL + "/api/v1/search")
	if err != nil {
		return nil, fmt.Errorf("prowlarr: bad base URL: %w", err)
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("type", searchType)
	for _, cat := range categories {
		q.Add("categories", strconv.Itoa(cat))
	}
	for _, id := range indexerIDs {
		q.Add("indexerIds", strconv.Itoa(id))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: build request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr: search returned %d", resp.StatusCode)
	}

	var raw []apiResult
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("prowlarr: decode response: %w", err)
	}

	out := make([]Result, 0, len(raw))
	for _, r := range raw {
		if r.Protocol != "" && r.Protocol != "torrent" {
			continue // usenet or unknown — not resolvable via qBittorrent
		}
		res := normalize(r)
		if res.MagnetURL == "" && r.DownloadURL != "" {
			// Some (typically private-tracker) indexers omit magnetUrl and
			// infoHash and only publish a .torrent download link. Fetch it
			// and derive the hash ourselves rather than dropping the
			// release outright.
			if hash, name, trackers, raw, err := c.fetchTorrentHash(ctx, r.DownloadURL); err == nil {
				res.InfoHash = hash
				if res.Title == "" {
					res.Title = name
				}
				res.MagnetURL = synthesizeMagnet(hash, res.Title, trackers)
				// Keep the raw .torrent so the play flow can add it
				// directly (skipping qBittorrent's unreliable metadata
				// fetch); the magnet above is the fallback.
				res.TorrentFile = raw
			}
		}
		if res.MagnetURL == "" {
			continue // no magnet and no infohash — cannot resolve-on-play
		}
		out = append(out, res)
	}
	return out, nil
}

// SearchEach runs query as one search per indexer concurrently and returns
// the union of results from indexers that answered within budget.
//
// Prowlarr's aggregate /api/v1/search is all-or-nothing: a single slow
// indexer stalls the whole response until that indexer's own (non-tunable)
// internal timeout, and there is no request parameter to cap the search or
// ask for partial results (see Prowlarr#586). Fanning out one request per
// indexer under a shared deadline recovers that behavior — indexers still
// in flight when budget elapses are abandoned and whatever already came
// back is kept.
//
// budget <= 0 waits for every indexer (still isolated one-per-request,
// capped only by the HTTP client timeout). When indexerIDs is empty the
// enabled torrent indexers are enumerated once via Indexers — that
// enumeration is covered by the same budget, so a hung /indexer can't
// stall the search past the deadline either.
//
// A single flaky or slow indexer never sinks the search: per-indexer
// failures are dropped. An error is returned only when the search cannot
// start (base URL unset, or indexer enumeration failed) or when every
// indexer failed for a real reason and nothing was collected. A pure
// budget/cancel timeout with no results is not an error — it is the
// deadline doing its job — so it returns (nil, nil).
func (c *Client) SearchEach(ctx context.Context, query, searchType string, categories, indexerIDs []int, budget time.Duration) ([]Result, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("prowlarr: base URL not configured")
	}

	// Derive the budget context first so it also bounds enumeration below.
	sctx := ctx
	if budget > 0 {
		var cancel context.CancelFunc
		sctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	ids := indexerIDs
	if len(ids) == 0 {
		infos, err := c.Indexers(sctx)
		if err != nil {
			return nil, fmt.Errorf("prowlarr: enumerate indexers: %w", err)
		}
		for _, ix := range infos {
			// Empty protocol is treated as torrent, matching Search's
			// per-release protocol filter.
			if ix.Enable && (ix.Protocol == "" || ix.Protocol == "torrent") {
				ids = append(ids, ix.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	var (
		mu       sync.Mutex
		out      []Result
		errs     []error
		hardFail bool // at least one indexer failed for a non-timeout reason
		wg       sync.WaitGroup
	)
	for _, id := range ids {
		wg.Go(func() {
			res, err := c.Search(sctx, query, searchType, categories, []int{id})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("indexer %d: %w", id, err))
				if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
					hardFail = true
				}
				return
			}
			out = append(out, res...)
		})
	}
	wg.Wait()

	// Surface an error only for a genuine failure that yielded nothing; a
	// nothing-in-time budget timeout returns (nil, nil) so callers don't
	// log it as if Prowlarr were down.
	if len(out) == 0 && hardFail {
		return nil, fmt.Errorf("prowlarr: all indexers failed: %w", errors.Join(errs...))
	}
	return out, nil
}

// maxTorrentFileBytes bounds how much of a .torrent download we'll read;
// legitimate .torrent files are a few KB to a few hundred KB at most.
const maxTorrentFileBytes = 2 << 20 // 2 MiB

// fetchTorrentHash downloads a .torrent file from a Prowlarr-provided
// download link and extracts its v1 infohash, display name, and announce
// trackers (needed so the synthesized magnet can find peers on private
// trackers).
func (c *Client) fetchTorrentHash(ctx context.Context, downloadURL string) (hash, name string, trackers []string, raw []byte, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("prowlarr: build torrent request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("prowlarr: fetch torrent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", nil, nil, fmt.Errorf("prowlarr: fetch torrent returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentFileBytes))
	if err != nil {
		return "", "", nil, nil, fmt.Errorf("prowlarr: read torrent body: %w", err)
	}
	hash, name, trackers, err = metainfo.FromTorrent(body)
	if err != nil {
		return "", "", nil, nil, err
	}
	return hash, name, trackers, body, nil
}

// synthesizeMagnet builds a magnet URI from an infohash, optional display
// name, and optional tracker announce URLs. Trackers matter for private
// torrents: without a tr= entry qBittorrent has no announce URL and (with
// DHT/PEX ineffective) finds no peers, so the download stalls at 0%.
func synthesizeMagnet(hash, title string, trackers []string) string {
	if hash == "" {
		return ""
	}
	v := url.Values{}
	if title != "" {
		v.Set("dn", title)
	}
	for _, tr := range trackers {
		v.Add("tr", tr)
	}
	return "magnet:?xt=urn:btih:" + hash + "&" + v.Encode()
}

// IndexerInfo is a Prowlarr indexer as reported by /api/v1/indexer.
type IndexerInfo struct {
	ID           int          `json:"id"`
	Name         string       `json:"name"`
	Protocol     string       `json:"protocol"` // "torrent" | "usenet"
	Enable       bool         `json:"enable"`
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities lists the search parameters an indexer's definition
// declares support for, per content type. Values are Prowlarr's
// capability-enum names (e.g. "ImdbId", "Season", "Episode");
// comparisons should be case-insensitive, matching Prowlarr's own parser.
type Capabilities struct {
	MovieSearchParams []string `json:"movieSearchParams"`
	TvSearchParams    []string `json:"tvSearchParams"`
}

// SupportsMovieImdb reports whether this indexer accepts an ImdbId movie
// search parameter (Prowlarr's "{ImdbId:...}" query token).
func (ix IndexerInfo) SupportsMovieImdb() bool {
	return hasParam(ix.Capabilities.MovieSearchParams, "imdbid")
}

// SupportsTvImdb reports whether this indexer accepts an ImdbId TV
// search parameter (Prowlarr's "{ImdbId:...}" query token).
func (ix IndexerInfo) SupportsTvImdb() bool {
	return hasParam(ix.Capabilities.TvSearchParams, "imdbid")
}

// SupportsMovieTmdb reports whether this indexer accepts a TmdbId movie
// search parameter (Prowlarr's "{TmdbId:...}" query token).
func (ix IndexerInfo) SupportsMovieTmdb() bool {
	return hasParam(ix.Capabilities.MovieSearchParams, "tmdbid")
}

// SupportsTvTmdb reports whether this indexer accepts a TmdbId TV
// search parameter (Prowlarr's "{TmdbId:...}" query token).
func (ix IndexerInfo) SupportsTvTmdb() bool {
	return hasParam(ix.Capabilities.TvSearchParams, "tmdbid")
}

func hasParam(params []string, want string) bool {
	for _, p := range params {
		if strings.EqualFold(p, want) {
			return true
		}
	}
	return false
}

// Indexers lists the indexers configured in the Prowlarr instance so a
// caller can let the operator scope searches to a subset.
func (c *Client) Indexers(ctx context.Context) ([]IndexerInfo, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("prowlarr: base URL not configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/indexer", nil)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: build request: %w", err)
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr: indexer list returned %d", resp.StatusCode)
	}

	var out []IndexerInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("prowlarr: decode response: %w", err)
	}
	return out, nil
}

// normalize derives a lowercase infohash and a usable magnet URL. If the
// release carries only a magnet, the hash is parsed from it; if it
// carries only an infohash, a magnet is synthesized. Releases with
// neither yield an empty MagnetURL (dropped by the caller).
func normalize(r apiResult) Result {
	res := Result{
		Title:      r.Title,
		MagnetURL:  r.MagnetURL,
		Size:       r.Size,
		Seeders:    r.Seeders,
		Indexer:    r.Indexer,
		Categories: parseCategories(r.Categories),
		Freeleech:  isFreeleech(r),
	}
	res.InfoHash = strings.ToLower(strings.TrimSpace(r.InfoHash))

	if res.MagnetURL != "" {
		if hash, _, err := metainfo.FromMagnet(res.MagnetURL); err == nil {
			res.InfoHash = hash
		}
	}
	if res.MagnetURL == "" && res.InfoHash != "" {
		// infoHash-only result: no .torrent to read trackers from.
		res.MagnetURL = synthesizeMagnet(res.InfoHash, r.Title, nil)
	}
	return res
}

// parseCategories decodes Prowlarr categories which may be either a list
// of ints (newznab ids) or a list of objects with an "id" field.
func parseCategories(raw json.RawMessage) []int {
	if len(raw) == 0 {
		return nil
	}
	var ints []int
	if err := json.Unmarshal(raw, &ints); err == nil {
		return ints
	}
	var objs []struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		out := make([]int, 0, len(objs))
		for _, o := range objs {
			out = append(out, o.ID)
		}
		return out
	}
	return nil
}
