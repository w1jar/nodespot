package nodelist

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func quiet() *log.Logger { return log.New(io.Discard, "", 0) }

func TestParse(t *testing.T) {
	infos, err := Parse(strings.NewReader(fixture(t, "astdb.txt")))
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 5 {
		t.Fatalf("got %d: %+v", len(infos), infos)
	}
	byNode := map[string]Info{}
	for _, i := range infos {
		byNode[i.Node] = i
	}
	if byNode["27339"].Location != "Nowhere, XX" {
		t.Fatalf("trailing space: %q", byNode["27339"].Location)
	}
	if byNode["50241"].Description != "" || byNode["50241"].Location != "Metropolis, ST" {
		t.Fatalf("%+v", byNode["50241"])
	}
	if byNode["1997"].Description != "My node (dup, later wins)" {
		t.Fatalf("dup: %+v", byNode["1997"])
	}
	if byNode["2000"].Location != "" {
		t.Fatal("empty last field")
	}
	crlf, _ := Parse(strings.NewReader(fixture(t, "crlf.txt")))
	if len(crlf) != 2 || crlf[0].Location != "Town, ST" {
		t.Fatalf("crlf %+v", crlf)
	}
}

func loaded(t *testing.T) *DB {
	d := New(Options{Logger: quiet()})
	infos, _ := Parse(strings.NewReader(fixture(t, "astdb.txt")))
	d.Set(infos, "test", time.Now())
	return d
}

func TestLookupSearch(t *testing.T) {
	d := loaded(t)
	if i, ok := d.Lookup("1999"); !ok || i.Callsign != "N0AAA" {
		t.Fatal(i, ok)
	}
	if _, ok := d.Lookup("9"); ok {
		t.Fatal("unexpected")
	}
	// node prefix
	r := d.Search("2", 50)
	if len(r) != 2 || r[0].Node != "2000" && r[0].Node != "27339" {
		t.Fatalf("prefix: %+v", r)
	}
	// callsign case-insensitive
	if r := d.Search("n0aab", 10); len(r) != 1 || r[0].Node != "27339" {
		t.Fatalf("callsign: %+v", r)
	}
	// description / location substring
	if r := d.Search("springfield", 10); len(r) != 1 || r[0].Node != "1999" {
		t.Fatalf("location: %+v", r)
	}
	if r := d.Search("REFLECTOR", 10); len(r) != 1 {
		t.Fatalf("desc: %+v", r)
	}
	// limit and empty
	if r := d.Search("n0", 2); len(r) != 2 {
		t.Fatalf("limit: %+v", r)
	}
	if r := d.Search("  ", 5); len(r) != 0 {
		t.Fatal("empty query matched")
	}
	// exact node ranks above substring matches
	if r := d.Search("2000", 5); len(r) == 0 || r[0].Node != "2000" {
		t.Fatalf("rank: %+v", r)
	}
}

func TestDownloadCacheAndFailure(t *testing.T) {
	var fail atomic.Bool
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			http.Error(w, "boom", 500)
			return
		}
		io.WriteString(w, fixture(t, "astdb.txt"))
	}))
	defer srv.Close()
	dir := t.TempDir()
	d := New(Options{URL: srv.URL, DataDir: dir, Logger: quiet()})
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := d.Status()
	if st.Count != 5 || st.Error != nil || st.Updated == nil || st.Source != srv.URL {
		t.Fatalf("%+v", st)
	}
	if _, err := os.Stat(filepath.Join(dir, "astdb.txt")); err != nil {
		t.Fatal("cache not written")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left: %v", entries)
	}
	// failure keeps data and cache, reports error
	fail.Store(true)
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("want error")
	}
	st = d.Status()
	if st.Count != 5 || st.Error == nil || !strings.Contains(*st.Error, "500") {
		t.Fatalf("%+v", st)
	}
	// fresh process loads from cache even though download fails
	d2 := New(Options{URL: srv.URL, DataDir: dir, Logger: quiet()})
	if err := d2.LoadCache(); err != nil {
		t.Fatal(err)
	}
	if st := d2.Status(); st.Count != 5 || st.Source != "cache" {
		t.Fatalf("%+v", st)
	}
	// recovery clears the error
	fail.Store(false)
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d.Status().Error != nil {
		t.Fatal("error not cleared")
	}
}

func TestDownloadRejects(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"garbage": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<html>not a nodelist</html>") },
		"toolarge": func(w http.ResponseWriter, r *http.Request) {
			for i := 0; i < 5000; i++ {
				io.WriteString(w, "1999|N0AAA|"+strings.Repeat("x", 100)+"|y\n")
			}
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			dir := t.TempDir()
			d := New(Options{URL: srv.URL, DataDir: dir, MaxBytes: 1000, Logger: quiet()})
			infos, _ := Parse(strings.NewReader(fixture(t, "astdb.txt")))
			d.Set(infos, "prev", time.Now())
			if err := d.Refresh(context.Background()); err == nil {
				t.Fatal("want error")
			}
			if d.Status().Count != 5 {
				t.Fatal("previous data lost")
			}
			if _, err := os.Stat(filepath.Join(dir, "astdb.txt")); err == nil {
				t.Fatal("cache written from bad download")
			}
		})
	}
}

func TestUnreachableAndTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	d := New(Options{URL: srv.URL, Timeout: 100 * time.Millisecond, Logger: quiet()})
	start := time.Now()
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("want timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout not applied")
	}
	if d.Status().Error == nil {
		t.Fatal("no error recorded")
	}
	d2 := New(Options{URL: "http://127.0.0.1:1/", Logger: quiet()})
	if err := d2.Refresh(context.Background()); err == nil {
		t.Fatal("want connection error")
	}
	if err := New(Options{Logger: quiet()}).Refresh(context.Background()); err == nil {
		t.Fatal("want no-url error")
	}
}

func TestRunRefreshesAndSurvivesFailure(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			http.Error(w, "down", 503)
			return
		}
		io.WriteString(w, fixture(t, "astdb.txt"))
	}))
	defer srv.Close()
	d := New(Options{URL: srv.URL, DataDir: t.TempDir(), Interval: 50 * time.Millisecond, Logger: quiet()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for d.Status().Count == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if d.Status().Count != 5 {
		t.Fatalf("Run never recovered: %+v", d.Status())
	}
}
