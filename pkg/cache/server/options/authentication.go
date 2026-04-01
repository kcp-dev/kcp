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

package options

import (
	"fmt"

	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/request/anonymous"
	"k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/apiserver/pkg/authentication/request/x509"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
)

type Authentication struct {
	ClientCAFile string

	// EmbeddedAuthenticator is an optional authenticator delegating to the
	// parent server's authentication chain. Set by the shard server when
	// the cache server runs embedded.
	EmbeddedAuthenticator authenticator.Request
}

func NewAuthentication() *Authentication {
	return &Authentication{}
}

func (o *Authentication) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.ClientCAFile, "client-ca-file", o.ClientCAFile, "Path to a PEM-encoded certificate bundle. If set, any request presenting a client certificate signed by one of the authorities in the bundle is authenticated with an identity corresponding to the CommonName of the client certificate.")
}

func (o *Authentication) Validate() []error {
	return nil
}

func (o *Authentication) ApplyTo(authenticationInfo *genericapiserver.AuthenticationInfo, servingInfo *genericapiserver.SecureServingInfo) error {
	if o.ClientCAFile == "" && o.EmbeddedAuthenticator == nil {
		// This validation cannot happen in .Validate because these
		// options may be set by the shard embedding the cache server.
		// For the standalone cache server it doesn't matter if it's
		// validated here or in .Validate.
		return fmt.Errorf("either --client-ca-file or an embedded authenticator must be configured")
	}

	var authenticators []authenticator.Request

	if o.ClientCAFile != "" {
		caProvider, err := dynamiccertificates.NewDynamicCAContentFromFile("client-ca", o.ClientCAFile)
		if err != nil {
			return fmt.Errorf("unable to load client CA file %q: %w", o.ClientCAFile, err)
		}

		if err := authenticationInfo.ApplyClientCert(caProvider, servingInfo); err != nil {
			return fmt.Errorf("unable to apply client cert: %w", err)
		}

		authenticators = append(authenticators, x509.NewDynamic(caProvider.VerifyOptions, x509.CommonNameUserConversion))
	}
	if o.EmbeddedAuthenticator != nil {
		authenticators = append(authenticators, o.EmbeddedAuthenticator)
	}
	authenticators = append(authenticators, anonymous.NewAuthenticator(nil))

	authenticationInfo.Authenticator = union.New(authenticators...)

	return nil
}
