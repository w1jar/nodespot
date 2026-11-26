// Package backup defines the backup archive format of docs/BACKUP.md
// (format 2) and the code that reads and validates it. Both the server and
// the root helper use this package: the helper never trusts the server's
// check and validates the archive again with Read.
//
// An archive is a gzip-compressed tar whose first entry is manifest.json.
// Every other entry is one of a few fixed files (the panel's database and
// config, the node's config and SA818 state), listed in the manifest with
// its size and sha256. Owner, group and mode come from the manifest; the tar
// headers are ignored apart from name, type and size.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/w1jar/nodespot/internal/debver"
)

// Format limits.
const (
	// FormatVersion 2 is the native node's archive (3.x). Format 1 was the
	// Asterisk node's (2.x); it cannot be restored.
	FormatVersion = 2
	ManifestName  = "manifest.json"

	// MaxUpload is the size limit of an uploaded or created archive.
	MaxUpload = 256 << 20
	// MaxExpanded bounds the total size of all files after decompression.
	MaxExpanded = 1 << 30
	// MaxEntries bounds the number of manifest entries.
	MaxEntries = 16

	maxManifest = 1 << 20
	maxPathLen  = 256
)

// TypeFile is the one entry type (kept in the manifest for clarity).
const TypeFile = "file"

// Archive locations of the parts.
const (
	PathNodespotDB   = "nodespot/nodespot.db"
	PathNodespotConf = "nodespot/nodespot.conf"
	PathNodeConfig   = "node/node.json"
	PathSA818        = "node/sa818.json"
)

// Paths lists every valid archive path.
var Paths = []string{PathNodespotDB, PathNodespotConf, PathNodeConfig, PathSA818}

// Entry is one file of the archive.
type Entry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Owner  string `json:"owner"`
	Group  string `json:"group"`
	Mode   string `json:"mode"`
}

// Parts says what the archive holds besides the panel's database.
type Parts struct {
	// Node: the node config (node.json); SA818: the radio's channel.
	Node  bool `json:"node"`
	SA818 bool `json:"sa818"`
	// Secrets: the node config keeps its network passwords. Without it
	// every password is blank, and a restore keeps the passwords already
	// saved on the node.
	Secrets bool `json:"secrets"`
}

// Manifest is manifest.json.
type Manifest struct {
	Format          int       `json:"format"`
	Created         time.Time `json:"created"`
	CreatedBy       string    `json:"createdBy"`
	NodespotVersion string    `json:"nodespotVersion"`
	Hostname        string    `json:"hostname"`
	Timezone        string    `json:"timezone"`
	Arch            string    `json:"arch"`
	// The Debian release of the backed-up node (os-release ID,
	// VERSION_CODENAME and PRETTY_NAME).
	OSID              string   `json:"osId,omitempty"`
	OSVersionCodename string   `json:"osCodename,omitempty"`
	OSPrettyName      string   `json:"osName,omitempty"`
	Callsign          string   `json:"callsign,omitempty"`
	Channel           string   `json:"channel"`
	Parts             Parts    `json:"parts"`
	Skipped           []string `json:"skipped,omitempty"`
	Files             []Entry  `json:"files"`
}

