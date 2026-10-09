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

package authentication

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHasImpersonationHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{name: "no headers", header: http.Header{}},
		{name: "unrelated headers", header: http.Header{"Authorization": {"Bearer x"}, "X-Remote-User": {"alice"}}, want: false},
		{name: "user", header: http.Header{"Impersonate-User": {"admin"}}, want: true},
		{name: "group", header: http.Header{"Impersonate-Group": {"system:masters"}}, want: true},
		{name: "extra", header: http.Header{"Impersonate-Extra-Scopes": {"cluster:root"}}, want: true},
		{
			// The apiserver's impersonation filter acts on this, so it must not be
			// treated as an unimpersonated request.
			name:   "uid alone",
			header: http.Header{"Impersonate-Uid": {"1234"}},
			want:   true,
		},
		{name: "empty user value is not a request", header: http.Header{"Impersonate-User": {""}}, want: false},
		{name: "lowercase extra key in a hand-built map", header: http.Header{"impersonate-extra-scopes": {"cluster:root"}}, want: true},
		{name: "lowercase user key in a hand-built map", header: http.Header{"impersonate-user": {"admin"}}, want: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, HasImpersonationHeaders(tc.header))
		})
	}
}

func TestScrubImpersonationHeaders(t *testing.T) {
	t.Parallel()

	header := http.Header{
		"Impersonate-User":         {"admin"},
		"Impersonate-Uid":          {"1234"},
		"Impersonate-Group":        {"system:masters", "team:a"},
		"Impersonate-Extra-Scopes": {"cluster:root"},
		"impersonate-extra-other":  {"x"},
		"Authorization":            {"Bearer keep-me-here"},
		"X-Remote-User":            {"alice"},
	}

	ScrubImpersonationHeaders(header)

	require.False(t, HasImpersonationHeaders(header), "every impersonation header must be gone")
	for _, k := range []string{"Impersonate-User", "Impersonate-Uid", "Impersonate-Group", "Impersonate-Extra-Scopes", "impersonate-extra-other"} {
		require.Empty(t, header.Values(k), "header %q must be removed", k)
	}
	// Scrubbing impersonation is a separate concern from credentials and identity,
	// so this must leave both alone.
	require.Equal(t, []string{"Bearer keep-me-here"}, header.Values("Authorization"))
	require.Equal(t, []string{"alice"}, header.Values("X-Remote-User"))

	// Idempotent, and safe on an empty header map.
	ScrubImpersonationHeaders(header)
	ScrubImpersonationHeaders(http.Header{})
}
