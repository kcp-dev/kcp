/*
Copyright 2022 The KCP Authors.

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

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
)

type requestAuthenticator struct {
	groups []string
}

func (a *requestAuthenticator) AuthenticateRequest(*http.Request) (*authenticator.Response, bool, error) {
	return &authenticator.Response{
		User: &user.DefaultInfo{
			Name:   "system:unsecured",
			Groups: a.groups,
		},
	}, true, nil
}

func TestGroupFilter(t *testing.T) {
	for _, tc := range []struct {
		name                                     string
		passOnGroups, dropGroups                 sets.String
		passOnGroupsPrefixes, dropGroupsPrefixes []string
		requestedGroups                          []string
		wantGroups                               []string
	}{
		{
			name:       "no groups",
			wantGroups: []string{},
		},
		{
			name: "pass all",

			requestedGroups: []string{"foo", "bar", "baz"},
			wantGroups:      []string{"bar", "baz", "foo"},
		},
		{
			name:         "pass groups",
			passOnGroups: sets.NewString("foo"),

			requestedGroups: []string{"foo", "bar", "baz"},
			wantGroups:      []string{"foo"},
		},
		{
			name:                 "pass groups prefixes",
			passOnGroupsPrefixes: []string{"bar"},

			requestedGroups: []string{"foo", "foo2", "bar1", "bar2", "baz"},
			wantGroups:      []string{"bar1", "bar2"},
		},
		{
			name:                 "pass groups and pass groups prefixes",
			passOnGroups:         sets.NewString("foo"),
			passOnGroupsPrefixes: []string{"bar"},

			requestedGroups: []string{"foo", "foo2", "bar1", "bar2", "baz"},
			wantGroups:      []string{"bar1", "bar2", "foo"},
		},
		{
			name:       "drop groups",
			dropGroups: sets.NewString("foo"),

			requestedGroups: []string{"foo", "foo2", "bar1", "bar2", "baz"},
			wantGroups:      []string{"bar1", "bar2", "baz", "foo2"},
		},
		{
			name:               "drop groups prefixes",
			dropGroupsPrefixes: []string{"baz"},

			requestedGroups: []string{"foo", "foo2", "bar1", "bar2", "baz", "baz1", "baz2"},
			wantGroups:      []string{"bar1", "bar2", "foo", "foo2"},
		},
		{
			name:               "drop groups and drop groups prefixes",
			dropGroups:         sets.NewString("foo"),
			dropGroupsPrefixes: []string{"baz"},

			requestedGroups: []string{"foo", "foo2", "bar1", "bar2", "baz", "baz1", "baz2"},
			wantGroups:      []string{"bar1", "bar2", "foo2"},
		},
		{
			name:                 "drop takes precedence",
			passOnGroups:         sets.NewString("foo"),
			passOnGroupsPrefixes: []string{"bar", "foo"},
			dropGroups:           sets.NewString("foo"),
			dropGroupsPrefixes:   []string{"baz"},

			requestedGroups: []string{"foo", "foo2", "bar1", "bar2", "baz", "baz1", "baz2"},
			wantGroups:      []string{"bar1", "bar2", "foo2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			filter := &GroupFilter{
				Authenticator:       &requestAuthenticator{groups: tc.requestedGroups},
				PassOnGroups:        tc.passOnGroups,
				DropGroups:          tc.dropGroups,
				PassOnGroupPrefixes: tc.passOnGroupsPrefixes,
				DropGroupPrefixes:   tc.dropGroupsPrefixes,
			}
			res, gotAuthenticated, err := filter.AuthenticateRequest(&http.Request{})
			require.NoError(t, err)
			require.True(t, gotAuthenticated)
			require.Equal(t, tc.wantGroups, res.User.GetGroups())
		})
	}

}
