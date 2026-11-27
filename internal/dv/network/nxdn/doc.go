// Package nxdn is the NXDN network (ctl.ModeNXDN): a client for NXDN
// reflectors using the protocol NXDNGateway speaks towards them (references:
// g4klx NXDNClients NXDNGateway/NXDNNetwork.cpp and NXDNGateway.cpp,
// NXDNParrot/NXDNNetwork.cpp, MMDVMHost NXDNControl.cpp; the payload layers
// are in internal/dv/nxdn). It registers itself for ctl.ModeNXDN.
//
// Wire formats (UDP, one datagram per packet):
//
//	NXDNP  "NXDNP"+callsign(10, space padded)+TG(2, big endian) = 17 bytes
//	       poll; it is also the link request. Sent three times when linking
//	       and then every 5 s. Reflectors (and NXDNParrot) echo it back.
//	NXDNU  same layout with "NXDNU": unlink. Sent three times.
//	NXDND  "NXDND"+src ID(2)+dst TG(2)+flags(1)+33-byte payload = 43 bytes,
//	       flags 0x01 group, 0x02 data, 0x04 start (VCALL), 0x08 end (TX_REL).
//	       The payload layout and how four AMBE+2 frames sit in it are
//	       documented in internal/dv/nxdn (doc.go, payload.go).
//
// Only IPv4 is used. Datagrams from any address other than the tuned
// reflector, and datagrams with a size other than 17 (NXDNP) or 43 (NXDND),
// are dropped (as NXDNGateway does).
//
// # Voice
//
// Every NXDND voice packet is 80 ms and carries four AMBE+2 frames of 49 bits;
// each is handed to the sink as one 7-byte network.VoiceFrame (vocoder layout,
// no transcoding). A call starts with a VCALL header packet; a voice packet
// that arrives with no call open starts one (late entry, source and
// destination are in the datagram header). A call ends on a TX_REL packet, on
// the end flag, when a VCALL from another source arrives, or after rx_timeout_ms
// without packets. Data calls are ignored. Only packets whose destination is
// the tuned TG are accepted. The source is reported as its decimal unit ID in
// CallHeader.Src (there is no ID database here).
//
// A local transmission sends the header (VCALL, flags group|start), one voice
// packet for every four frames written (the last group is padded with the
// silence frame) and a trailer (TX_REL, flags group|end). The SACCH of the
// voice packets carries the VCALL message in four parts, so receivers can late
// enter. Transmission needs nxdn_id.
//
// # Target and link state
//
// Tune: the TG is Target.TG, or Target.Room parsed as a decimal number
// (1..65535); host and port are Target.Host/Target.Port (resolved by the
// server from NXDN_Hosts.txt) and fall back to NetConfig.Address/Port, then
// port 41400. Tune unlinks the previous reflector (NXDNU) if there was one,
// then sends the poll burst. Unlink sends NXDNU and stops polling. When Run
// ends while linked an NXDNU is sent too.
//
// Login state: NXDN has no authentication, so "login" means the reflector
// answers. Pending from Tune until the first NXDNP (poll echo) or NXDND from
// the reflector; OK from then on; Pending again ("no reply from reflector, retrying") when
// nothing was heard for missed_polls poll intervals, and Failed ("no reply from reflector, retrying")
// when the first reply is still missing after that time. While not OK polls
// are retried with exponential backoff (backoff_min_ms doubling up to
// backoff_max_ms), then every poll_interval_ms once OK. Polling never stops
// until Unlink, so a returning reflector is picked up again.
//
// # Options (ctl.NetConfig.Options)
//
//	nxdn_id          this node's 16-bit unit ID (1..65535); required to transmit,
//	                 receiving works without it
//	nxdn_ran         RAN carried in the SACCH of transmitted packets, 0..63, default 0
//	listen           local UDP bind address, default "0.0.0.0:0"
//	poll_interval_ms default 5000
//	missed_polls     default 3: silence of this many poll intervals loses the link
//	backoff_min_ms   default 1000, first retry delay while not OK
//	backoff_max_ms   default 30000
//	rx_timeout_ms    default 1500, lost-end timeout of a received call
//
// The callsign sent in polls is ctl.Identity.Callsign (upper-cased, at most 10
// characters). NXDN has no password; NetConfig.Password is unused.
package nxdn