// Error is a problem with the archive; its message is safe to show.
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func bad(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// NewerFormatError means the archive has a format version this build does
// not know.
type NewerFormatError struct{ Version int }

func (e *NewerFormatError) Error() string {
	return fmt.Sprintf("the backup uses format %d, which this version of nodespot does not know; update nodespot first", e.Version)
}

// ErrAsteriskBackup is the error for a format 1 archive.
var ErrAsteriskBackup = &Error{Msg: "this backup was made by aslcp 2.x for an Asterisk node; nodespot runs its own node and cannot restore it (set the node up again, it takes one screen)"}

var (
	reName = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}\$?$`)
	reMode = regexp.MustCompile(`^0[0-7]{3}$`)
	reSHA  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidName reports whether s is a plausible user or group name.
func ValidName(s string) bool { return reName.MatchString(s) }

// ParseMode parses an octal permission string ("0640").
func ParseMode(s string) (uint32, error) {
	if !reMode.MatchString(s) {
		return 0, bad("invalid mode %q", s)
	}
	v, _ := strconv.ParseUint(s, 8, 32)
	return uint32(v), nil
}

// FormatMode formats permission bits as "0640".
func FormatMode(m uint32) string { return fmt.Sprintf("%04o", m&0o777) }

// ValidPath reports whether p is one of the archive's paths.
func ValidPath(p string) error {
	for _, v := range Paths {
		if p == v {
			return nil
		}
	}
	return bad("path %q is not part of the backup layout", p)
}

func validEntry(e *Entry) error {
	if err := ValidPath(e.Path); err != nil {
		return err
	}
	if e.Type != TypeFile {
		return bad("entry %q must be a regular file", e.Path)
	}
	if !reName.MatchString(e.Owner) || !reName.MatchString(e.Group) {
		return bad("invalid owner or group for %q", e.Path)
	}
	if _, err := ParseMode(e.Mode); err != nil {
		return bad("invalid mode for %q", e.Path)
	}
	if e.Size < 0 || e.Size > MaxExpanded || !reSHA.MatchString(e.SHA256) {
		return bad("invalid file entry %q", e.Path)
	}
	return nil
}

func textOK(s string) bool {
	if len(s) > 256 || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// Validate checks the manifest (format, every path, size, owner and mode,
// duplicates, totals). It does not look at archive content.
func (m *Manifest) Validate() error {
	if m.Format > FormatVersion {
		return &NewerFormatError{Version: m.Format}
	}
	if m.Format == 1 {
		return ErrAsteriskBackup
	}
	if m.Format != FormatVersion {
		return bad("unsupported backup format %d", m.Format)
	}
	if len(m.Files) > MaxEntries || len(m.Skipped) > 32 {
		return bad("too many entries")
	}
	for _, s := range append([]string{m.Hostname, m.Timezone, m.Arch, m.NodespotVersion, m.CreatedBy, m.OSID,
		m.OSVersionCodename, m.OSPrettyName, m.Callsign}, m.Skipped...) {
		if !textOK(s) {
			return bad("invalid text field in the manifest")
		}
	}
	if m.Channel != "" && m.Channel != "stable" && m.Channel != "beta" {
		return bad("invalid update channel %q", m.Channel)
	}
	seen := map[string]bool{}
	var total int64
	for i := range m.Files {
		e := &m.Files[i]
		if err := validEntry(e); err != nil {
			return err
		}
		if seen[e.Path] {
			return bad("duplicate entry %q", e.Path)
		}
		seen[e.Path] = true
		total += e.Size
		if total > MaxExpanded {
			return bad("the backup is larger than %d MiB when unpacked", MaxExpanded>>20)
		}
	}
	if !seen[PathNodespotDB] {
		return bad("the backup has no nodespot database")
	}
	if m.Parts.Node != seen[PathNodeConfig] || m.Parts.SA818 != seen[PathSA818] {
		return bad("the manifest's parts do not match its files")
	}
	return nil
}

// Total returns the sum of all file sizes.
func (m *Manifest) Total() int64 {
	var t int64
	for _, e := range m.Files {
		t += e.Size
	}
	return t
}

// Sort orders the entries by path.
func (m *Manifest) Sort() {
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
}

// Find returns the entry for path, or nil.
func (m *Manifest) Find(path string) *Entry {
	for i := range m.Files {
		if m.Files[i].Path == path {
			return &m.Files[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------- reading

// countReader fails once more than n bytes were read.
type countReader struct {
	r io.Reader
	n int64
}

var errExpanded = bad("the backup expands to more than its manifest declares (possible archive bomb)")

func (c *countReader) Read(p []byte) (int, error) {
	if c.n < 0 {
		return 0, errExpanded
	}
	if int64(len(p)) > c.n+1 {
		p = p[:c.n+1]
	}
	n, err := c.r.Read(p)
	c.n -= int64(n)
	if c.n < 0 {
		return n, errExpanded
	}
	return n, err
}

// Handler receives each file of an archive. body yields exactly e.Size
// bytes; the content is verified against the manifest after the handler
// returns (an error from Read means the archive is bad even if the handler
// already used the bytes, so handlers must write to scratch space only).
type Handler func(e Entry, body io.Reader) error

// Read validates a gzip-compressed archive completely and calls handle (may
// be nil) for every file. The manifest must be the first entry; every other
// entry must be in the manifest with matching size and sha256; every
// manifest entry must be present; nothing may expand beyond the manifest's
// total (decompression limit).
func Read(r io.Reader, handle Handler) (*Manifest, error) {
	cr := &countReader{r: r, n: int64(MaxUpload) * 2}
	gz, err := gzip.NewReader(cr)
	if err != nil {
		return nil, bad("not a gzip-compressed backup file")
	}
	defer gz.Close()
	// Budget for the decompressed stream, until the manifest is known.
	lim := &countReader{r: gz, n: maxManifest + 4096}
	tr := tar.NewReader(lim)
	hdr, err := tr.Next()
	if err != nil {
		return nil, wrapTar(err, "the archive is empty or damaged")
	}
	if hdr.Name != ManifestName || (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) {
		return nil, bad("the first entry of the archive is not manifest.json")
	}
	if hdr.Size > maxManifest {
		return nil, bad("manifest.json is too large")
	}
	raw, err := io.ReadAll(io.LimitReader(tr, hdr.Size))
	if err != nil {
		return nil, wrapTar(err, "manifest.json is damaged")
	}
	var fm struct {
		Format int `json:"format"`
	}
	if json.Unmarshal(raw, &fm) == nil {
		if fm.Format > FormatVersion {
			return nil, &NewerFormatError{Version: fm.Format}
		}
		if fm.Format == 1 {
			return nil, ErrAsteriskBackup
		}
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, bad("manifest.json is not valid")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	byPath := make(map[string]*Entry, len(m.Files))
	for i := range m.Files {
		byPath[m.Files[i].Path] = &m.Files[i]
	}
	// Tar headers (512 bytes, plus padding and PAX extensions) of every
	// entry are budgeted on top of the file contents.
	lim.n = m.Total() + int64(len(m.Files)+8)*(4096+maxPathLen*2)
	seen := make(map[string]bool, len(m.Files))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, wrapTar(err, "the archive is damaged or truncated")
		}
		e := byPath[hdr.Name]
		if e == nil {
			return nil, bad("the archive contains %q, which is not in the manifest", hdr.Name)
		}
		if seen[hdr.Name] {
			return nil, bad("duplicate archive entry %q", hdr.Name)
		}
		seen[hdr.Name] = true
		if (hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA) || hdr.Size != e.Size {
			return nil, bad("entry %q does not match the manifest", hdr.Name)
		}
		h := sha256.New()
		er := &errReader{r: io.LimitReader(tr, e.Size)}
		body := io.TeeReader(er, h)
		if handle != nil {
			if err := handle(*e, body); err != nil {
				if er.err != nil && er.err != io.EOF {
					// The handler failed because the archive itself could
					// not be read (truncated or corrupt gzip).
					return nil, wrapTar(er.err, "the archive is damaged or truncated")
				}
				return nil, err
			}
		}
		if _, err := io.Copy(io.Discard, body); err != nil {
			return nil, wrapTar(err, "the archive is damaged or truncated")
		}
		if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
			return nil, bad("checksum mismatch for %q", hdr.Name)
		}
	}
	for i := range m.Files {
		if !seen[m.Files[i].Path] {
			return nil, bad("the archive is truncated: %q is missing", m.Files[i].Path)
		}
	}
	// Consume the rest (gzip trailer, padding) inside the budget.
	if _, err := io.Copy(io.Discard, lim); err != nil {
		return nil, wrapTar(err, "the archive is damaged")
	}
	return &m, nil
}

// errReader remembers the first read error of the archive stream.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && e.err == nil {
		e.err = err
	}
	return n, err
}

func wrapTar(err error, msg string) error {
	var be *Error
	if errors.As(err, &be) {
		return be
	}
	return bad("%s", msg)
}

// ---------------------------------------------------------------- writing

// Writer writes an archive: gzip, tar, manifest first.
type Writer struct {
	gz   *gzip.Writer
	tw   *tar.Writer
	mod  time.Time
	seen map[string]bool
	m    *Manifest
}

// NewWriter starts an archive on w and writes the manifest (which must be
// valid and complete).
func NewWriter(w io.Writer, m *Manifest) (*Writer, error) {
	m.Sort()
	if err := m.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return nil, err
	}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	bw := &Writer{gz: gz, tw: tw, mod: m.Created, seen: map[string]bool{}, m: m}
	if err := tw.WriteHeader(&tar.Header{Name: ManifestName, Mode: 0o600, Size: int64(len(raw)), ModTime: m.Created, Format: tar.FormatPAX}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(raw); err != nil {
		return nil, err
	}
	return bw, nil
}

// Add writes the file named path from the manifest; body is checked against
// the manifest's size and sha256.
func (w *Writer) Add(path string, body io.Reader) error {
	e := w.m.Find(path)
	if e == nil {
		return fmt.Errorf("backup: %q is not in the manifest", path)
	}
	if w.seen[path] {
		return fmt.Errorf("backup: %q written twice", path)
	}
	w.seen[path] = true
	mode, _ := ParseMode(e.Mode)
	h := &tar.Header{Name: e.Path, Mode: int64(mode), ModTime: w.mod, Typeflag: tar.TypeReg, Size: e.Size}
	if err := w.tw.WriteHeader(h); err != nil {
		return err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(w.tw, sum), io.LimitReader(body, e.Size+1))
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(sum.Sum(nil)) != e.SHA256 {
		return fmt.Errorf("backup: %q changed while it was being read", e.Path)
	}
	return nil
}

// Close checks that every manifest entry was written and finishes the
// archive.
func (w *Writer) Close() error {
	for _, e := range w.m.Files {
		if !w.seen[e.Path] {
			return fmt.Errorf("backup: %q was not written", e.Path)
		}
	}
	if err := w.tw.Close(); err != nil {
		return err
	}
	return w.gz.Close()
}

// Sum returns the size and sha256 of data, for a manifest entry.
func Sum(data []byte) (int64, string) {
	h := sha256.Sum256(data)
	return int64(len(data)), hex.EncodeToString(h[:])
}

// VersionNewer reports whether version a is strictly newer than b by the
// dpkg algorithm. Either being empty or "dev" yields false.
func VersionNewer(a, b string) bool {
	if a == "" || b == "" || a == "dev" || b == "dev" || !debver.Valid(a) || !debver.Valid(b) {
		return false
	}
	return debver.Compare(a, b) > 0
}

// TrimText shortens s for a manifest text field.
func TrimText(s string) string {
	s = strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
	for len(s) > 256 {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}
