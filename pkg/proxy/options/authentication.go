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
	"context"
	"fmt"
	"strings"
	"time"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/keyutil"
	serviceaccountcontroller "k8s.io/kubernetes/pkg/controller/serviceaccount"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"
	"k8s.io/kubernetes/pkg/serviceaccount"

	"github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
	kcpauthentication "github.com/kcp-dev/kcp/pkg/proxy/authentication"
)

const resyncPeriod = 10 * time.Hour

// Authentication wraps BuiltInAuthenticationOptions so we can minimize the
// dependencies on apiserver auth machinery, specifically by overriding the
// ApplyTo so we can remove those config dependencies not relevant to the
// subset of auth methods we enable in the proxy.
type Authentication struct {
	BuiltInOptions *kubeoptions.BuiltInAuthenticationOptions
	PassOnGroups   []string
	DropGroups     []string
}

// NewAuthentication creates a default Authentication.
func NewAuthentication() *Authentication {
	auth := &Authentication{
		// Note: when adding new auth methods, also update AdditionalAuthEnabled below
		BuiltInOptions: kubeoptions.NewBuiltInAuthenticationOptions().
			WithClientCert().
			WithOIDC().
			WithServiceAccounts().
			WithTokenFile(),
		// SystemLogicalClusterAdmin is privileged and only for internal traffic,
		// SystemExternalLogicalClusterAdmin must be used for all logical-cluster-admin
		// requests via the proxy, so we drop SystemLogicalClusterAdmin here
		DropGroups: []string{user.SystemPrivilegedGroup, bootstrap.SystemLogicalClusterAdmin},
	}
	auth.BuiltInOptions.ServiceAccounts.Issuers = []string{"https://kcp.default.svc"}
	return auth
}

// When configured to enable auth other than ClientCert, this returns true.
func (c *Authentication) AdditionalAuthEnabled() bool {
	return c.tokenAuthEnabled() || c.serviceAccountAuthEnabled() || c.oidcAuthEnabled()
}

func (c *Authentication) oidcAuthEnabled() bool {
	return c.BuiltInOptions.OIDC != nil && c.BuiltInOptions.OIDC.IssuerURL != ""
}

func (c *Authentication) tokenAuthEnabled() bool {
	return c.BuiltInOptions.TokenFile != nil && c.BuiltInOptions.TokenFile.TokenFile != ""
}

func (c *Authentication) serviceAccountAuthEnabled() bool {
	return c.BuiltInOptions.ServiceAccounts != nil && len(c.BuiltInOptions.ServiceAccounts.KeyFiles) != 0
}

func (c *Authentication) ApplyTo(ctx context.Context, authenticationInfo *genericapiserver.AuthenticationInfo, servingInfo *genericapiserver.SecureServingInfo, rootShardConfig *rest.Config) error {
	// Note BuiltInAuthenticationOptions.ApplyTo is not called, so we
	// can reduce the dependencies pulled in from auth methods which aren't enabled
	authenticatorConfig, err := c.BuiltInOptions.ToAuthenticationConfig()
	if err != nil {
		return err
	}

	// Set up the ClientCert if the client-ca-file option was passed
	if authenticatorConfig.ClientCAContentProvider != nil {
		if err = authenticationInfo.ApplyClientCert(authenticatorConfig.ClientCAContentProvider, servingInfo); err != nil {
			return fmt.Errorf("unable to load client CA file: %w", err)
		}
	}

	// Set for service account auth, if enabled
	if c.serviceAccountAuthEnabled() {
		authenticationInfo.APIAudiences = c.BuiltInOptions.APIAudiences
		if len(c.BuiltInOptions.ServiceAccounts.Issuers) != 0 && len(c.BuiltInOptions.APIAudiences) == 0 {
			authenticationInfo.APIAudiences = c.BuiltInOptions.ServiceAccounts.Issuers
		}

		config := rest.CopyConfig(rootShardConfig)
		tokenGetterClient, err := kcpkubernetesclientset.NewForConfig(config)
		if err != nil {
			return fmt.Errorf("failed to create client for ServiceAccountTokenGetter: %w", err)
		}

		versionedInformers := kcpkubernetesinformers.NewSharedInformerFactory(tokenGetterClient, resyncPeriod)

		authenticatorConfig.ServiceAccountTokenGetter = serviceaccountcontroller.NewClusterGetterFromClient(
			tokenGetterClient,
			versionedInformers.Core().V1().Secrets().Lister(),
			versionedInformers.Core().V1().ServiceAccounts().Lister(),
		)
		authenticatorConfig.SecretsWriter = tokenGetterClient.CoreV1().Secrets()

		if len(c.BuiltInOptions.ServiceAccounts.KeyFiles) > 0 {
			// Load and set the public keys.
			var pubKeys []interface{}
			for _, f := range c.BuiltInOptions.ServiceAccounts.KeyFiles {
				keys, err := keyutil.PublicKeysFromFile(f)
				if err != nil {
					return fmt.Errorf("failed to parse key file %q: %w", f, err)
				}
				pubKeys = append(pubKeys, keys...)
			}
			keysGetter, err := serviceaccount.StaticPublicKeysGetter(pubKeys)
			if err != nil {
				return fmt.Errorf("failed to set up public service account keys: %w", err)
			}
			authenticatorConfig.ServiceAccountPublicKeysGetter = keysGetter
		}
	}

	// Sets up a union Authenticator for all enabled auth methods
	authenticationInfo.Authenticator, _, _, _, err = authenticatorConfig.New(ctx)
	if err != nil {
		return err
	}

	// only pass on those groups to the shards we want
	if len(c.PassOnGroups) > 0 || len(c.DropGroups) > 0 {
		filter := &kcpauthentication.GroupFilter{
			Authenticator: authenticationInfo.Authenticator,
			PassOnGroups:  sets.New[string](),
			DropGroups:    sets.New[string](),
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

// AddFlags delegates to ClientCertAuthenticationOptions.
func (c *Authentication) AddFlags(fs *pflag.FlagSet) {
	c.BuiltInOptions.AddFlags(fs)

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
