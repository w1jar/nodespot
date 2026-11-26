package main

import (
	"net"
	"net/url"
)

func interfaceAddrs() []net.Addr {
	a, _ := net.InterfaceAddrs()
	return a
}

// firstNonLoopbackIPv4 returns the first non-loopback, non-link-local IPv4.
func firstNonLoopbackIPv4(addrs []net.Addr) string {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() && !ip4.IsUnspecified() {
			return ip4.String()
		}
	}
	return ""
}

// setupLink builds the one-time first-run setup URL for the listen address.
func setupLink(listen, code string, addrs []net.Addr) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "", "8080"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = firstNonLoopbackIPv4(addrs)
		if host == "" {
			host = "localhost"
		}
	}
	if port == "" {
		port = "8080"
	}
	return "http://" + net.JoinHostPort(host, port) + "/#/setup?code=" + url.QueryEscape(code)
}
