package net

import (
	"net"
	"strconv"
)

// SplitAddrPort splits the given address into its IP address and port parts.
// It works with both IPv4 and IPv6 addresses.
func SplitAddrPort(addr string) (net.IP, int, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, -1, err
	}

	portInt, err := strconv.Atoi(port)
	if err != nil {
		return nil, -1, err
	}

	if addr := net.ParseIP(host); addr != nil {
		return addr, portInt, nil
	}

	return net.IPv4zero, portInt, nil
}
