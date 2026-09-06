package deluge

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/gdm85/go-rencode"

	"github.com/kipsilabs/seedstrem/internal/deluge/delugerpc"
	"github.com/kipsilabs/seedstrem/internal/downloader"
)

// fakeAPI fakes the vendored RPC surface at the api interface boundary.
type fakeAPI struct {
	calls []string

	connectErr error
	statuses   map[string]*delugerpc.TorrentStatus
	pieces     map[string][]int
	piecesNil  map[string]bool
	plugins    []string

	byLabel map[string]map[string]*delugerpc.TorrentStatus

	setFilePriorities map[string][]int
	torrentOptions    []string

	freeSpace    int64
	freeSpaceErr error
}

func newFakeAPI() *fakeAPI {
	return &fakeAPI{
		statuses:          map[string]*delugerpc.TorrentStatus{},
		pieces:            map[string][]int{},
		piecesNil:         map[string]bool{},
		setFilePriorities: map[string][]int{},
	}
}

func (f *fakeAPI) record(format string, args ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeAPI) Connect(context.Context) error { f.record("connect"); return f.connectErr }
func (f *fakeAPI) Close() error                  { f.record("close"); return nil }

func (f *fakeAPI) TorrentStatus(_ context.Context, hash string) (*delugerpc.TorrentStatus, error) {
	ts, ok := f.statuses[hash]
	if !ok {
		return nil, delugerpc.RPCError{ExceptionType: "InvalidTorrentError"}
	}
	return ts, nil
}

func (f *fakeAPI) TorrentsStatusByLabel(_ context.Context, label string) (map[string]*delugerpc.TorrentStatus, error) {
	f.record("torrentsStatusByLabel %s", label)
	out := map[string]*delugerpc.TorrentStatus{}
	for hash, ts := range f.byLabel[label] {
		out[hash] = ts
	}
	return out, nil
}

func (f *fakeAPI) TorrentsStatus(_ context.Context, _ delugerpc.TorrentState, ids []string) (map[string]*delugerpc.TorrentStatus, error) {
	out := map[string]*delugerpc.TorrentStatus{}
	for _, id := range ids {
		if ts, ok := f.statuses[id]; ok {
			out[id] = ts
		}
	}
	return out, nil
}

func (f *fakeAPI) AddTorrentMagnet(_ context.Context, uri string, _ *delugerpc.Options) (string, error) {
	f.record("addMagnet %s", uri)
	return "abc123", nil
}

func (f *fakeAPI) AddTorrentFile(_ context.Context, name, _ string, _ *delugerpc.Options) (string, error) {
	f.record("addFile %s", name)
	return "abc123", nil
}

func (f *fakeAPI) RemoveTorrent(_ context.Context, id string, rmFiles bool) (bool, error) {
	f.record("remove %s files=%v", id, rmFiles)
	if _, ok := f.statuses[id]; !ok {
		return false, delugerpc.RPCError{ExceptionType: "InvalidTorrentError"}
	}
	delete(f.statuses, id)
	return true, nil
}

func (f *fakeAPI) SetTorrentOptions(_ context.Context, id string, o *delugerpc.Options) error {
	seq, flp := "nil", "nil"
	if o.V2.SequentialDownload != nil {
		seq = fmt.Sprint(*o.V2.SequentialDownload)
	}
	if o.PrioritizeFirstLastPieces != nil {
		flp = fmt.Sprint(*o.PrioritizeFirstLastPieces)
	}
	f.record("setOptions %s seq=%s flp=%s", id, seq, flp)
	f.torrentOptions = append(f.torrentOptions, id)
	return nil
}

func (f *fakeAPI) SetFilePriorities(_ context.Context, hash string, priorities []int) error {
	f.record("setFilePriorities %s %v", hash, priorities)
	f.setFilePriorities[hash] = priorities
	return nil
}

func (f *fakeAPI) PieceStates(_ context.Context, hash string) ([]int, error) {
	if f.piecesNil[hash] {
		return nil, nil
	}
	if p, ok := f.pieces[hash]; ok {
		return p, nil
	}
	return nil, delugerpc.RPCError{ExceptionType: "InvalidTorrentError"}
}

func (f *fakeAPI) ResumeTorrents(_ context.Context, ids ...string) error {
	f.record("resume %v", ids)
	return nil
}

func (f *fakeAPI) DaemonVersion(context.Context) (string, error) { return "2.1.1", nil }

func (f *fakeAPI) GetFreeSpace(_ context.Context, path string) (int64, error) {
	f.record("getFreeSpace path=%s", path)
	return f.freeSpace, f.freeSpaceErr
}

func (f *fakeAPI) GetEnabledPlugins(context.Context) ([]string, error) { return f.plugins, nil }

func (f *fakeAPI) RPC(_ context.Context, method string, args rencode.List, _ rencode.Dictionary) (rencode.List, error) {
	f.record("rpc %s", method)
	return rencode.List{}, nil
}

func newTestClient(f api) *client {
	return &client{rpc: f, label: "seedstrem", flagCache: map[string]flags{}}
}

