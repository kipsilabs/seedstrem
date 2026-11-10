package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func (e *env) post(t *testing.T, target, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if e.cookie != nil {
		req.AddCookie(e.cookie)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, req)
	return w
}

func TestAddTorrentMagnet(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	body, _ := json.Marshal(map[string]string{"magnet": "magnet:?xt=urn:btih:" + readdHash + "&dn=Show"})
	w := e.post(t, "/torrents", "application/json", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("add = %d %s, want 201", w.Code, w.Body.String())
	}
	if _, err := e.store.TorrentByHash(context.Background(), readdHash); err != nil {
		t.Errorf("not stored: %v", err)
	}
}

func TestAddTorrentFileUpload(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "x.torrent")
	if _, err := fw.Write([]byte(readdTorrent)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	w := e.post(t, "/torrents", mw.FormDataContentType(), buf.Bytes())
	if w.Code != http.StatusCreated {
		t.Fatalf("upload = %d %s, want 201", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	hash, _ := resp["hash"].(string)
	if hash == "" {
		t.Fatalf("response has no hash: %s", w.Body.String())
	}
	if _, err := e.store.TorrentFile(context.Background(), hash); err != nil {
		t.Errorf(".torrent not saved: %v", err)
	}
}

func TestAddTorrentRejectsInvalid(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	for name, body := range map[string]string{
		"empty":      `{}`,
		"bad magnet": `{"magnet":"nope"}`,
		"bad json":   `{`,
	} {
		w := e.post(t, "/torrents", "application/json", []byte(body))
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400 (%s)", name, w.Code, strings.TrimSpace(w.Body.String()))
		}
	}
}

func TestAddTorrentRequiresSession(t *testing.T) {
	e := newEnv(t)
	w := e.post(t, "/torrents", "application/json", []byte(`{}`))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", w.Code)
	}
}
