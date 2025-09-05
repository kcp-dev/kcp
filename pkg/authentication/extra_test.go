/*
Copyright 2025 The KCP Authors.

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

func TestExtraFilter(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                 string
		allowExtraKeys       []string
		dropExtraKeyContains []string
		requestedExtra       map[string][]string
		wantExtra            map[string][]string
	}{
		{
			name:      "no extra",
			wantExtra: map[string][]string{},
		},
		{
			name: "pass all",

			requestedExtra: map[string][]string{
				"foo":    {"bar"},
				"foobar": {"baz"},
				"barfoo": {"baz2"},
			},
			wantExtra: map[string][]string{
				"foo":    {"bar"},
				"foobar": {"baz"},
				"barfoo": {"baz2"},
			},
		},
		{
			name: "drop contains",

			dropExtraKeyContains: []string{"bar"},
			requestedExtra: map[string][]string{
				"foo":    {"bar"},
				"foobar": {"baz"},
				"barfoo": {"baz2"},
			},
			wantExtra: map[string][]string{
				"foo": {"bar"},
			},
		},
		{
			name: "allow takes precedence over drop contains",

			allowExtraKeys:       []string{"barfoo"},
			dropExtraKeyContains: []string{"bar"},
			requestedExtra: map[string][]string{
				"foo":    {"bar"},
				"foobar": {"baz"},
				"barfoo": {"baz2"},
			},
			wantExtra: map[string][]string{
				"foo":    {"bar"},
				"barfoo": {"baz2"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filter := &ExtraFilter{
				Authenticator:        &requestAuthenticator{extra: tc.requestedExtra},
				AllowExtraKeys:       tc.allowExtraKeys,
				DropExtraKeyContains: tc.dropExtraKeyContains,
			}
			res, gotAuthenticated, err := filter.AuthenticateRequest(&http.Request{})
			require.NoError(t, err)
			require.True(t, gotAuthenticated)
			require.Equal(t, tc.wantExtra, res.User.GetExtra())
		})
	}
}
