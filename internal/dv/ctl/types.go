// Package ctl is the control contract between the nodespot server and nodespot-dv:
// newline-delimited JSON over the unix socket /run/nodespot-dv/ctl.sock.
package ctl

import (
	"encoding/json"
	"strings"
	"time"
)

const (
	SocketPath    = "/run/nodespot-dv/ctl.sock"
	ConfigPath    = "/var/lib/nodespot-dv/config.json"
	ConfigVersion = 1
	MaxLine       = 64 << 10
)

// Ops.
const (
	OpPing      = "ping"
	OpStatus    = "status"
	OpReload    = "reload"    // re-read ConfigPath (the "apply config" request)
	OpMode      = "mode"      // ModeArgs
	OpTune      = "tune"      // TuneArgs
	OpUnlink    = "unlink"    // UnlinkArgs
	OpSubscribe = "subscribe" // connection becomes an event stream
)

// Error codes.
const (
	CodeBadRequest   = "bad_request"
	CodeForbidden    = "forbidden"
	CodeBadConfig    = "bad_config"
	CodeNotAvailable = "not_available" // network not implemented in this build
	CodeNotEnabled   = "not_enabled"   // network disabled in config
	CodeBusy         = "busy"          // a call is in progress and the op would cut it
	CodeLoginFailed  = "login_failed"  // an on-demand network refused the login or did not answer in time
	CodeInternal     = "internal"
)

type Request struct {
	ID   uint64          `json:"id"`
	Op   string          `json:"op"`
	Args json.RawMessage `json:"args,omitempty"`
}

type Response struct {
	ID     uint64          `json:"id"`
	OK     bool            `json:"ok"`
	Code   string          `json:"code,omitempty"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"` // Status for status/subscribe/mode/tune/unlink/reload
}

// Mode ids. M1 implements ModeLoop only.
const (
	ModeNone = ""
	ModeLoop = "LOOP"
	ModeBM   = "BM"
	ModeTGIF = "TGIF"
	ModeDMR  = "DMR"
	ModeYSF  = "YSF"
	ModeP25  = "P25"
	ModeNXDN = "NXDN"
	ModeM17  = "M17"
)

// TGIFAddress and TGIFPort are the built-in TGIF master, used when the DMR
// host list is missing or has no TGIF entry.
const (
	TGIFAddress = "tgif.network"
	TGIFPort    = 62031
)

// DefaultHomebrewPassword is the login password of a DMR - Other master when
// none is set: the value most Homebrew masters ship with. BrandMeister and
// TGIF have no such default.
const DefaultHomebrewPassword = "passw0rd"

// EchoTestCall is the source callsign of the echo test's playback call (mode
// LOOP, shown as "Echo test" in the panel). It is not a valid callsign (a space),
// so it never collides with a station.
const EchoTestCall = "ECHO TEST"

// EchoTestName is how the panel names that caller and the mode (Last heard,
// Now calling).
const EchoTestName = "Echo test"

// IsEchoTest reports whether t is the echo test's playback call.
func IsEchoTest(t Talk) bool { return t.Mode == ModeLoop && t.Dir == "rx" && t.Call == EchoTestCall }

type ModeArgs struct {
	Mode  string `json:"mode"`
	Force bool   `json:"force,omitempty"` // cut an active call
}

// Target is where a tune points. For DMR-family modes TG (and Private) are
// used; for YSF/P25/NXDN the server resolves the host list entry first.
type Target struct {
	TG      uint32 `json:"tg,omitempty"`
	Slot    int    `json:"slot,omitempty"` // 1 or 2; 0 = network default
	Private bool   `json:"private,omitempty"`
	Room    string `json:"room,omitempty"` // YSF reflector id / P25,NXDN TG as text
	Name    string `json:"name,omitempty"`
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
}

type TuneArgs struct {
	Mode   string `json:"mode"`
	Target Target `json:"target"`
	Force  bool   `json:"force,omitempty"`
}

type UnlinkArgs struct {
	Mode string `json:"mode,omitempty"` // "" = current
	// Release leaves the network the way a mode switch does: the target is
	// unlinked and an on-demand session (BrandMeister) is stopped unless
	// "Stay logged in" is on. The mode stays. An engine older than this field
	// refuses the request as a bad one; callers then send it without Release.
	Release bool `json:"release,omitempty"`
}

type LoginState string

const (
	LoginOff     LoginState = "off"
	LoginPending LoginState = "pending"
	LoginOK      LoginState = "ok"
	LoginFailed  LoginState = "failed"
	// LoginIdle: an on-demand network (NetConfig.OnDemand) that is not the
	// active mode. It has no session on purpose, so it is not a failure.
	LoginIdle LoginState = "idle"
)

