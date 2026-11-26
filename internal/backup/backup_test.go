package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/rand"
	"strings"
	"testing"
	"time"
)

const dbBody = "SQLITE example"

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func fileEntry(path, content string) Entry {
	return Entry{Path: path, Type: TypeFile, Size: int64(len(content)), SHA256: sum(content), Owner: "root", Group: "root", Mode: "0640"}
}

// baseManifest is a valid manifest holding the database and files; the
// parts follow the files.
func baseManifest(files ...Entry) *Manifest {
	m := &Manifest{Format: FormatVersion, Created: time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC), CreatedBy: "admin",
		NodespotVersion: "0.0.1", Hostname: "node", Timezone: "UTC", Arch: "arm64", Callsign: "N0CALL",
		Files: append([]Entry{fileEntry(PathNodespotDB, dbBody)}, files...)}
	for _, e := range files {
		switch e.Path {
		case PathNodeConfig:
			m.Parts.Node = true
		case PathSA818:
			m.Parts.SA818 = true
		}
	}
	return m
}

type rawEnt struct {
	hdr  tar.Header
	body string
}

// rawArchive writes a gzip tar with the manifest first and then ents,
// bypassing the Writer's checks.
func rawArchive(t *testing.T, m any, ents ...rawEnt) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	mj, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	all := append([]rawEnt{{hdr: tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(mj))}, body: string(mj)}}, ents...)
	for _, e := range all {
		h := e.hdr
		if h.Typeflag == tar.TypeReg || h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
			if h.Size == 0 {
				h.Size = int64(len(e.body))
			}
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			body := e.body
			if int64(len(body)) > h.Size {
				body = body[:h.Size]
			}
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func reg(path, content string) rawEnt {
	return rawEnt{hdr: tar.Header{Name: path, Mode: 0o644, Size: int64(len(content))}, body: content}
}

func TestWriterReaderRoundTrip(t *testing.T) {
	bodies := map[string]string{PathNodespotDB: dbBody, PathNodeConfig: "{\"version\":1}\n", PathSA818: "{\"version\":1}\n", PathNodespotConf: "[server]\n"}
	m := baseManifest(fileEntry(PathNodeConfig, bodies[PathNodeConfig]), fileEntry(PathSA818, bodies[PathSA818]),
		fileEntry(PathNodespotConf, bodies[PathNodespotConf]))
	m.Parts.Secrets = true
	var buf bytes.Buffer
	w, err := NewWriter(&buf, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range m.Files {
		if err := w.Add(e.Path, strings.NewReader(bodies[e.Path])); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	m2, err := Read(&buf, func(e Entry, body io.Reader) error {
		b, _ := io.ReadAll(body)
		got[e.Path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Callsign != "N0CALL" || !m2.Parts.Node || !m2.Parts.SA818 || !m2.Parts.Secrets || len(m2.Files) != 4 {
		t.Errorf("%+v", m2)
	}
	for p, b := range bodies {
		if got[p] != b {
			t.Errorf("%s: %q", p, got[p])
		}
	}
}

func TestWriterRefuses(t *testing.T) {
	m := baseManifest(fileEntry(PathNodeConfig, "{}"))
	var buf bytes.Buffer
	w, err := NewWriter(&buf, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(PathSA818, strings.NewReader("x")); err == nil {
		t.Error("entry not in the manifest written")
	}
	if err := w.Add(PathNodeConfig, strings.NewReader("{changed}")); err == nil {
		t.Error("changed body accepted")
	}
	if err := w.Close(); err == nil {
		t.Error("closed with a missing entry")
	}
}

func TestManifestValidate(t *testing.T) {
	node := fileEntry(PathNodeConfig, "{}")
	for _, tc := range []struct {
		name string
		edit func(m *Manifest)
		ok   bool
	}{
		{"valid", func(m *Manifest) {}, true},
		{"no database", func(m *Manifest) { m.Files = m.Files[1:] }, false},
		{"parts say no node", func(m *Manifest) { m.Parts.Node = false }, false},
		{"parts say sa818", func(m *Manifest) { m.Parts.SA818 = true }, false},
		{"unknown path", func(m *Manifest) { m.Files = append(m.Files, fileEntry("asterisk/etc/rpt.conf", "x")) }, false},
		{"not a file", func(m *Manifest) { m.Files[1].Type = "dir" }, false},
		{"duplicate", func(m *Manifest) { m.Files = append(m.Files, node) }, false},
		{"bad owner", func(m *Manifest) { m.Files[1].Owner = "ro ot;" }, false},
		{"bad mode", func(m *Manifest) { m.Files[1].Mode = "rwx" }, false},
		{"bad sha", func(m *Manifest) { m.Files[1].SHA256 = "abc" }, false},
		{"bad channel", func(m *Manifest) { m.Channel = "nightly" }, false},
		{"beta channel", func(m *Manifest) { m.Channel = "beta" }, true},
		{"control character", func(m *Manifest) { m.Callsign = "N0CALL\n" }, false},
		{"format 0", func(m *Manifest) { m.Format = 0 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := baseManifest(node)
			tc.edit(m)
			if err := m.Validate(); (err == nil) != tc.ok {
				t.Errorf("got %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestReadRejects(t *testing.T) {
	db := reg(PathNodespotDB, dbBody)
	good := fileEntry(PathNodeConfig, "x")
	cases := []struct {
		name string
		arc  []byte
	}{
		{"unknown path", rawArchive(t, baseManifest(fileEntry("usr/bin/evil", "x")), db, reg("usr/bin/evil", "x"))},
		{"traversal", rawArchive(t, baseManifest(), db, reg("node/../../evil", "x"))},
		{"absolute", rawArchive(t, baseManifest(fileEntry("/etc/passwd", "x")), db, reg("/etc/passwd", "x"))},
		{"symlink", rawArchive(t, baseManifest(good), db, rawEnt{hdr: tar.Header{Name: PathNodeConfig, Typeflag: tar.TypeSymlink, Linkname: "/etc/shadow"}})},
		{"device", rawArchive(t, baseManifest(good), db, rawEnt{hdr: tar.Header{Name: PathNodeConfig, Typeflag: tar.TypeChar}})},
		{"hardlink", rawArchive(t, baseManifest(good), db, rawEnt{hdr: tar.Header{Name: PathNodeConfig, Typeflag: tar.TypeLink, Linkname: "/etc/shadow"}})},
		{"sha mismatch", rawArchive(t, baseManifest(good), db, reg(PathNodeConfig, "y"))},
		{"size larger than manifest", rawArchive(t, baseManifest(good), db, reg(PathNodeConfig, strings.Repeat("x", 100)))},
		{"not in manifest", rawArchive(t, baseManifest(good), db, reg(PathNodeConfig, "x"), reg(PathSA818, "x"))},
		{"missing entry", rawArchive(t, baseManifest(good), db)},
		{"duplicate entry", rawArchive(t, baseManifest(good), db, reg(PathNodeConfig, "x"), reg(PathNodeConfig, "x"))},
		{"setuid mode", rawArchive(t, baseManifest(Entry{Path: PathNodeConfig, Type: TypeFile, Size: 1, SHA256: sum("x"), Owner: "root", Group: "root", Mode: "4755"}), db, reg(PathNodeConfig, "x"))},
		{"manifest not json", func() []byte {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			_ = tw.WriteHeader(&tar.Header{Name: ManifestName, Typeflag: tar.TypeReg, Size: 3})
			_, _ = tw.Write([]byte("{x}"))
			_ = tw.Close()
			_ = gz.Close()
			return buf.Bytes()
		}()},
		{"no manifest first", func() []byte {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			_ = tw.WriteHeader(&tar.Header{Name: PathNodeConfig, Typeflag: tar.TypeReg, Size: 1})
			_, _ = tw.Write([]byte("x"))
			_ = tw.Close()
			_ = gz.Close()
			return buf.Bytes()
		}()},
		{"not gzip", []byte("hello world, not a backup")},
		{"empty", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Read(bytes.NewReader(c.arc), nil)
			if err == nil {
				t.Fatal("accepted")
			}
			var be *Error
			if !errors.As(err, &be) {
				t.Errorf("error %v is not a user-facing backup error", err)
			}
		})
	}
}

func TestReadFormats(t *testing.T) {
	for _, tc := range []struct {
		format int
		check  func(error) bool
	}{
		{1, func(err error) bool { return errors.Is(err, ErrAsteriskBackup) }},
		{3, func(err error) bool { var nf *NewerFormatError; return errors.As(err, &nf) && nf.Version == 3 }},
		{FormatVersion, func(err error) bool { return err == nil }},
	} {
		m := baseManifest()
		m.Format = tc.format
		// A format 1 manifest has fields this version does not know.
		raw := map[string]any{}
		b, _ := json.Marshal(m)
		_ = json.Unmarshal(b, &raw)
		if tc.format == 1 {
			raw["asl3Version"] = "3.0"
			raw["files"] = []any{map[string]any{"path": "asterisk/etc", "type": "dir"}}
		}
		_, err := Read(bytes.NewReader(rawArchive(t, raw, reg(PathNodespotDB, dbBody))), nil)
		if !tc.check(err) {
			t.Errorf("format %d: %v", tc.format, err)
		}
	}
}

func TestReadTruncated(t *testing.T) {
	body := strings.Repeat("abcdefgh", 4000)
	m := baseManifest(fileEntry(PathNodeConfig, body))
	arc := rawArchive(t, m, reg(PathNodespotDB, dbBody), reg(PathNodeConfig, body))
	for _, n := range []int{len(arc) - 1, len(arc) / 2, 30} {
		if _, err := Read(bytes.NewReader(arc[:n]), nil); err == nil {
			t.Errorf("truncated at %d accepted", n)
		}
	}
}

func TestReadDecompressionBomb(t *testing.T) {
	// The manifest promises 1 byte but the entry carries far more; the
	// reader must stop at the declared size, not expand it.
	m := baseManifest(fileEntry(PathNodeConfig, "x"))
	big := strings.Repeat("\x00", 8<<20)
	arc := rawArchive(t, m, reg(PathNodespotDB, dbBody), rawEnt{hdr: tar.Header{Name: PathNodeConfig, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(big))}, body: big})
	if _, err := Read(bytes.NewReader(arc), nil); err == nil {
		t.Fatal("accepted")
	}
}

func TestValidPath(t *testing.T) {
	for _, p := range Paths {
		if err := ValidPath(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"", "/a", "node/", "node//node.json", "node/./node.json", "node/../node.json", "a\x00b", "asterisk/etc/rpt.conf"} {
		if ValidPath(p) == nil {
			t.Errorf("path %q accepted", p)
		}
	}
}

func TestVersionNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"2.1.0", "2.0.9", true}, {"2.0.0", "2.0.0", false}, {"1.9.9", "2.0.0", false}, {"2.0.0~beta1", "2.0.0", false}, {"", "1.0", false}, {"dev", "1.0", false}, {"1.0", "dev", false}} {
		if got := VersionNewer(c.a, c.b); got != c.want {
			t.Errorf("VersionNewer(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

// A handler that copies the body (as the services do) fails with the raw
// gzip error on a truncated upload; Read must still report a bad backup.
func TestReadTruncatedWithHandlerIsBadBackup(t *testing.T) {
	rb := make([]byte, 100000)
	rand.New(rand.NewSource(1)).Read(rb) // incompressible, so cutting the gzip lands inside the file
	body := string(rb)
	m := baseManifest(fileEntry(PathNodeConfig, body))
	arc := rawArchive(t, m, reg(PathNodespotDB, dbBody), reg(PathNodeConfig, body))
	h := func(e Entry, r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	}
	_, err := Read(bytes.NewReader(arc[:len(arc)/2]), h)
	var be *Error
	if !errors.As(err, &be) {
		t.Fatalf("want *Error, got %T %v", err, err)
	}
}
