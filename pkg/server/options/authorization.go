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
	"errors"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/authorization/path"
	"k8s.io/apiserver/pkg/authorization/union"
	genericapiserver "k8s.io/apiserver/pkg/server"

	authz "github.com/kcp-dev/kcp/pkg/authorization"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
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

// WithAlwaysAllowGroups appends the list of paths to AlwaysAllowGroups.
func (s *Authorization) WithAlwaysAllowGroups(groups ...string) *Authorization {
	s.AlwaysAllowGroups = append(s.AlwaysAllowGroups, groups...)
	return s
}

// WithAlwaysAllowPaths appends the list of paths to AlwaysAllowPaths.
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

func (s *Authorization) ApplyTo(config *genericapiserver.Config, kubeInformers, globalKubeInformers kcpkubernetesinformers.SharedInformerFactory, kcpInformers, globalKcpInformers kcpinformers.SharedInformerFactory) error {
	var authorizers []authorizer.Authorizer

	localLogicalClusterLister := kcpInformers.Core().V1alpha1().LogicalClusters().Lister()
	globalLogicalClusterLister := globalKcpInformers.Core().V1alpha1().LogicalClusters().Lister()

	// group authorizer
	if len(s.AlwaysAllowGroups) > 0 {
		privGroups := authorizerfactory.NewPrivilegedGroups(s.AlwaysAllowGroups...)
		authorizers = append(authorizers, authz.WithWarrants(privGroups))
	}

	// path authorizer
	if len(s.AlwaysAllowPaths) > 0 {
		a, err := path.NewAuthorizer(s.AlwaysAllowPaths)
		if err != nil {
			return err
		}
		authorizers = append(authorizers, a)
	}

	// kcp authorizers, these are evaluated in reverse order
	// TODO: link the markdown

	// bootstrap rules defined once for every workspace
	bootstrapAuth, bootstrapRules := authz.NewBootstrapPolicyAuthorizer(kubeInformers)
	bootstrapAuth = authz.NewDecorator("05-bootstrap", bootstrapAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// resolves RBAC resources in the workspace
	localAuth, localResolver := authz.NewLocalAuthorizer(kubeInformers)
	localAuth = authz.NewDecorator("05-local", localAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	globalAuth, _ := authz.NewGlobalAuthorizer(kubeInformers, globalKubeInformers)
	globalAuth = authz.NewDecorator("05-global", globalAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	chain := union.New(bootstrapAuth, localAuth, globalAuth)
	chain = authz.WithWarrants(chain)

	// everything below - skipped for Deep SAR

	// enforce maximal permission policy
	chain = withWarrantsInChainCallingDelegateWithOriginalUser(authz.NewMaximalPermissionPolicyAuthorizer(kubeInformers, globalKubeInformers, kcpInformers, globalKcpInformers), chain)
	chain = authz.NewDecorator("04-maxpermissionpolicy", chain).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// protect status updates to apiexport and apibinding
	chain = withWarrantsInChainCallingDelegateWithOriginalUser(authz.NewSystemCRDAuthorizer, chain)
	chain = authz.NewDecorator("03-systemcrd", chain).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// content auth deteremines if users have access to the workspace itself - by default, in Kube there is a set
	// of default permissions given even to system:authenticated (like access to discovery) - this authorizer allows
	// kcp to make workspaces entirely invisible to users that have not been given access, by making system:authenticated
	// mean nothing unless they also have `verb=access` on `/`
	chain = withWarrantsInChainCallingDelegateWithOriginalUser(authz.NewWorkspaceContentAuthorizer(kubeInformers, globalKubeInformers, localLogicalClusterLister, globalLogicalClusterLister), chain)
	chain = authz.NewDecorator("02-content", chain).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// workspaces are annotated to list the groups required on users wishing to access the workspace -
	// this is mostly useful when adding a core set of groups to an org workspace and having them inherited
	// by child workspaces; this gives administrators of an org control over which users can be given access
	// to content in sub-workspaces
	chain = withWarrantsInChainCallingDelegateWithOriginalUser(authz.NewRequiredGroupsAuthorizer(localLogicalClusterLister, globalLogicalClusterLister), chain)
	chain = authz.NewDecorator("01-requiredgroups", chain).AddAuditLogging().AddAnonymization()

	authorizers = append(authorizers, chain)

	config.RuleResolver = authz.ResolverWithWarrants(union.NewRuleResolvers(bootstrapRules, localResolver))
	config.Authorization.Authorizer = union.New(authorizers...)
	return nil
}

// withWarrantsInChainCallingDelegateWithOriginalUser could be named shorter, but then it would
// look innocent which it isn't. It produces one level of the chain with the wrapper called
// for the base and all warrents. The delegate though is called only once with the original user.
// This is done by hooking into the delegate call, and storing the original user and the
// result of the delegate call in the context. So it avoids calling the delegate multiple times.
func withWarrantsInChainCallingDelegateWithOriginalUser(wrapper func(authorizer.Authorizer) authorizer.Authorizer, delegate authorizer.Authorizer) authorizer.Authorizer {
	type T int
	const last T = iota
	type state struct {
		original authorizer.Attributes
		called   bool
		dec      authorizer.Decision
		reason   string
		err      error
	}

	chain := wrapper(authorizer.AuthorizerFunc(func(ctx context.Context, _ authorizer.Attributes) (authorizer.Decision, string, error) {
		// run delegate only once
		k, _ := ctx.Value(last).(T)
		st, ok := ctx.Value(k).(*state)
		if !ok {
			return authorizer.DecisionNoOpinion, "", errors.New("no state found in context") // should not happen
		}
		if !st.called {
			st.dec, st.reason, st.err = delegate.Authorize(ctx, st.original)
			st.called = true
		}
		return st.dec, st.reason, st.err
	}))
	chain = authz.WithWarrants(chain)

	return authorizer.AuthorizerFunc(func(ctx context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
		// store the attributes with original user in the context
		k, _ := ctx.Value(last).(T)
		k++
		ctx = context.WithValue(context.WithValue(ctx, k, &state{original: a}), last, k)

		return chain.Authorize(ctx, a)
	})
}
