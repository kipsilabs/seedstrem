package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
	"github.com/kipsilabs/seedstrem/internal/store"
	"github.com/kipsilabs/seedstrem/internal/torrents"
)

const adminPassword = "test-admin-pw"

type env struct {
	handler   http.Handler
	h         *Handler
	config    *config.Manager
	fake      *fake.Server
	swappable *downloader.Swappable
	store     *store.Store
	cookie    *http.Cookie
	t         *testing.T
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := fake.New()

	cfg := config.Default()
	cfg.Server.AdminPassword = adminPassword
	cfg.Storage.Database = filepath.Join(t.TempDir(), "db")
	cm := config.NewManager(cfg, filepath.Join(t.TempDir(), "config.yaml"))

	st, err := store.Open(cfg.Storage.Database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	dc := downloader.NewSwappable(f)
	// Tests swap in a fresh fake on config changes, standing in for the
	// real backend factory.
	newClient := func(config.Config) downloader.Client { return fake.New() }
	svc := torrents.New(st, dc, func() torrents.Settings {
		c := cm.Get()
		return torrents.Settings{DeleteFilesOnRemove: c.Storage.DeleteFilesOnRemove}
	}, nil)
	h := New(cm, st, dc, svc, newClient, "test", nil)
	return &env{handler: h.Router(), h: h, config: cm, fake: f, swappable: dc, store: st, t: t}
}

func (e *env) login(t *testing.T) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{"password":"`+adminPassword+`"}`))
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("login failed: %d %s", w.Code, w.Body.String())
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			e.cookie = c
			return
		}
	}
	t.Fatal("no session cookie set")
}

func (e *env) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

func TestLoginWrongPassword(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/session", strings.NewReader(`{"password":"nope"}`))
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.Code)
	}
}

func TestUnauthenticatedRejected(t *testing.T) {
	e := newEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/config", nil)
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d; want 401", w.Code)
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	req := httptest.NewRequest(http.MethodPost, "/config/test-qbittorrent", nil)
	req.AddCookie(e.cookie)
	// No X-Requested-With header.
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d; want 403", w.Code)
	}
}

func TestSessionExpiry(t *testing.T) {
	if validSession(adminPassword, mintSession(adminPassword, time.Now().Add(-time.Minute)), time.Now()) {
		t.Error("expired session accepted")
	}
	if validSession("other-password", mintSession(adminPassword, time.Now().Add(time.Hour)), time.Now()) {
		t.Error("session valid across password change")
	}
	if !validSession(adminPassword, mintSession(adminPassword, time.Now().Add(time.Hour)), time.Now()) {
		t.Error("fresh session rejected")
	}
	if validSession(adminPassword, "garbage", time.Now()) {
		t.Error("garbage session accepted")
	}
}

func TestGetConfigMasksSecrets(t *testing.T) {
	e := newEnv(t)
	cfg := e.config.Get()
	cfg.QBittorrent.Password = "qbit-secret"
	if err := e.config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	e.login(t)

	w := e.do(t, http.MethodGet, "/config", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "qbit-secret") || strings.Contains(body, adminPassword) {
		t.Errorf("secrets leaked: %s", body)
	}
}

func TestPutConfigKeepsMaskedPassword(t *testing.T) {
	e := newEnv(t)
	cfg := e.config.Get()
	cfg.QBittorrent.Password = "qbit-secret"
	if err := e.config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	e.login(t)

	// Read config, send it back unchanged (password still masked).
	w := e.do(t, http.MethodGet, "/config", "")
	var dto configDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(dto)
	w = e.do(t, http.MethodPut, "/config", string(payload))
	if w.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", w.Code, w.Body.String())
	}
	if got := e.config.Get().QBittorrent.Password; got != "qbit-secret" {
		t.Errorf("masked password overwrote stored value: %q", got)
	}

	// Setting a real new password does update.
	dto.QBittorrent.Password = "new-secret"
	payload, _ = json.Marshal(dto)
	if w = e.do(t, http.MethodPut, "/config", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("put status = %d", w.Code)
	}
	if got := e.config.Get().QBittorrent.Password; got != "new-secret" {
		t.Errorf("password not updated: %q", got)
	}
}

func TestPutConfigKeepsMaskedTMDbAPIKey(t *testing.T) {
	e := newEnv(t)
	cfg := e.config.Get()
	cfg.Meta.TMDbAPIKey = "tmdb-secret"
	if err := e.config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	e.login(t)

	w := e.do(t, http.MethodGet, "/config", "")
	var dto configDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Meta.TMDbAPIKey != passwordMask {
		t.Fatalf("expected masked tmdb key, got %q", dto.Meta.TMDbAPIKey)
	}

	payload, _ := json.Marshal(dto)
	if w = e.do(t, http.MethodPut, "/config", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", w.Code, w.Body.String())
	}
	if got := e.config.Get().Meta.TMDbAPIKey; got != "tmdb-secret" {
		t.Errorf("masked tmdb key overwrote stored value: %q", got)
	}

	dto.Meta.TMDbAPIKey = "new-tmdb-secret"
	payload, _ = json.Marshal(dto)
	if w = e.do(t, http.MethodPut, "/config", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("put status = %d", w.Code)
	}
	if got := e.config.Get().Meta.TMDbAPIKey; got != "new-tmdb-secret" {
		t.Errorf("tmdb key not updated: %q", got)
	}
}

func TestPutConfigValidates(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	w := e.do(t, http.MethodGet, "/config", "")
	var dto configDTO
	json.Unmarshal(w.Body.Bytes(), &dto)
	dto.Server.ExternalURL = "not-a-url"
	payload, _ := json.Marshal(dto)

	w = e.do(t, http.MethodPut, "/config", string(payload))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d; want 400", w.Code)
	}
}

func TestTestQbittorrent(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	// The test-connection endpoint always dials a real qBittorrent WebUI
	// (it can't use the injected fake, since it builds a fresh client from
	// the posted URL), so only the failure path is exercisable in a unit
	// test without a live instance.
	w := e.do(t, http.MethodPost, "/config/test-qbittorrent", `{"url":"http://127.0.0.1:1","username":"u","password":"p"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var res map[string]any
	json.Unmarshal(w.Body.Bytes(), &res)
	if res["ok"] != false {
		t.Errorf("expected failure for dead qbittorrent: %v", res)
	}
}

func TestProwlarrIndexers(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	prow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/indexer" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Write([]byte(`[
			{"id":1,"name":"Alpha","protocol":"torrent","enable":true},
			{"id":2,"name":"Beta","protocol":"torrent","enable":false},
			{"id":3,"name":"Gamma","protocol":"torrent","enable":true}
		]`))
	}))
	defer prow.Close()

	w := e.do(t, http.MethodPost, "/config/prowlarr-indexers", `{"url":"`+prow.URL+`","api_key":"k"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var res struct {
		OK       bool `json:"ok"`
		Indexers []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"indexers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected ok, got %s", w.Body.String())
	}
	// Only the two enabled indexers should be returned.
	if len(res.Indexers) != 2 || res.Indexers[0].ID != 1 || res.Indexers[1].ID != 3 {
		t.Errorf("indexers = %+v, want enabled 1 and 3", res.Indexers)
	}

	// Dead instance yields ok:false rather than an error status.
	w = e.do(t, http.MethodPost, "/config/prowlarr-indexers", `{"url":"http://127.0.0.1:1","api_key":"k"}`)
	json.Unmarshal(w.Body.Bytes(), &res)
	if res.OK {
		t.Error("expected failure for dead prowlarr")
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	w := e.do(t, http.MethodGet, "/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status endpoint = %d", w.Code)
	}
	var status map[string]any
	json.Unmarshal(w.Body.Bytes(), &status)
	if status["qbittorrent"].(map[string]any)["connected"] != true {
		t.Errorf("qbittorrent not connected: %v", status)
	}
	manifestURL, _ := status["manifest_url"].(string)
	if !strings.HasSuffix(manifestURL, "/stremio/manifest.json") {
		t.Errorf("status missing manifest_url: %v", status)
	}
}

