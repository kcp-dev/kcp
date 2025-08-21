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
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
)

// GroupFilter is a filter that filters out group that are not in the allowed groups,
// and groups that are in the disallowed groups.
type GroupFilter struct {
	Authenticator authenticator.Request

	PassOnGroups sets.Set[string]
	DropGroups   sets.Set[string]

	PassOnGroupPrefixes []string
	DropGroupPrefixes   []string
}

var _ authenticator.Request = &GroupFilter{}

func (a *GroupFilter) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	resp, ok, err := a.Authenticator.AuthenticateRequest(req)
	if resp == nil || resp.User == nil {
		return resp, ok, err
	}
	groupsToPassOn := sets.New[string](resp.User.GetGroups()...)

	info := user.DefaultInfo{
		Name:  resp.User.GetName(),
		UID:   resp.User.GetUID(),
		Extra: resp.User.GetExtra(),
	}
	resp.User = &info

	if len(a.PassOnGroups) > 0 || len(a.PassOnGroupPrefixes) > 0 {
		for g := range groupsToPassOn {
			if a.PassOnGroups.Has(g) || hasPrefix(g, a.PassOnGroupPrefixes...) {
				continue
			}

			groupsToPassOn.Delete(g)
		}
	}

	for g := range groupsToPassOn {
		if a.DropGroups.Has(g) || hasPrefix(g, a.DropGroupPrefixes...) {
			groupsToPassOn.Delete(g)
		}
	}

	info.Groups = sets.List[string](groupsToPassOn)

	return resp, ok, err
}

func hasPrefix(v string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}