func TestSetFilePriorityPatchesFullArray(t *testing.T) {
	f := newFakeAPI()
	f.statuses["abc123"] = &delugerpc.TorrentStatus{
		Name: "Movie", TotalSize: 300,
		Files: []delugerpc.File{
			{Index: 0, Size: 100, Path: "a"},
			{Index: 1, Size: 100, Path: "b"},
			{Index: 2, Size: 100, Path: "c"},
		},
		FilePriorities: []int64{4, 4, 4},
	}
	c := newTestClient(f)

	// Neutral priority 1 (normal) maps to Deluge 4; 0 stays skip.
	if err := c.SetFilePriority(context.Background(), "ABC123", []int{0, 2}, 0); err != nil {
		t.Fatalf("SetFilePriority: %v", err)
	}
	want := []int{0, 4, 0}
	got := f.setFilePriorities["abc123"]
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("priorities = %v, want %v (read-modify-write of the full array)", got, want)
	}

	// Neutral 7 (max) maps to Deluge 7 (high).
	f.statuses["abc123"].FilePriorities = []int64{0, 4, 0}
	if err := c.SetFilePriority(context.Background(), "abc123", []int{1}, 7); err != nil {
		t.Fatalf("SetFilePriority: %v", err)
	}
	if got := f.setFilePriorities["abc123"]; got[1] != 7 {
		t.Errorf("priorities = %v, want index 1 = 7", got)
	}
}

func TestPieceStatesSynthesizesForFinishedTorrent(t *testing.T) {
	f := newFakeAPI()
	f.statuses["abc123"] = &delugerpc.TorrentStatus{
		Name: "Movie", TotalSize: 100, NumPieces: 4, Progress: 100, IsFinished: true,
	}
	f.piecesNil["abc123"] = true
	c := newTestClient(f)

	states, err := c.PieceStates(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("PieceStates: %v", err)
	}
	if len(states) != 4 {
		t.Fatalf("states = %d, want 4 synthesized", len(states))
	}
	for i, s := range states {
		if s != downloader.PieceHave {
			t.Errorf("piece %d = %v, want PieceHave", i, s)
		}
	}
}

func TestPieceStatesNilForMetadatalessTorrent(t *testing.T) {
	f := newFakeAPI()
	f.statuses["abc123"] = &delugerpc.TorrentStatus{Name: "Movie", TotalSize: 100, NumPieces: 4, Progress: 10}
	f.piecesNil["abc123"] = true
	c := newTestClient(f)

	states, err := c.PieceStates(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("PieceStates: %v", err)
	}
	if len(states) != 0 {
		t.Errorf("states = %v, want empty for unfinished torrent reporting None", states)
	}
}

func TestTorrentNotFound(t *testing.T) {
	c := newTestClient(newFakeAPI())
	if _, err := c.Torrent(context.Background(), "unknown"); !errors.Is(err, downloader.ErrTorrentNotFound) {
		t.Errorf("err = %v, want ErrTorrentNotFound", err)
	}
	if err := c.Delete(context.Background(), "unknown", false); !errors.Is(err, downloader.ErrTorrentNotFound) {
		t.Errorf("delete err = %v, want ErrTorrentNotFound", err)
	}
}

func TestSettersRememberFlags(t *testing.T) {
	f := newFakeAPI()
	f.statuses["abc123"] = &delugerpc.TorrentStatus{Name: "Movie", TotalSize: 100}
	c := newTestClient(f)
	ctx := context.Background()

	if err := c.SetSequentialDownload(ctx, "ABC123", true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFirstLastPiecePrio(ctx, "abc123", true); err != nil {
		t.Fatal(err)
	}
	info, err := c.Torrent(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if !info.SequentialDownload || !info.FirstLastPiecePrio {
		t.Errorf("flags not remembered: %+v", info)
	}
}

func TestTransportErrorDropsConnection(t *testing.T) {
	f := newFakeAPI()
	c := newTestClient(f)
	ctx := context.Background()

	// RPCError (daemon-side) must NOT drop the connection...
	if _, err := c.Torrent(ctx, "unknown"); err == nil {
		t.Fatal("want error")
	}
	closes := 0
	for _, call := range f.calls {
		if call == "close" {
			closes++
		}
	}
	if closes != 0 {
		t.Errorf("daemon-side error must not close the connection: calls=%v", f.calls)
	}

	// ...but a transport-level error must, so the next call redials.
	f.statuses["abc123"] = &delugerpc.TorrentStatus{Name: "Movie", TotalSize: 100}
	c2 := newTestClient(&erroringAPI{fakeAPI: f})
	if err := c2.Start(ctx, "abc123"); err == nil {
		t.Fatal("want transport error")
	}
	c2f := c2.rpc.(*erroringAPI)
	if !c2f.closed {
		t.Error("transport error must close the connection")
	}
	if c2.connected {
		t.Error("client must mark itself disconnected after a transport error")
	}
}

type erroringAPI struct {
	*fakeAPI
	closed bool
}

func (e *erroringAPI) ResumeTorrents(context.Context, ...string) error {
	return errors.New("broken pipe")
}

func (e *erroringAPI) Close() error {
	e.closed = true
	return nil
}

func TestVersionPrefix(t *testing.T) {
	c := newTestClient(newFakeAPI())
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "deluge 2.1.1" {
		t.Errorf("version = %q", v)
	}
}

func TestFreeSpace(t *testing.T) {
	f := newFakeAPI()
	f.freeSpace = 4096
	c := newTestClient(f)

	got, err := c.FreeSpace(context.Background())
	if err != nil {
		t.Fatalf("FreeSpace: %v", err)
	}
	if got != 4096 {
		t.Errorf("free space = %d, want 4096", got)
	}
	// The default download location is requested, i.e. an empty path.
	if !slices.Contains(f.calls, "getFreeSpace path=") {
		t.Errorf("calls = %v, want a getFreeSpace with an empty path", f.calls)
	}
}

func TestFreeSpaceError(t *testing.T) {
	f := newFakeAPI()
	f.freeSpaceErr = errors.New("boom")
	c := newTestClient(f)

	if _, err := c.FreeSpace(context.Background()); err == nil {
		t.Fatal("FreeSpace: want error, got nil")
	}
}
