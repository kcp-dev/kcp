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
	"slices"
	"strings"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
)

// ExtraFilter is a filter that filters out extra fields that are not
// allowed.
type ExtraFilter struct {
	Authenticator authenticator.Request

	// AllowExtraKeys is a list of exact keys to allow through.
	// It takes precedence over DropExtraKeyContains.
	AllowExtraKeys []string
	// DropExtraKeyContains is a list of strings that will cause an extra
	// key/value pair to be dropped if the key contains any of them.
	DropExtraKeyContains []string
}

var _ authenticator.Request = &ExtraFilter{}

func (a *ExtraFilter) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	resp, ok, err := a.Authenticator.AuthenticateRequest(req)
	if resp == nil || resp.User == nil {
		return resp, ok, err
	}

	info := user.DefaultInfo{
		Name:   resp.User.GetName(),
		UID:    resp.User.GetUID(),
		Groups: resp.User.GetGroups(),
		Extra:  map[string][]string{},
	}
	for k, v := range resp.User.GetExtra() {
		if slices.Contains(a.AllowExtraKeys, k) {
			info.Extra[k] = v
			continue
		}
		if containsAny(k, a.DropExtraKeyContains...) {
			continue
		}
		info.Extra[k] = v
	}
	resp.User = &info

	return resp, ok, err
}

func containsAny(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
