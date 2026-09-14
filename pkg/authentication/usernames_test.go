/*
Copyright 2022 The kcp Authors.

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
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
)

// staticAuthenticator returns a fixed result, so tests can drive
// ForbidSystemUsernames through every shape a delegate can return.
type staticAuthenticator struct {
	response      *authenticator.Response
	authenticated bool
	err           error
}

func (a *staticAuthenticator) AuthenticateRequest(*http.Request) (*authenticator.Response, bool, error) {
	return a.response, a.authenticated, a.err
}

func responseFor(name string) *authenticator.Response {
	return &authenticator.Response{User: &user.DefaultInfo{Name: name}}
}

func TestForbidSystemUsernames(t *testing.T) {
	t.Parallel()

	delegateErr := errors.New("delegate boom")

	for _, tc := range []struct {
		name string

		response      *authenticator.Response
		authenticated bool
		err           error

		wantName          string // empty means a nil response is expected
		wantAuthenticated bool
		wantErr           string // empty means no error is expected
	}{
		{
			name:          "regular username passes through",
			response:      responseFor("alice"),
			authenticated: true,

			wantName:          "alice",
			wantAuthenticated: true,
		},
		{
			name:          "system username is rejected",
			response:      responseFor("system:admin"),
			authenticated: true,

			wantErr: "system usernames are not admitted",
		},
		{
			name:          "system serviceaccount is rejected",
			response:      responseFor("system:serviceaccount:default:builder"),
			authenticated: true,

			wantErr: "system usernames are not admitted",
		},
		{
			name:          "bare system prefix is rejected",
			response:      responseFor("system:"),
			authenticated: true,

			wantErr: "system usernames are not admitted",
		},
		{
			// Only the "system:" prefix is reserved; a name that merely starts
			// with the word "system" is an ordinary user.
			name:          "system without colon passes through",
			response:      responseFor("systemuser"),
			authenticated: true,

			wantName:          "systemuser",
			wantAuthenticated: true,
		},
		{
			// A delegate that errors is allowed to return a nil response; the
			// filter must not dereference it.
			name: "delegate error with nil response",
			err:  delegateErr,

			wantErr: delegateErr.Error(),
		},
		{
			// Some authenticators return a populated response alongside an
			// error. The error result wins, and is passed through untouched.
			name:     "delegate error with system username response",
			response: responseFor("system:admin"),
			err:      delegateErr,

			wantName: "system:admin",
			wantErr:  delegateErr.Error(),
		},
		{
			name: "unauthenticated with nil response",
		},
		{
			// Not authenticated means the name was never asserted, so there is
			// nothing to forbid: the (false, nil) result passes through.
			name:     "unauthenticated with system username response",
			response: responseFor("system:admin"),

			wantName: "system:admin",
		},
		{
			name:          "authenticated with nil response",
			authenticated: true,

			wantAuthenticated: true,
		},
		{
			name:          "authenticated with nil user",
			response:      &authenticator.Response{},
			authenticated: true,

			wantAuthenticated: true,
			// response is non-nil but carries no user; assert separately below.
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			auth := ForbidSystemUsernames(&staticAuthenticator{
				response:      tc.response,
				authenticated: tc.authenticated,
				err:           tc.err,
			})

			resp, authenticated, err := auth.AuthenticateRequest(&http.Request{})

			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantAuthenticated, authenticated)

			switch {
			case tc.wantName != "":
				require.NotNil(t, resp)
				require.NotNil(t, resp.User)
				require.Equal(t, tc.wantName, resp.User.GetName())
			case tc.response != nil && tc.response.User == nil && tc.err == nil:
				// The delegate's user-less response is forwarded as-is.
				require.Equal(t, tc.response, resp)
			default:
				require.Nil(t, resp)
			}
		})
	}
}

// TestForbidSystemUsernamesDoesNotPanicOnNilUser guards the nil checks
// explicitly: before them, a delegate returning a response without a user (or
// no response at all) crashed the filter instead of failing the request.
func TestForbidSystemUsernamesDoesNotPanicOnNilUser(t *testing.T) {
	t.Parallel()

	for _, delegate := range []*staticAuthenticator{
		{response: nil, authenticated: false, err: errors.New("boom")},
		{response: nil, authenticated: true, err: nil},
		{response: &authenticator.Response{}, authenticated: true, err: nil},
	} {
		require.NotPanics(t, func() {
			_, _, _ = ForbidSystemUsernames(delegate).AuthenticateRequest(&http.Request{})
		})
	}
}
