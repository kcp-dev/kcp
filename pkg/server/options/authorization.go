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
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/authorization/path"
	"k8s.io/apiserver/pkg/authorization/union"
	genericapiserver "k8s.io/apiserver/pkg/server"

	authz "github.com/kcp-dev/kcp/pkg/authorization"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

type Authorization struct {
	// AlwaysAllowPaths are HTTP paths which are excluded from authorization. They can be plain
	// paths or end in * in which case prefix-match is applied. A leading / is optional.
	AlwaysAllowPaths []string

	// AlwaysAllowGroups are groups which are allowed to take any actions.  In kube, this is privileged system group.
	AlwaysAllowGroups []string
}

func NewAuthorization() *Authorization {
	return &Authorization{
		// This allows the kubelet to always get health and readiness without causing an authorization check.
		// This field can be cleared by callers if they don't want this behavior.
		AlwaysAllowPaths:  []string{"/healthz", "/readyz", "/livez"},
		AlwaysAllowGroups: []string{user.SystemPrivilegedGroup},
	}
}

// WithAlwaysAllowGroups appends the list of paths to AlwaysAllowGroups
func (s *Authorization) WithAlwaysAllowGroups(groups ...string) *Authorization {
	s.AlwaysAllowGroups = append(s.AlwaysAllowGroups, groups...)
	return s
}

// WithAlwaysAllowPaths appends the list of paths to AlwaysAllowPaths
func (s *Authorization) WithAlwaysAllowPaths(paths ...string) *Authorization {
	s.AlwaysAllowPaths = append(s.AlwaysAllowPaths, paths...)
	return s
}

func (s *Authorization) Validate() []error {
	if s == nil {
		return nil
	}

	allErrors := []error{}

	return allErrors
}

func (s *Authorization) AddFlags(fs *pflag.FlagSet) {
	if s == nil {
		return
	}

	fs.StringSliceVar(&s.AlwaysAllowPaths, "authorization-always-allow-paths", s.AlwaysAllowPaths,
		"A list of HTTP paths to skip during authorization, i.e. these are authorized without "+
			"contacting the 'core' kubernetes server.")
}

func (s *Authorization) ApplyTo(config *genericapiserver.Config, informer kcpkubernetesinformers.SharedInformerFactory, kcpinformer kcpinformers.SharedInformerFactory) error {
	var authorizers []authorizer.Authorizer

	workspaceLister := kcpinformer.Core().V1alpha1().LogicalClusters().Lister()

	// group authorizer
	if len(s.AlwaysAllowGroups) > 0 {
		authorizers = append(authorizers, authorizerfactory.NewPrivilegedGroups(s.AlwaysAllowGroups...))
	}

	// path authorizer
	if len(s.AlwaysAllowPaths) > 0 {
		a, err := path.NewAuthorizer(s.AlwaysAllowPaths)
		if err != nil {
			return err
		}
		authorizers = append(authorizers, a)
	}

	// kcp authorizers
	bootstrapAuth, bootstrapRules := authz.NewBootstrapPolicyAuthorizer(informer)
	bootstrapAuth = authz.NewDecorator("bootstrap.authorization.kcp.dev", bootstrapAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	localAuth, localResolver := authz.NewLocalAuthorizer(informer)
	localAuth = authz.NewDecorator("local.authorization.kcp.dev", localAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	maxPermissionPolicyAuth := authz.NewMaximalPermissionPolicyAuthorizer(informer, kcpinformer, union.New(bootstrapAuth, localAuth))
	maxPermissionPolicyAuth = authz.NewDecorator("maxpermissionpolicy.authorization.kcp.dev", maxPermissionPolicyAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	systemCRDAuth := authz.NewSystemCRDAuthorizer(maxPermissionPolicyAuth)
	systemCRDAuth = authz.NewDecorator("systemcrd.authorization.kcp.dev", systemCRDAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	contentAuth := authz.NewWorkspaceContentAuthorizer(informer, workspaceLister, systemCRDAuth)
	contentAuth = authz.NewDecorator("content.authorization.kcp.dev", contentAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	requiredGroupsAuth := authz.NewRequiredGroupsAuthorizer(workspaceLister, contentAuth)
	requiredGroupsAuth = authz.NewDecorator("requiredgroups.authorization.kcp.dev", requiredGroupsAuth).AddAuditLogging().AddAnonymization()

	authorizers = append(authorizers, requiredGroupsAuth)

	config.RuleResolver = union.NewRuleResolvers(bootstrapRules, localResolver)
	config.Authorization.Authorizer = union.New(authorizers...)
	return nil
}
