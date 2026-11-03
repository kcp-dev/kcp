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

package framework

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPathMatchPrefixToStrip(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		path  string
		trail string
		want  string
	}{
		"trail":                     {path: "/services/admin/apis/v1", trail: "apis/v1", want: "/services/admin"},
		"trail with trailing slash": {path: "/services/admin/apis/v1/", trail: "apis/v1/", want: "/services/admin"},
		"no trail":                  {path: "/services/admin", want: "/services/admin"},
		"no trail, trailing slash":  {path: "/services/admin/", want: "/services/admin"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, PathMatch{Path: tc.path, Trail: tc.trail}.PrefixToStrip())
		})
	}
}

func TestPathMatchValue(t *testing.T) {
	t.Parallel()

	require.Empty(t, PathMatch{Path: "/x"}.Value("a"))

	m := NewPathMatch("/x/1", "", map[string]string{"a": "1"})
	require.Equal(t, "1", m.Value("a"))
	require.Empty(t, m.Value("b"))
}