func TestTorrentsListing(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	// Empty list is fine.
	w := e.do(t, http.MethodGet, "/torrents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("torrents = %d", w.Code)
	}
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(items) != 0 {
		t.Errorf("expected empty list, got %v", items)
	}
}

// closableFake wraps the fake client with a close flag so the hot-swap
// path's cleanup of the replaced client is observable.
type closableFake struct {
	*fake.Server
	closed atomic.Bool
}

func (c *closableFake) Close() error {
	c.closed.Store(true)
	return nil
}

func TestPutConfigSwapsDownloaderTypeAndClosesOld(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	old := &closableFake{Server: fake.New()}
	e.swappable.Swap(old)

	w := e.do(t, http.MethodGet, "/config", "")
	var dto configDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Downloader.Type != "qbittorrent" {
		t.Fatalf("initial type = %q", dto.Downloader.Type)
	}
	dto.Downloader.Type = "deluge"
	payload, _ := json.Marshal(dto)
	if w = e.do(t, http.MethodPut, "/config", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", w.Code, w.Body.String())
	}
	if got := e.config.Get().Downloader.Type; got != "deluge" {
		t.Errorf("stored type = %q", got)
	}
	// The old client must be closed (asynchronously) after the swap.
	deadline := time.Now().Add(2 * time.Second)
	for !old.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("replaced client was never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPutConfigKeepsMaskedDelugePassword(t *testing.T) {
	e := newEnv(t)
	cfg := e.config.Get()
	cfg.Deluge.Password = "deluge-secret"
	if err := e.config.Update(cfg); err != nil {
		t.Fatal(err)
	}
	e.login(t)

	w := e.do(t, http.MethodGet, "/config", "")
	var dto configDTO
	if err := json.Unmarshal(w.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Deluge.Password != passwordMask {
		t.Fatalf("expected masked deluge password, got %q", dto.Deluge.Password)
	}
	payload, _ := json.Marshal(dto)
	if w = e.do(t, http.MethodPut, "/config", string(payload)); w.Code != http.StatusOK {
		t.Fatalf("put status = %d body=%s", w.Code, w.Body.String())
	}
	if got := e.config.Get().Deluge.Password; got != "deluge-secret" {
		t.Errorf("masked deluge password overwrote stored value: %q", got)
	}
}

func TestTestDeluge(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	// Like test-qbittorrent, this dials a real daemon from the posted
	// settings, so only the failure path is unit-testable.
	w := e.do(t, http.MethodPost, "/config/test-deluge", `{"host":"127.0.0.1","port":1,"username":"u","password":"p"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var res map[string]any
	json.Unmarshal(w.Body.Bytes(), &res)
	if res["ok"] != false {
		t.Errorf("expected failure for dead deluge daemon: %v", res)
	}
}

func TestStatusReportsDownloader(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	w := e.do(t, http.MethodGet, "/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	var res struct {
		Downloader struct {
			Type      string `json:"type"`
			Connected bool   `json:"connected"`
		} `json:"downloader"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Downloader.Type != "qbittorrent" || !res.Downloader.Connected {
		t.Errorf("downloader status = %+v", res.Downloader)
	}
}

func TestTorrentsListingExposesSeedTimes(t *testing.T) {
	e := newEnv(t)
	e.login(t)

	const hash = "aa11bb22cc33dd44"
	if err := e.store.InsertTorrent(context.Background(), store.Torrent{
		ID: "tt1", Hash: hash, Name: "Example", Phase: store.PhaseSelected, AddedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// Completed torrent that has been seeding for 2h; default SeedTime is 72h.
	e.fake.Put(&fake.Torrent{Hash: hash, Progress: 1, SeedingTime: 2 * time.Hour})

	w := e.do(t, http.MethodGet, "/torrents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("torrents = %d", w.Code)
	}
	var items []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d: %v", len(items), items)
	}
	if got := items[0]["seed_time"]; got != float64(72*3600) {
		t.Errorf("seed_time = %v, want %d", got, 72*3600)
	}
	if got := items[0]["seeding_time"]; got != float64(2*3600) {
		t.Errorf("seeding_time = %v, want %d", got, 2*3600)
	}
}

func TestTorrentsExposesOrigin(t *testing.T) {
	// The UI distinguishes torrents seedstrem added from ones adopted out
	// of the download client, so origin has to reach the client.
	e := newEnv(t)
	e.login(t)
	if err := e.store.InsertTorrent(context.Background(), store.Torrent{
		ID: "T1", Hash: "aaaa", Name: "adopted one", Phase: store.PhaseSelected,
		AddedAt: 1, Origin: store.OriginAdopted,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := e.do(t, http.MethodGet, "/torrents", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var items []struct {
		ID     string `json:"id"`
		Origin string `json:"origin"`
	}
	if err := json.NewDecoder(w.Body).Decode(&items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d torrents, want 1", len(items))
	}
	if items[0].Origin != store.OriginAdopted {
		t.Fatalf("origin = %q, want %q", items[0].Origin, store.OriginAdopted)
	}
}
