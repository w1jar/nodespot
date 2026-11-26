// Package node is the native node: one radio, exactly one active mode.
//
// The radio is separate from the networks. Whatever mode is active, its
// audio goes out to the radio and the radio's audio goes out to it; the
// node never bridges one network to another. The node owns everything that
// happens on the radio side: the simplex logic (COS, PTT while the mode
// talks, hang, transmit timeout), the two AGCs and the
// station ID, which is sent on the radio only and never to a mode.
//
// The engine runs on a 20 ms clock ([Engine.Run] calls [Engine.Step]);
// tests drive Step directly with a fake radio, fake modes and fake times.
// The control API ([Server]) is JSON lines on a Unix socket.
package node
