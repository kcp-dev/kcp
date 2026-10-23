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
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/authorization/path"
	"k8s.io/apiserver/pkg/authorization/union"
	genericapiserver "k8s.io/apiserver/pkg/server"
)

type Authorization struct {
	AlwaysAllowPaths  []string
	AlwaysAllowGroups []string
}

func NewAuthorization() *Authorization {
	return &Authorization{
		AlwaysAllowPaths:  []string{"/healthz", "/readyz", "/livez"},
		AlwaysAllowGroups: []string{user.SystemPrivilegedGroup},
	}
}

func (o *Authorization) AddFlags(fs *pflag.FlagSet) {
	fs.StringSliceVar(&o.AlwaysAllowPaths, "authorization-always-allow-paths", o.AlwaysAllowPaths,
		"A list of HTTP paths to skip during authorization, i.e. these are authorized without contacting the 'core' kubernetes server.")
}

func (o *Authorization) Validate() []error {
	return nil
}

func (o *Authorization) ApplyTo(config *genericapiserver.Config) error {
	var authorizers []union.NamedAuthorizer

	if len(o.AlwaysAllowGroups) > 0 {
		authorizers = append(authorizers, union.NamedAuthorizer{AuthorizerName: "AlwaysAllowGroups", Authorizer: authorizerfactory.NewPrivilegedGroups(o.AlwaysAllowGroups...)})
	}

	if len(o.AlwaysAllowPaths) > 0 {
		a, err := path.NewAuthorizer(o.AlwaysAllowPaths)
		if err != nil {
			return err
		}
		authorizers = append(authorizers, union.NamedAuthorizer{AuthorizerName: "AlwaysAllowPaths", Authorizer: a})
	}

	unioned, err := union.New(authorizers...)
	if err != nil {
		return err
	}
	config.Authorization.Authorizer = unioned
	return nil
}
