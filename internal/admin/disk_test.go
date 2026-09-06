package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/kipsilabs/seedstrem/internal/config"
	"github.com/kipsilabs/seedstrem/internal/downloader"
	"github.com/kipsilabs/seedstrem/internal/downloader/fake"
)

// diskOf fetches /status and returns its "disk" object.
func diskOf(t *testing.T, e *env) map[string]any {
	t.Helper()
	w := e.do(t, http.MethodGet, "/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d %s, want 200", w.Code, w.Body.String())
	}
	var body struct {
		Disk map[string]any `json:"disk"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.Disk == nil {
		t.Fatal("status response has no disk object")
	}
	return body.Disk
}

// seedCompleted puts a torrent in the store and the fake client with the
// given completed byte count, so the handler can sum it.
func seedCompleted(t *testing.T, e *env, id, hash string, completed int64) {
	t.Helper()
	seedTorrent(t, e, id, hash)
	if !e.fake.Update(hash, func(tor *fake.Torrent) { tor.Completed = completed }) {
		t.Fatalf("fake has no torrent %s", hash)
	}
}

// setDownloadPath configures a single path mapping so the statfs fallback
// has a path to measure.
func setDownloadPath(t *testing.T, e *env, local string) {
	t.Helper()
	cfg := e.config.Get()
	cfg.Paths.Mappings = []config.Mapping{{Remote: "/downloads", Local: local}}
	if err := e.config.Update(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
}

func TestStatusDiskUsedSumsCompleted(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	seedCompleted(t, e, "id-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 100)
	seedCompleted(t, e, "id-2", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 250)

	if got := diskOf(t, e)["used"]; got != float64(350) {
		t.Errorf("used = %v, want 350", got)
	}
}

func TestStatusDiskFreeFromClient(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	e.fake.SetFreeSpace(8192)
	// A statfs fallback would be wrong here; make it loud if it is used.
	e.h.diskUsage = func(string) (int64, int64, error) {
		t.Error("diskUsage called even though the client answered")
		return 0, 0, nil
	}

	disk := diskOf(t, e)
	if disk["free"] != float64(8192) {
		t.Errorf("free = %v, want 8192", disk["free"])
	}
	if disk["free_source"] != "client" {
		t.Errorf("free_source = %v, want client", disk["free_source"])
	}
}

func TestStatusDiskFallsBackToLocalStatfs(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	// Default fake behaviour is ErrNotSupported.
	e.fake.SetFreeSpaceErr(downloader.ErrNotSupported)
	setDownloadPath(t, e, "/data")
	e.h.diskUsage = func(path string) (int64, int64, error) {
		if path != "/data" {
			t.Errorf("diskUsage path = %q, want /data", path)
		}
		return 300, 1000, nil
	}

	disk := diskOf(t, e)
	if disk["free"] != float64(700) {
		t.Errorf("free = %v, want 700 (total 1000 - used 300)", disk["free"])
	}
	if disk["free_source"] != "local" {
		t.Errorf("free_source = %v, want local", disk["free_source"])
	}
}

func TestStatusDiskOmitsFreeWhenBothFail(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	seedCompleted(t, e, "id-1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 100)
	e.fake.SetFreeSpaceErr(errors.New("client unreachable"))
	setDownloadPath(t, e, "/data")
	e.h.diskUsage = func(string) (int64, int64, error) {
		return 0, 0, errors.New("statfs failed")
	}

	disk := diskOf(t, e)
	if _, ok := disk["free"]; ok {
		t.Errorf("free present (%v), want omitted when both sources fail", disk["free"])
	}
	if _, ok := disk["free_source"]; ok {
		t.Errorf("free_source present (%v), want omitted", disk["free_source"])
	}
	// used still reports: it comes from data the handler already holds.
	if disk["used"] != float64(100) {
		t.Errorf("used = %v, want 100", disk["used"])
	}
}

func TestStatusDiskOmitsFreeWhenNoMappingConfigured(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	e.fake.SetFreeSpaceErr(downloader.ErrNotSupported)
	// config.Default() ships a /downloads -> /data mapping, so the
	// no-path case has to be arranged explicitly.
	cfg := e.config.Get()
	cfg.Paths.Mappings = nil
	if err := e.config.Update(cfg); err != nil {
		t.Fatalf("update config: %v", err)
	}
	e.h.diskUsage = func(string) (int64, int64, error) {
		t.Error("diskUsage called with no mapping configured")
		return 0, 0, nil
	}

	if _, ok := diskOf(t, e)["free"]; ok {
		t.Error("free present, want omitted with no configured download path")
	}
}
