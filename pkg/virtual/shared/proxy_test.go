/*
Copyright 2026 The kcp Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package shared

import (
	"net/http"
	"strings"
	"testing"

	"k8s.io/client-go/rest"
)

func TestImpersonationKey(t *testing.T) {
	t.Parallel()

	base := rest.ImpersonationConfig{
		UserName: "alice",
		UID:      "uid-1",
		Groups:   []string{"g1", "g2"},
		Extra:    map[string][]string{"scopes": {"a", "b"}},
	}

	t.Run("ensure output is stable", func(t *testing.T) {
		t.Parallel()

		got := impersonationKey(base)
		var b strings.Builder
		b.WriteString("u=alice")
		b.WriteString(impersonationKeySeparator)
		b.WriteString("uid=uid-1")
		b.WriteString(impersonationKeySeparator)
		b.WriteString("g=g1")
		b.WriteString(impersonationKeySeparator)
		b.WriteString("g=g2")
		b.WriteString(impersonationKeySeparator)
		b.WriteString("e=scopes=a")
		b.WriteString(impersonationKeySeparator)
		b.WriteString("e=scopes=b")
		want := b.String()
		if got != want {
			t.Errorf("impersonationKey = %q, want %q", got, want)
		}
	})

	tests := []struct {
		name  string
		a, b  rest.ImpersonationConfig
		equal bool
	}{
		{
			name:  "identical",
			a:     base,
			b:     base,
			equal: true,
		},
		{
			name:  "group order does not matter",
			a:     base,
			b:     rest.ImpersonationConfig{UserName: "alice", UID: "uid-1", Groups: []string{"g2", "g1"}, Extra: map[string][]string{"scopes": {"a", "b"}}},
			equal: true,
		},
		{
			name:  "extra value order does not matter",
			a:     base,
			b:     rest.ImpersonationConfig{UserName: "alice", UID: "uid-1", Groups: []string{"g1", "g2"}, Extra: map[string][]string{"scopes": {"b", "a"}}},
			equal: true,
		},
		{
			name:  "different UID",
			a:     base,
			b:     rest.ImpersonationConfig{UserName: "alice", UID: "uid-2", Groups: []string{"g1", "g2"}, Extra: map[string][]string{"scopes": {"a", "b"}}},
			equal: false,
		},
		{
			name:  "different user",
			a:     base,
			b:     rest.ImpersonationConfig{UserName: "bob", UID: "uid-1", Groups: []string{"g1", "g2"}, Extra: map[string][]string{"scopes": {"a", "b"}}},
			equal: false,
		},
		{
			name:  "different group",
			a:     base,
			b:     rest.ImpersonationConfig{UserName: "alice", UID: "uid-1", Groups: []string{"g1", "g3"}, Extra: map[string][]string{"scopes": {"a", "b"}}},
			equal: false,
		},
		{
			name:  "extra key vs value cannot collide",
			a:     rest.ImpersonationConfig{UserName: "alice", Extra: map[string][]string{"a": {"b"}}},
			b:     rest.ImpersonationConfig{UserName: "alice", Extra: map[string][]string{"a=b": nil}},
			equal: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ka, kb := impersonationKey(tc.a), impersonationKey(tc.b)
			if (ka == kb) != tc.equal {
				t.Errorf("impersonationKey equality = %v, want %v\n a=%q\n b=%q", ka == kb, tc.equal, ka, kb)
			}
		})
	}
}

func TestTransportCacheReusesTransport(t *testing.T) {
	t.Parallel()

	c := NewTransportCache(&rest.Config{Host: "https://example.com"})

	imp := rest.ImpersonationConfig{UserName: "alice", UID: "uid-1"}
	rt1, err := c.TransportFor(imp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rt2, err := c.TransportFor(imp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt1 != rt2 {
		t.Errorf("expected the same round-tripper for identical impersonation, got distinct instances")
	}

	other, err := c.TransportFor(rest.ImpersonationConfig{UserName: "bob", UID: "uid-2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rt1 == other {
		t.Errorf("expected distinct round-trippers for different impersonation identities")
	}
}

type roundTripperWithoutClose struct{}

func (roundTripperWithoutClose) RoundTrip(*http.Request) (*http.Response, error) { return nil, nil }

func TestCloseIdleConnections(t *testing.T) {
	t.Parallel()

	config := &rest.Config{Host: "https://example.com"}
	cache := NewTransportCache(config)
	impersonated, err := cache.TransportFor(rest.ImpersonationConfig{
		UserName: "alice",
		UID:      "uid-1",
		Groups:   []string{"g1"},
		Extra:    map[string][]string{"scopes": {"a"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tests := []struct {
		name string
		rt   http.RoundTripper
		want bool
	}{
		{name: "bare http.Transport", rt: &http.Transport{}, want: true},
		{name: "impersonating transport from TransportFor", rt: impersonated, want: true},
		{name: "round-tripper without CloseIdleConnections", rt: roundTripperWithoutClose{}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := closeIdleConnections(tc.rt); got != tc.want {
				t.Errorf("closeIdleConnections() = %v, want %v", got, tc.want)
			}
		})
	}
}
