// Package homebrew is the MMDVM "Homebrew" repeater protocol client (as used
// by MMDVMHost, DMRGateway, hblink3 masters, FreeDMR, TGIF, DMR+ in Homebrew
// mode). It registers for both ctl.ModeTGIF and ctl.ModeDMR; each NetConfig
// builds its own instance with its own UDP socket, so several logins can run
// at once.
//
// Wire formats (all UDP, one datagram per packet; references: MMDVMHost
// DMRNetwork.cpp, hblink3 hblink.py, DMRGateway):
//
//	RPTL  "RPTL"+id(4)                                  login
//	RPTACK "RPTACK"+salt(4)                             master challenge
//	RPTK  "RPTK"+id(4)+SHA256(salt||password)(32)       authentication
//	RPTC  "RPTC"+id(4)+config(294) = 302 bytes          see Config layout in rptc.go
//	RPTO  "RPTO"+id(4)+options                          optional
//	RPTPING "RPTPING"+id(4) / MSTPONG "MSTPONG"+id(4)   keepalive, every 5 s
//	MSTNAK "MSTNAK"+id(4)                               relogin
//	MSTCL "MSTCL"+id(4) / RPTCL "RPTCL"+id(4)           close
//	DMRD  55-byte voice/data packet                     see dmrd.go
//
// Defaults: TGIF uses timeslot 2 and ignores the slot and options keys. With
// no NetConfig.Address it connects to tgif.network port 62031 (ctl.TGIFAddress,
// ctl.TGIFPort); the node does not read the DMR host list (hostlists.TGIFFromFile
// is not wired in: the node runs as another user and has no access to the web
// app's data directory). DMR has no default address; NetConfig.Address is
// required, and a port-less address uses 62031.
//
// Option keys (ctl.NetConfig.Options):
//
//	slot            1 or 2, default 2 (hotspot simplex style); TGIF is always 2
//	colour_code     0..15, default 1
//	options         RPTO string, e.g. "TS2=1997;"; omitted if empty; never sent for TGIF
//	repeater_id     overrides Identity.RepeaterID, then Identity.DMRID
//	rx_freq,tx_freq Hz for RPTC, default 0 (see rptc.go note)
//	location, description, url   RPTC text fields
//	tune_kerchunk   default true: Tune sends header+terminator to the TG
//	unlink_tg       default 4000: Unlink sends a kerchunk to this TG
//	ping_interval_ms      default 5000
//	login_timeout_ms      default 5000, per login step
//	backoff_min_ms/backoff_max_ms  default 1000 / 60000
//	rx_timeout_ms         default 1500, lost-end timeout of a received call
//	rx_any_slot           default true for DMR, false for TGIF: deliver received calls of both
//	                      slots (DMR+ masters move a talkgroup to its own slot); TX stays on slot
//	missed_pongs          default 3
//
// The password is never logged and never appears in Status.
// Received calls: the source is reported as its decimal DMR ID in
// CallHeader.Src (no ID database here). For TGIF any TG on the slot is
// delivered; CallHeader.TG reports it. With rx_any_slot (DMR) both slots are
// delivered and CallHeader.Slot is the slot the call arrived on; while a call
// is in progress, packets of another slot are ignored, so one call is never
// cut by a stray stream.
package homebrew