// DetailNoReply starts the Detail of a pending or failed network login whose
// reflector (YSF, P25, NXDN) has sent nothing for the link timeout: no poll
// reply, no voice. The dashboard shows it as "Reflector not answering".
const DetailNoReply = "no reply from "

// NotAnswering reports whether a network's login and detail say that its
// reflector does not answer (the engine's lost state, DetailNoReply).
func NotAnswering(l LoginState, detail string) bool {
	return (l == LoginPending || l == LoginFailed) && strings.HasPrefix(detail, DetailNoReply)
}

type NetStatus struct {
	Mode      string     `json:"mode"`
	Available bool       `json:"available"` // implemented in this build
	Enabled   bool       `json:"enabled"`   // enabled in config
	Login     LoginState `json:"login"`
	Detail    string     `json:"detail,omitempty"`
}

type VocoderStatus struct {
	OK       bool   `json:"ok"`
	Addr     string `json:"addr"`
	Frames   uint64 `json:"frames"`
	Timeouts uint64 `json:"timeouts"`
}

type Status struct {
	Version  string        `json:"version"`
	Started  time.Time     `json:"started"`
	Mode     string        `json:"mode"`
	Target   *Target       `json:"target,omitempty"`
	Updated  time.Time     `json:"updated"`
	USRP     USRPStatus    `json:"usrp"`
	Vocoder  VocoderStatus `json:"vocoder"`
	Networks []NetStatus   `json:"networks"`
	Talk     *Talk         `json:"talk,omitempty"` // call in progress
	Audio    *AudioStatus  `json:"audio,omitempty"`
}

// Audio verdicts (AudioSummary.Verdict).
const (
	VerdictLow  = "low"
	VerdictOK   = "ok"
	VerdictHigh = "high"
)

// AudioStatus is the level meter of the two audio directions, measured in the
// bridge after the static gain and before and after the AGC. Levels are dBFS;
// silence reads -90.
type AudioStatus struct {
	TargetDb int      `json:"targetDb"` // the configured AGC target (the verdict uses it even with the AGC off)
	Tx       AudioDir `json:"tx"`       // node -> network
	Rx       AudioDir `json:"rx"`       // network -> node
}

// AudioDir is one direction. The levels are live (they fall away when audio
// stops, like a meter); Last is the summary of the previous transmission.
type AudioDir struct {
	AGC       bool          `json:"agc"`    // the AGC is on for this direction
	Active    bool          `json:"active"` // audio is flowing now
	PeakDb    float64       `json:"peakDb"`
	RmsDb     float64       `json:"rmsDb"`
	OutPeakDb float64       `json:"outPeakDb"` // after the AGC (the same as PeakDb when it is off)
	OutRmsDb  float64       `json:"outRmsDb"`
	Last      *AudioSummary `json:"last,omitempty"`
}

// AudioSummary is one finished transmission (or received call).
type AudioSummary struct {
	SpeechDb    float64   `json:"speechDb"`    // mean level of the speech frames, before the AGC
	OutSpeechDb float64   `json:"outSpeechDb"` // the same after the AGC
	PeakDb      float64   `json:"peakDb"`      // highest peak, before the AGC
	OutPeakDb   float64   `json:"outPeakDb"`
	Clipped     int       `json:"clipped"`    // frames with samples at or near full scale, before the AGC
	DurationMs  int64     `json:"durationMs"` // of the audio
	Verdict     string    `json:"verdict"`    // VerdictLow, VerdictOK or VerdictHigh
	AGC         bool      `json:"agc"`        // the AGC was on
	At          time.Time `json:"at"`
}

type USRPStatus struct {
	Listen   string    `json:"listen"` // 127.0.0.1:34001
	Peer     string    `json:"peer"`   // 127.0.0.1:32001
	LastRx   time.Time `json:"lastRx,omitempty"`
	RxFrames uint64    `json:"rxFrames"`
	TxFrames uint64    `json:"txFrames"`
}

// Talk is one call. Dir rx = from the network to the node, tx = from the node.
type Talk struct {
	ID       uint64        `json:"id"`
	Dir      string        `json:"dir"` // "rx" | "tx"
	Mode     string        `json:"mode"`
	Call     string        `json:"call,omitempty"`
	DMRID    uint32        `json:"dmrId,omitempty"`
	TG       uint32        `json:"tg,omitempty"`
	Room     string        `json:"room,omitempty"`
	Slot     int           `json:"slot,omitempty"`
	Private  bool          `json:"private,omitempty"`
	Start    time.Time     `json:"start"`
	End      time.Time     `json:"end,omitempty"`
	Duration time.Duration `json:"durationNs,omitempty"`
}

