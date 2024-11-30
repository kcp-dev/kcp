/*
Copyright 2022 The Kube Bind Authors.

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
	"os"

	"github.com/spf13/pflag"
)

type OIDC struct {
	IssuerClientID     string
	IssuerClientSecret string
	IssuerURL          string
	CallbackURL        string
	AuthorizeURL       string
	OIDCCAFile         string
}

func NewOIDC() *OIDC {
	return &OIDC{}
}

func (options *OIDC) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&options.IssuerClientID, "oidc-issuer-client-id", options.IssuerClientID, "Issuer client ID")
	fs.StringVar(&options.IssuerClientSecret, "oidc-issuer-client-secret", options.IssuerClientSecret, "OpenID client secret")
	fs.StringVar(&options.IssuerURL, "oidc-issuer-url", options.IssuerURL, "Callback URL for OpenID responses.")
	fs.StringVar(&options.CallbackURL, "oidc-callback-url", options.CallbackURL, "OpenID callback URL")
	fs.StringVar(&options.AuthorizeURL, "oidc-authorize-url", options.AuthorizeURL, "OpenID authorize URL")
	fs.StringVar(&options.OIDCCAFile, "oidc-ca-file", options.OIDCCAFile, "OpenID CA file")
}

func (options *OIDC) Complete() error {
	return nil
}

func (options *OIDC) Validate() error {
	if options.IssuerClientID == "" {
		return fmt.Errorf("OIDC issuer client ID cannot be empty")
	}
	if options.IssuerClientSecret == "" {
		return fmt.Errorf("OIDC issuer client secret cannot be empty")
	}
	if options.IssuerURL == "" {
		return fmt.Errorf("OIDC issuer URL cannot be empty")
	}
	if options.CallbackURL == "" {
		return fmt.Errorf("OIDC callback URL cannot be empty")
	}

	if options.OIDCCAFile != "" {
		if _, err := os.Stat(options.OIDCCAFile); err != nil {
			return fmt.Errorf("OIDC CA file %s does not exist", options.OIDCCAFile)
		}
	}

	return nil
}
