package backupsvc

import (
	"strings"
	"testing"

	"github.com/w1jar/nodespot/internal/backup"
	"github.com/w1jar/nodespot/internal/helper"
	"github.com/w1jar/nodespot/internal/store"
)

func previewFor(m *backup.Manifest, plan helper.RestoreResult, sa818 []byte) Preview {
	s := &Service{}
	return s.preview(m, plan, store.Inspection{}, sa818)
}

func hasWarning(p Preview, code string) (string, bool) {
	for _, w := range p.Warnings {
		if w.Code == code {
			return w.Message, true
		}
	}
	return "", false
}

func TestPreviewWarnings(t *testing.T) {
	node := backup.Parts{Node: true, Secrets: true}
	for _, tc := range []struct {
		name  string
		m     *backup.Manifest
		plan  helper.RestoreResult
		want  []string // warning codes expected
		never []string // warning codes not expected
	}{
		{"cross release", &backup.Manifest{OSVersionCodename: "bookworm", Parts: node},
			helper.RestoreResult{OSCodename: "trixie"}, []string{"os_release"}, []string{"no_secrets", "missing_secrets"}},
		{"same release", &backup.Manifest{OSVersionCodename: "trixie", Parts: node},
			helper.RestoreResult{OSCodename: "trixie"}, nil, []string{"os_release"}},
		{"unknown backup release", &backup.Manifest{Parts: node},
			helper.RestoreResult{OSCodename: "trixie"}, nil, []string{"os_release"}},
		{"other arch", &backup.Manifest{Arch: "amd64", Parts: node},
			helper.RestoreResult{Arch: "arm64"}, []string{"arch"}, nil},
		{"without passwords", &backup.Manifest{Parts: backup.Parts{Node: true}},
			helper.RestoreResult{}, []string{"no_secrets"}, []string{"missing_secrets"}},
		{"passwords missing everywhere", &backup.Manifest{Parts: backup.Parts{Node: true}},
			helper.RestoreResult{MissingSecrets: []string{"EchoLink"}}, []string{"no_secrets", "missing_secrets"}, nil},
		{"no node settings", &backup.Manifest{}, helper.RestoreResult{}, []string{"no_node"}, []string{"no_secrets"}},
		{"skipped", &backup.Manifest{Parts: node, Skipped: []string{"sa818.json (unreadable)"}},
			helper.RestoreResult{}, []string{"skipped_files"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := previewFor(tc.m, tc.plan, nil)
			for _, c := range tc.want {
				if _, ok := hasWarning(p, c); !ok {
					t.Errorf("no %s warning: %+v", c, p.Warnings)
				}
			}
			for _, c := range tc.never {
				if _, ok := hasWarning(p, c); ok {
					t.Errorf("unexpected %s warning", c)
				}
			}
			if p.Modes == nil || p.MissingSecrets == nil || p.Users == nil || p.Checklist == nil {
				t.Errorf("nil list in %+v", p)
			}
		})
	}
}

func TestPreviewCrossReleaseMessage(t *testing.T) {
	p := previewFor(&backup.Manifest{OSVersionCodename: "bookworm"}, helper.RestoreResult{OSCodename: "trixie"}, nil)
	msg, _ := hasWarning(p, "os_release")
	if !strings.Contains(msg, "Debian 12 (bookworm)") || !strings.Contains(msg, "Debian 13 (trixie)") || strings.Contains(msg, "Asterisk") {
		t.Errorf("%q", msg)
	}
	if p.Backup.OSCodename != "bookworm" || p.Node.OSCodename != "trixie" {
		t.Errorf("os fields: %+v %+v", p.Backup, p.Node)
	}
}

func TestPreviewRadio(t *testing.T) {
	for _, tc := range []struct {
		name, json string
		want       *Radio
	}{
		{"narrow, no tone", `{"version":1,"settings":{"bandwidth":0,"rx_mhz":446.1,"tx_mhz":446.1,"squelch":4}}`,
			&Radio{Bandwidth: "narrow", RxMHz: 446.1, TxMHz: 446.1, RxTone: "none", TxTone: "none", Squelch: 4}},
		{"wide, tones", `{"version":1,"settings":{"bandwidth":1,"rx_mhz":145.5,"tx_mhz":145.5,` +
			`"rx_tone":{"kind":"ctcss","ctcss_hz":100},"tx_tone":{"kind":"dcs","dcs_code":23,"dcs_inverted":true}}}`,
			&Radio{Bandwidth: "wide", RxMHz: 145.5, TxMHz: 145.5, RxTone: "CTCSS 100.0", TxTone: "DCS 023I"}},
		{"unknown version", `{"version":9}`, nil},
		{"not json", `CURRENT_FREQ_RX=446.1`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseRadio([]byte(tc.json))
			if (got == nil) != (tc.want == nil) || (got != nil && *got != *tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