// Event types.
const (
	EvStatus    = "status"     // Status changed (mode/target/network)
	EvTalkStart = "talk_start" // Talk
	EvTalkEnd   = "talk_end"   // Talk with End and Duration
	EvLogin     = "login"      // Login
	EvVocoder   = "vocoder"    // VocoderStatus
	EvPing      = "ping"
	EvOverflow  = "overflow" // subscriber too slow; server closes
)

type Event struct {
	Type    string         `json:"type"`
	At      time.Time      `json:"at"`
	Status  *Status        `json:"status,omitempty"`
	Talk    *Talk          `json:"talk,omitempty"`
	Login   *LoginEvent    `json:"login,omitempty"`
	Vocoder *VocoderStatus `json:"vocoder,omitempty"`
}

type LoginEvent struct {
	Mode   string     `json:"mode"`
	State  LoginState `json:"state"`
	Detail string     `json:"detail,omitempty"`
}

// Config is ConfigPath, rendered by nodespot-helper (dv-config), mode 0600 nodespot-dv.
type Config struct {
	Version  int           `json:"version"`
	USRP     USRPConfig    `json:"usrp"`
	Vocoder  VocoderConfig `json:"vocoder"`
	Identity Identity      `json:"identity"`
	Networks []NetConfig   `json:"networks"`
	Ctl      CtlConfig     `json:"ctl"`
	Resume   *TuneArgs     `json:"resume,omitempty"` // tune at start (written by the server via helper in M2; ignored if nil)
}

type USRPConfig struct {
	Listen string `json:"listen"`             // "127.0.0.1:34001"
	Peer   string `json:"peer"`               // "127.0.0.1:32001"
	GainRx int    `json:"gainRxDb,omitempty"` // -20..20
	GainTx int    `json:"gainTxDb,omitempty"`
	// The AGC, shared by all modes (docs/DV.md "Audio level and AGC"). AGCTx
	// works on the node's audio going to the network, AGCRx on the network's
	// audio going to the node. AGCRx is on by default and AGCTx off. A file
	// without agcRx, agcTargetDb and agcMaxGainDb gets the defaults
	// (config.Parse). agcRx is always written, so that an explicit false
	// survives a round trip instead of falling back to on.
	AGCTx     bool `json:"agcTx,omitempty"`
	AGCRx     bool `json:"agcRx"`
	AGCTarget int  `json:"agcTargetDb,omitempty"` // -40..-6, default -22
	AGCMax    int  `json:"agcMaxGainDb"`          // 0..30, default 18
}

// AGC defaults and limits.
const (
	DefaultAGCTarget = -22
	DefaultAGCMax    = 18
	DefaultAGCRx     = true // network audio AGC (to the node) is on by default, the node audio AGC (to the network) off
	MinAGCTarget     = -40
	MaxAGCTarget     = -6
	MaxAGCMaxGain    = 30
	MaxGainDb        = 20 // the static gains are -20..20
)

type VocoderConfig struct {
	MD380     string `json:"md380"`     // "127.0.0.1:2470"
	TimeoutMS int    `json:"timeoutMs"` // default 60
}

type Identity struct {
	Callsign   string `json:"callsign,omitempty"` // tests: N0CALL
	DMRID      uint32 `json:"dmrId,omitempty"`    // tests: 1234567
	RepeaterID uint32 `json:"repeaterId,omitempty"`
}

// NetConfig carries secrets: never logged, never in Status.
type NetConfig struct {
	Mode     string            `json:"mode"`
	Enabled  bool              `json:"enabled"`
	Address  string            `json:"address,omitempty"`
	Port     int               `json:"port,omitempty"`
	Password string            `json:"password,omitempty"`
	Options  map[string]string `json:"options,omitempty"`
	// OnDemand: the network's session runs only while it is the active mode
	// (entering the mode logs in and waits for it, leaving unlinks and ends
	// the session), so the account is free for other clients in between.
	// nil: the mode's default (config.Validate fills it: on for BM, unless
	// the option stay_logged_in is "true"; off for the others).
	OnDemand *bool `json:"onDemand,omitempty"`
}

// OptStayLoggedIn is the BM option that keeps its session open outside BM mode.
const OptStayLoggedIn = "stay_logged_in"

// WantsOnDemand is the default of NetConfig.OnDemand for a network: only
// BrandMeister is on demand, because it allows one session per DMR ID.
func WantsOnDemand(c NetConfig) bool {
	return c.Mode == ModeBM && c.Options[OptStayLoggedIn] != "true"
}

type CtlConfig struct {
	Socket    string `json:"socket,omitempty"`
	AllowUIDs []int  `json:"allowUids,omitempty"` // tests only; default root + user nodespot
}
