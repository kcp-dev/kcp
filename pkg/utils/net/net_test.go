package net

import (
	"net"
	"reflect"
	"testing"
)

func TestSplitAddrPort(t *testing.T) {
	var testCases = []struct {
		addr         string
		expectedAddr net.IP
		expectedPort int
		expectedErr  error
	}{
		{
			addr:         "127.0.0.1:8080",
			expectedAddr: net.ParseIP("127.0.0.1"),
			expectedPort: 8080,
		},
		{
			addr:         ":8080",
			expectedAddr: net.IPv4zero,
			expectedPort: 8080,
		},
		{
			addr:         "[::1]:8080",
			expectedAddr: net.ParseIP("::1"),
			expectedPort: 8080,
		},
		{
			addr:         "[2001:db8:1f70::999:de8:7648:6e8]:8080",
			expectedAddr: net.ParseIP("2001:db8:1f70::999:de8:7648:6e8"),
			expectedPort: 8080,
		},
		{
			addr:         "[::]:8080",
			expectedAddr: net.IPv6zero,
			expectedPort: 8080,
		},
		{
			addr:        "127.0.0.1",
			expectedErr: &net.AddrError{Err: "missing port in address", Addr: "127.0.0.1"},
		},
	}

	for i, tc := range testCases {
		actualAddr, actualPort, err := SplitAddrPort(tc.addr)
		if err != nil {
			if tc.expectedErr != nil {
				if !reflect.DeepEqual(tc.expectedErr, err) {
					t.Fatalf("test case %d. errors mismatch. expected: %s, actual: %s", i, tc.expectedErr, err)
				}
				continue
			}

			t.Fatalf("test case %d. unexpected error: %v", i, err)
		}

		if tc.expectedAddr.String() != actualAddr.String() {
			t.Errorf("test case %d. addresses mismatch. expected: %s, actual: %s", i, tc.expectedAddr, actualAddr)
		}

		if tc.expectedPort != actualPort {
			t.Errorf("test case %d. ports mismatch. expected: %d, actual: %d", i, tc.expectedPort, actualPort)
		}
	}
}
