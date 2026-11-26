package helper

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/w1jar/nodespot/internal/backup"
)

// Backup operations. backup-info is a plain request/response operation.
// backup-create and backup-restore are streaming operations: the request is
// still one small JSON object (at most 4 KiB, like the plain operations).
//
//   - backup-create answers with one JSON line, the BackupHeader, followed by
//     a plain tar stream with the node part of the backup (entries named as
//     in the header, content verified against the header's sha256) and EOF.
//     A failure before the header is an {"error": ...} line; a failure later
//     cuts the stream short, which the tar reader reports.
//   - backup-restore is followed by the gzip-compressed archive (the file the
//     user downloaded) on the same connection; the client half-closes its
//     side after the last byte. The answer is one JSON RestoreResult.
const (
	OpBackupInfo    = "backup-info"
	OpBackupCreate  = "backup-create"
	OpBackupRestore = "backup-restore"
)

// BackupRequest is the argument of backup-create.
type BackupRequest struct {
	// Secrets keeps the network passwords in the node config. Without it
	// every secret mode setting is left out of the backup.
	Secrets bool `json:"secrets"`
}

// BackupInfo is the result of backup-info: what this node has to back up.
type BackupInfo struct {
	// Node: the node config exists; Secrets: it holds passwords.
	Node    bool `json:"node"`
	Secrets bool `json:"secrets"`
	// SA818: the radio module's saved channel exists.
	SA818 bool `json:"sa818"`
}

// BackupHeader is the first line of a backup-create stream.
type BackupHeader struct {
	Arch       string         `json:"arch"`
	OSID       string         `json:"osId,omitempty"`
	OSCodename string         `json:"osCodename,omitempty"`
	OSName     string         `json:"osName,omitempty"`
	Channel    string         `json:"channel"`
	Callsign   string         `json:"callsign,omitempty"`
	Parts      backup.Parts   `json:"parts"`
	Skipped    []string       `json:"skipped"`
	Entries    []backup.Entry `json:"entries"`
}

// RestoreRequest is the argument of backup-restore.
type RestoreRequest struct {
	// DryRun validates the archive and returns the plan without changing
	// anything.
	DryRun bool `json:"dryRun"`
}

// RestoreStep is one step of an apply.
type RestoreStep struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// RestoreResult is the result of backup-restore.
type RestoreResult struct {
	DryRun bool          `json:"dryRun"`
	OK     bool          `json:"ok"`
	Error  string        `json:"error,omitempty"`
	Steps  []RestoreStep `json:"steps"`
	// The state of this node, for the warnings.
	Arch       string `json:"arch"`
	OSID       string `json:"osId,omitempty"`
	OSCodename string `json:"osCodename,omitempty"`
	OSName     string `json:"osName,omitempty"`
	// Callsign is this node's current station callsign.
	Callsign string `json:"callsign,omitempty"`
	// Modes are the modes the restored config sets up; MissingSecrets the
	// ones left out because the backup has no passwords for them and this
	// node has none either (they need setting up again).
	Modes          []string `json:"modes"`
	MissingSecrets []string `json:"missingSecrets"`
}

// isStreamOp reports whether op carries a byte stream.
func isStreamOp(op string) bool {
	return op == OpBackupCreate || op == OpBackupRestore || op == OpDiagCollect
}

// maxBackupHeader bounds the header line a client accepts.
const maxBackupHeader = 64 << 20

// BackupStream is an open backup-create answer: the header and the tar
// stream behind it. Close it when done.
type BackupStream struct {
	Header BackupHeader
	Body   io.Reader
	closer io.Closer
}

// Close releases the connection.
func (b *BackupStream) Close() error {
	if b.closer == nil {
		return nil
	}
	return b.closer.Close()
}

// ReadBackupStream parses the header line of a backup-create answer from r.
func ReadBackupStream(r io.Reader, closer io.Closer) (*BackupStream, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	line, err := readLine(br, maxBackupHeader)
	if err != nil {
		return nil, fmt.Errorf("helper: %w", err)
	}
	var e errorResponse
	if json.Unmarshal(line, &e) == nil && e.Error != "" {
		return nil, errors.New(e.Error)
	}
	var h BackupHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("helper: bad response: %w", err)
	}
	return &BackupStream{Header: h, Body: br, closer: closer}, nil
}

func readLine(br *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	for {
		chunk, err := br.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > max {
			return nil, errors.New("header too large")
		}
		if err == nil {
			return out, nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if len(out) > 0 && errors.Is(err, io.EOF) {
				return out, nil
			}
			return nil, err
		}
	}
}
