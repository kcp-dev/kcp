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

package options

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/request/x509"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"

	kcpauthentication "github.com/kcp-dev/kcp/pkg/proxy/authentication"
)

// Authentication wraps ClientCertAuthenticationOptions so we don't pull in
// more auth machinery than we need with DelegatingAuthenticationOptions
type Authentication struct {
	ClientCert apiserveroptions.ClientCertAuthenticationOptions

	PassOnGroups []string
	DropGroups   []string
}

// NewAuthentication creates a default Authentication
func NewAuthentication() *Authentication {
	return &Authentication{
		DropGroups: []string{user.SystemPrivilegedGroup},
	}
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

	// only pass on those groups to the shards we want
	if len(c.PassOnGroups) > 0 || len(c.DropGroups) > 0 {
		filter := &kcpauthentication.GroupFilter{
			Authenticator: authenticationInfo.Authenticator,
			PassOnGroups:  sets.NewString(),
			DropGroups:    sets.NewString(),
		}
		authenticationInfo.Authenticator = filter

		for _, g := range c.PassOnGroups {
			if strings.HasSuffix(g, "*") {
				filter.PassOnGroupPrefixes = append(filter.PassOnGroupPrefixes, g[:len(g)-1])
			} else {
				filter.PassOnGroups.Insert(g)
			}
		}
		for _, g := range c.DropGroups {
			if strings.HasSuffix(g, "*") {
				filter.DropGroupPrefixes = append(filter.DropGroupPrefixes, g[:len(g)-1])
			} else {
				filter.DropGroups.Insert(g)
			}
		}
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

func (c *Authentication) Validate() []error {
	return nil
}
