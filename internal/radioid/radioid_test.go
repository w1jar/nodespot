package radioid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const sample = Header + "\n" +
	"1234570,N1CALL,Sam,Example,Anytown,Ohio,United States\n" +
	"1234567,N0CALL,Alex,Example,Springfield,Illinois,United States\n" +
	"not-a-number,N2CALL,Bad,Row,X,Y,Z\n" +
	"1234568,N0CALL,Alex,Example,Springfield,Illinois,United States\n" +
	"1234569,short\n" +
	"\n" +
	"1234571,N3CALL,\"Quinn, Jr\",Example,Anytown,,Canada\r\n" +
	"1234572,N4CALL,Last,One,Town,St,Country" // no trailing newline

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestIndexLookup(t *testing.T) {
	x, err := BuildIndex(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if x.Entries() != 5 {
		t.Fatalf("entries = %d, want 5 (malformed rows skipped)", x.Entries())
	}
	u, ok := x.ByID(1234567)
	if !ok || u.Callsign != "N0CALL" || u.Name() != "Alex Example" || u.City != "Springfield" || u.State != "Illinois" || u.Country != "United States" {
		t.Fatalf("ByID = %+v %v", u, ok)
	}
	if _, ok := x.ByID(1234569); ok {
		t.Fatal("malformed row must not be found")
	}
	if _, ok := x.ByID(99); ok {
		t.Fatal("unknown ID found")
	}
	if _, ok := x.ByID(99); ok { // cached miss
		t.Fatal("unknown ID found")
	}
	// one callsign, several IDs: the first row of the file
	u, ok = x.ByCallsign("n0call")
	if !ok || u.ID != 1234567 {
		t.Fatalf("ByCallsign = %+v %v", u, ok)
	}
	u, ok = x.ByCallsign("N3CALL")
	if !ok || u.First != "Quinn, Jr" || u.Country != "Canada" {
		t.Fatalf("quoted row = %+v %v", u, ok)
	}
	if u, ok := x.ByID(1234572); !ok || u.Country != "Country" {
		t.Fatalf("last row without newline = %+v %v", u, ok)
	}
	if _, ok := x.ByCallsign("N9CALL"); ok {
		t.Fatal("unknown callsign found")
	}
	if _, ok := x.ByCallsign(""); ok {
		t.Fatal("empty callsign found")
	}
	var nilIdx *Index
	if _, ok := nilIdx.ByID(1); ok || nilIdx.Entries() != 0 {
		t.Fatal("nil index must be empty")
	}
}

func TestIndexCacheBound(t *testing.T) {
	x, err := BuildIndex(write(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	for i := 0; i < 3*cacheMax; i++ {
		x.ByID(uint32(2000000 + i))
	}
	if len(x.cache) > cacheMax {
		t.Fatalf("cache holds %d entries", len(x.cache))
	}
}

func TestHeaderValidation(t *testing.T) {
	for _, bad := range []string{"", "RADIO_ID,CALLSIGN\n1,A\n", "id,call,first\n", "<html>\n"} {
		if _, err := BuildIndex(write(t, bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := BuildIndex(write(t, Header+"\n")); err == nil {
		t.Error("accepted a list without rows")
	}
	if _, err := BuildIndex(write(t, "\ufeff"+Header+"\r\n1234567,N0CALL,A,B,C,D,E\r\n")); err != nil {
		t.Errorf("BOM and CRLF rejected: %v", err)
	}
	if !ValidHeader(" radio_id,callsign,first_name,last_name,city,state,country ") || ValidHeader("RADIO_ID") {
		t.Error("ValidHeader")
	}
	if _, err := BuildIndex(filepath.Join(t.TempDir(), "missing")); !os.IsNotExist(err) {
		t.Errorf("missing file: %v", err)
	}
}

func TestLongLineSkipped(t *testing.T) {
	body := Header + "\n1234567,N0CALL,A,B,C,D,E\n1234568,N1CALL," + strings.Repeat("x", 200<<10) + ",B,C,D,E\n1234569,N2CALL,A,B,C,D,E\n"
	x, err := BuildIndex(write(t, body))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	if x.Entries() != 2 {
		t.Fatalf("entries = %d, want 2", x.Entries())
	}
	if _, ok := x.ByID(1234569); !ok {
		t.Fatal("row after a long line lost")
	}
}

// ---- download

type srv struct {
	body, etag  string
	hits        atomic.Int32
	conditional atomic.Int32
	ua          atomic.Value
}

func (s *srv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	s.ua.Store(r.Header.Get("User-Agent"))
	if r.Header.Get("If-None-Match") != "" {
		s.conditional.Add(1)
		if r.Header.Get("If-None-Match") == s.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("ETag", s.etag)
	w.Header().Set("Last-Modified", "Fri, 20 Nov 2026 00:00:00 GMT")
	_, _ = w.Write([]byte(s.body))
}

type memSettings map[string]string

func (m memSettings) GetSetting(k string) (string, error) {
	if v, ok := m[k]; ok {
		return v, nil
	}
	return "", os.ErrNotExist
}
func (m memSettings) SetSetting(k, v string) error { m[k] = v; return nil }

func TestDownload(t *testing.T) {
	s := &srv{body: sample, etag: `"v1"`}
	ts := httptest.NewServer(s)
	defer ts.Close()
	dir := t.TempDir()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	g := New(Options{Dir: dir, URL: ts.URL, Version: "1.3.5", Client: ts.Client(), Now: func() time.Time { return now }})
	ctx := context.Background()

	if err := g.Download(ctx, false); err != nil {
		t.Fatal(err)
	}
	if ua, _ := s.ua.Load().(string); ua != "nodespot/1.3.5" {
		t.Errorf("User-Agent = %q", ua)
	}
	st := g.Status()
	if st.Entries != 5 || st.Updated != "2026-11-21T12:00:00Z" || st.Error != "" || !st.Enabled {
		t.Fatalf("status = %+v", st)
	}
	if u, ok := g.ByID(1234567); !ok || u.Callsign != "N0CALL" {
		t.Fatalf("lookup after download: %+v %v", u, ok)
	}

	// 304: nothing replaced, check time moves
	now = now.Add(time.Hour)
	if err := g.Download(ctx, false); err != nil {
		t.Fatal(err)
	}
	if s.conditional.Load() != 1 {
		t.Fatalf("conditional requests = %d", s.conditional.Load())
	}
	if st := g.Status(); st.Updated != "2026-11-21T12:00:00Z" || st.Checked != "2026-11-21T13:00:00Z" || st.Entries != 5 {
		t.Fatalf("status after 304 = %+v", st)
	}

	// a new process reads etag and index back from disk
	g2 := New(Options{Dir: dir, URL: ts.URL, Client: ts.Client(), Now: func() time.Time { return now }})
	g2.Load()
	if g2.Status().Entries != 5 || g2.Status().Updated == "" {
		t.Fatalf("reload = %+v", g2.Status())
	}

	// forced: no conditional header
	if err := g.Download(ctx, true); err != nil || s.conditional.Load() != 1 {
		t.Fatalf("force: %v, conditional %d", err, s.conditional.Load())
	}

	// bad header: the old list stays, the error is reported
	s.body, s.etag = "<html>nope</html>\n", `"v2"`
	if err := g.Download(ctx, true); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("bad header accepted: %v", err)
	}
	if st := g.Status(); st.Entries != 5 || st.Error == "" {
		t.Fatalf("status after bad header = %+v", st)
	}
	if u, ok := g.ByID(1234567); !ok || u.Callsign != "N0CALL" {
		t.Fatal("old list lost after a bad download")
	}
	b, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(b) != sample {
		t.Fatal("file replaced by a bad download")
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, ".user.csv-*")); len(ents) != 0 {
		t.Fatalf("temp files left: %v", ents)
	}

	// HTTP error
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	g.o.URL = bad.URL
	if err := g.Download(ctx, true); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("500: %v", err)
	}
	// success clears the error
	g.o.URL = ts.URL
	s.body, s.etag = sample+"\n1234573,N5CALL,A,B,C,D,E\n", `"v3"`
	if err := g.Download(ctx, true); err != nil || g.Status().Error != "" || g.Status().Entries != 6 {
		t.Fatalf("recovery: %v %+v", err, g.Status())
	}
}

func TestDownloadOversized(t *testing.T) {
	s := &srv{body: sample + strings.Repeat("1234999,N6CALL,A,B,C,D,E\n", 200), etag: `"big"`}
	ts := httptest.NewServer(s)
	defer ts.Close()
	g := New(Options{Dir: t.TempDir(), URL: ts.URL, Client: ts.Client(), MaxSize: 1024})
	err := g.Download(context.Background(), false)
	if err == nil || !strings.Contains(err.Error(), "larger") {
		t.Fatalf("oversized accepted: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(g.o.Dir, FileName)); serr == nil {
		t.Fatal("oversized list was saved")
	}
	if ents, _ := filepath.Glob(filepath.Join(g.o.Dir, ".user.csv-*")); len(ents) != 0 {
		t.Fatalf("temp files left: %v", ents)
	}
	// without a Content-Length (chunked) the cap still holds
	chunked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		_, _ = w.Write([]byte(Header + "\n"))
		fl.Flush()
		for i := 0; i < 100; i++ {
			_, _ = w.Write([]byte("1234999,N6CALL,A,B,C,D,E\n"))
		}
	}))
	defer chunked.Close()
	g.o.URL = chunked.URL
	if err := g.Download(context.Background(), false); err == nil {
		t.Fatal("chunked oversized accepted")
	}
}

func TestSwitchPersisted(t *testing.T) {
	st := memSettings{}
	s := &srv{body: sample, etag: `"v1"`}
	ts := httptest.NewServer(s)
	defer ts.Close()
	dir := t.TempDir()
	g := New(Options{Dir: dir, URL: ts.URL, Client: ts.Client(), Settings: st})
	if !g.Enabled() {
		t.Fatal("default must be on")
	}
	if err := g.Download(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if err := g.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if st[SettingKey] != "false" || g.Status().Entries != 0 {
		t.Fatalf("off: %v %+v", st, g.Status())
	}
	if _, ok := g.ByID(1234567); ok {
		t.Fatal("lookup while off")
	}
	if err := g.Download(context.Background(), false); err != ErrOff {
		t.Fatalf("download while off: %v", err)
	}
	if New(Options{Dir: dir, Settings: st}).Enabled() {
		t.Fatal("switch not restored")
	}
	if err := g.SetEnabled(true); err != nil || g.Status().Entries != 5 {
		t.Fatalf("on: %v %+v", err, g.Status())
	}
}
