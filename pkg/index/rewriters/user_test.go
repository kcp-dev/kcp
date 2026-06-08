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

package rewriters

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestHomeClusterName ensures that HomeClusterName stays stable.
func TestHomeClusterName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"":                 "36ks9xj4a2n61",
		"alice":            "v1u4alow0iv9",
		"bob":              "2okdry95y5lkv",
		"user-with-dash":   "1e25681jymk1l",
		"user:with:colons": "2bqfj6xlthg3t",
		// sha224("user172")[:8] starts with 0x00; locks in the leading-zero-byte encoding.
		"user172": "0f9mt7goyif7",
	}

	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := HomeClusterName(name).String()
			assert.Equal(t, want, got)
		})
	}
}

func TestUserRewriter(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		in   []string
		want []string
	}{
		"user with suffix": {
			in:   []string{"user", "alice", "ws"},
			want: []string{"v1u4alow0iv9", "ws"},
		},
		"user without suffix": {
			in:   []string{"user", "alice"},
			want: []string{"v1u4alow0iv9"},
		},
		"non-user passthrough": {
			in:   []string{"root", "org", "ws"},
			want: []string{"root", "org", "ws"},
		},
		"user prefix only - passthrough": {
			in:   []string{"user"},
			want: []string{"user"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, UserRewriter(tc.in))
		})
	}
}
