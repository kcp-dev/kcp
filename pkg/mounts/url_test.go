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

package mounts

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateURL(t *testing.T) {
	t.Parallel()

	valid := []string{
		"https://example.com",
		"https://example.com/",
		"https://example.com:6443/clusters/root:org:ws",
		"https://10.0.0.1:443/services/custom-url/proxy",
		"https://[::1]:8443/base",
	}
	for _, raw := range valid {
		u, err := ValidateURL(raw)
		require.NoErrorf(t, err, "%q must be accepted", raw)
		require.Equal(t, "https", u.Scheme)
	}

	invalid := map[string]string{
		"":                                    "must not be empty",
		"http://example.com":                  "scheme must be https",
		"HTTP://example.com":                  "scheme must be https",
		"ftp://example.com":                   "scheme must be https",
		"example.com/clusters/root":           "scheme must be https",
		"/clusters/root":                      "scheme must be https",
		"https://":                            "host must be set",
		"https:///path":                       "host must be set",
		"https:example.com":                   "must be an absolute URL",
		"https://user:pass@example.com":       "must not contain user info",
		"https://user@example.com":            "must not contain user info",
		"https://example.com/path?watch=true": "must not contain a query",
		"https://example.com/path?":           "must not contain a query",
		"https://example.com/path#frag":       "must not contain a fragment",
		"https://exa mple.com":                "invalid character",
		"https://example.com/%zz":             "invalid URL escape",
	}
	for raw, want := range invalid {
		_, err := ValidateURL(raw)
		require.Errorf(t, err, "%q must be rejected", raw)
		require.ErrorContainsf(t, err, want, "%q", raw)
	}
}
