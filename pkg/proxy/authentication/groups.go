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
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/request/x509"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"
)

// GroupFilter is a filter that filters out group that are not in the allowed groups,
// and groups that are in the disallowed groups.
type GroupFilter struct {
	Authenticator authenticator.Request

	PassOnGroups sets.String
	DropGroups   sets.String

	PassOnGroupPrefixes []string
	DropGroupPrefixes   []string
}

var _ authenticator.Request = &GroupFilter{}

func (a *GroupFilter) AuthenticateRequest(req *http.Request) (*authenticator.Response, bool, error) {
	resp, ok, err := a.Authenticator.AuthenticateRequest(req)
	if resp == nil || resp.User == nil {
		return resp, ok, err
	}
	groupsToPassOn := sets.NewString(resp.User.GetGroups()...)

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

	info.Groups = groupsToPassOn.List()

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

// Authentication wraps ClientCertAuthenticationOptions so we don't pull in
// more auth machinery than we need with DelegatingAuthenticationOptions
type Authentication struct {
	ClientCert apiserveroptions.ClientCertAuthenticationOptions

	PassOnGroups []string
	DropGroups   []string
}

// NewAuthentication creates a default Authentication
func NewAuthentication() *Authentication {
	return &Authentication{}
}

// ApplyTo sets up the x509 Authenticator if the client-ca-file option was passed
func (c *Authentication) ApplyTo(authenticationInfo *genericapiserver.AuthenticationInfo, servingInfo *genericapiserver.SecureServingInfo) error {
	clientCAProvider, err := c.ClientCert.GetClientCAContentProvider()
	if err != nil {
		return fmt.Errorf("unable to load client CA provider: %w", err)
	}
	if clientCAProvider != nil {
		if err = authenticationInfo.ApplyClientCert(clientCAProvider, servingInfo); err != nil {
			return fmt.Errorf("unable to assign client CA provider: %w", err)
		}
		authenticationInfo.Authenticator = x509.NewDynamic(clientCAProvider.VerifyOptions, x509.CommonNameUserConversion)
	}
	return nil
}

// AddFlags delegates to ClientCertAuthenticationOptions
func (c *Authentication) AddFlags(fs *pflag.FlagSet) {
	c.ClientCert.AddFlags(fs)

	fs.StringSliceVar(&c.PassOnGroups, "authentication-pass-on-groups", c.PassOnGroups,
		"Groups that are passed on to the shard. Empty matches all. \"prefix*\" matches "+
			"all beginning with the given prefix. Dropping trumps over passing on.")
	fs.StringSliceVar(&c.DropGroups, "authentication-drop-groups", c.DropGroups,
		"Groups that are not passed on to the shard. Empty matches none. \"prefix*\" matches "+
			"all beginning with the given prefix. Dropping trumps over passing on.")
}

// Validate just completes the options pattern. Returns nil.
func (c *Authentication) Validate() []error {
	return nil
}
