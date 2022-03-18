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

	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/request/x509"
	genericapiserver "k8s.io/apiserver/pkg/server"
	apiserveroptions "k8s.io/apiserver/pkg/server/options"
)

// Authentication wraps ClientCertAuthenticationOptions so we don't pull in
// more auth machinery than we need with DelegatingAuthenticationOptions
type Authentication struct {
	ClientCert apiserveroptions.ClientCertAuthenticationOptions
}

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
}

// Validate just completes the options pattern. Returns nil.
func (c *Authentication) Validate() []error {
	return nil
}
