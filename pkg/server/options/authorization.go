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

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/spf13/pflag"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	"k8s.io/apiserver/pkg/authorization/path"
	"k8s.io/apiserver/pkg/authorization/union"
	"k8s.io/apiserver/pkg/informerfactoryhack"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/egressselector"
	authzmodes "k8s.io/kubernetes/pkg/kubeapiserver/authorizer/modes"
	kubeoptions "k8s.io/kubernetes/pkg/kubeapiserver/options"

	authz "github.com/kcp-dev/kcp/pkg/authorization"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
)

type Authorization struct {
	// AlwaysAllowPaths are HTTP paths which are excluded from authorization. They can be plain
	// paths or end in * in which case prefix-match is applied. A leading / is optional.
	AlwaysAllowPaths []string

	// AlwaysAllowGroups are groups which are allowed to take any actions.  In kube, this is privileged system group.
	AlwaysAllowGroups []string

	// Webhook contains flags to enable an external HTTPS webhook to perform
	// authorization against. Note that not all built-in options are supported by kcp.
	Webhook *kubeoptions.BuiltInAuthorizationOptions
}

func NewAuthorization() *Authorization {
	return &Authorization{
		// This allows the kubelet to always get health and readiness without causing an authorization check.
		// This field can be cleared by callers if they don't want this behavior.
		AlwaysAllowPaths:  []string{"/healthz", "/readyz", "/livez"},
		AlwaysAllowGroups: []string{user.SystemPrivilegedGroup},
		Webhook:           kubeoptions.NewBuiltInAuthorizationOptions(),
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

func (s *Authorization) Complete() error {
	if s == nil {
		return nil
	}

	// kcp only supports optionally specifying an external authorization webhook
	// in addition to the built-in authorization logic.
	if s.Webhook.WebhookConfigFile != "" {
		s.Webhook.Modes = []string{authzmodes.ModeWebhook}
	} else {
		s.Webhook = nil
	}

	return nil
}

func (s *Authorization) Validate() []error {
	if s == nil {
		return nil
	}

	allErrors := []error{}

	if s.Webhook != nil {
		if errs := s.Webhook.Validate(); len(errs) > 0 {
			allErrors = append(allErrors, errs...)
		}
	}

	return allErrors
}

func (s *Authorization) AddFlags(fs *pflag.FlagSet) {
	if s == nil {
		return
	}

	fs.StringSliceVar(&s.AlwaysAllowPaths, "authorization-always-allow-paths", s.AlwaysAllowPaths,
		"A list of HTTP paths to skip during authorization, i.e. these are authorized without "+
			"contacting the 'core' kubernetes server.")

	// Only surface selected, webhook-related CLI flags

	fs.StringVar(&s.Webhook.WebhookConfigFile, "authorization-webhook-config-file", s.Webhook.WebhookConfigFile,
		"File with optional webhook configuration in kubeconfig format. The API server will query the remote service to determine access on the API server's secure port.")

	fs.StringVar(&s.Webhook.WebhookVersion, "authorization-webhook-version", s.Webhook.WebhookVersion,
		"The API version of the authorization.k8s.io SubjectAccessReview to send to and expect from the webhook.")

	fs.DurationVar(&s.Webhook.WebhookCacheAuthorizedTTL, "authorization-webhook-cache-authorized-ttl", s.Webhook.WebhookCacheAuthorizedTTL,
		"The duration to cache 'authorized' responses from the webhook authorizer.")

	fs.DurationVar(&s.Webhook.WebhookCacheUnauthorizedTTL, "authorization-webhook-cache-unauthorized-ttl", s.Webhook.WebhookCacheUnauthorizedTTL,
		"The duration to cache 'unauthorized' responses from the webhook authorizer.")
}

func (s *Authorization) ApplyTo(ctx context.Context, config *genericapiserver.Config, kubeInformers, globalKubeInformers kcpkubernetesinformers.SharedInformerFactory, kcpInformers, globalKcpInformers kcpinformers.SharedInformerFactory) error {
	var authorizers []authorizer.Authorizer

	localLogicalClusterLister := kcpInformers.Core().V1alpha1().LogicalClusters().Lister()
	globalLogicalClusterLister := globalKcpInformers.Core().V1alpha1().LogicalClusters().Lister()

	// group authorizer
	if len(s.AlwaysAllowGroups) > 0 {
		privGroups := authorizerfactory.NewPrivilegedGroups(s.AlwaysAllowGroups...)
		authorizers = append(authorizers, privGroups)
	}

	// path authorizer
	if len(s.AlwaysAllowPaths) > 0 {
		a, err := path.NewAuthorizer(s.AlwaysAllowPaths)
		if err != nil {
			return err
		}
		authorizers = append(authorizers, a)
	}

	// Re-use the authorizer from the generic control plane (this is only set for webhooks);
	// make sure this is added *after* the alwaysAllow* authorizers, or else the webhook could prevent
	// healthcheck endpoints from working.
	// NB: Due to the inner workings of Kubernetes' webhook authorizer, this authorizer will actually
	// always be a union of a privilegedGroupAuthorizer for system:masters and the webhook itself,
	// ensuring the webhook isn't called for that privileged group.
	if webhook := s.Webhook; webhook != nil && webhook.WebhookConfigFile != "" {
		authorizationConfig, err := webhook.ToAuthorizationConfig(informerfactoryhack.Wrap(kubeInformers))
		if err != nil {
			return err
		}

		if config.EgressSelector != nil {
			egressDialer, err := config.EgressSelector.Lookup(egressselector.ControlPlane.AsNetworkContext())
			if err != nil {
				return err
			}
			authorizationConfig.CustomDial = egressDialer
		}

		authorizer, _, err := authorizationConfig.New(ctx, config.APIServerID)
		if err != nil {
			return err
		}

		authorizers = append(authorizers, authorizer)
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

	// everything below - skipped for Deep SAR

	// enforce maximal permission policy
	chain = authz.NewMaximalPermissionPolicyAuthorizer(kubeInformers, globalKubeInformers, kcpInformers, globalKcpInformers)(chain)
	chain = authz.NewDecorator("04-maxpermissionpolicy", chain).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// protect status updates to apiexport and apibinding
	chain = authz.NewSystemCRDAuthorizer(chain)
	chain = authz.NewDecorator("03-systemcrd", chain).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// content auth deteremines if users have access to the workspace itself - by default, in Kube there is a set
	// of default permissions given even to system:authenticated (like access to discovery) - this authorizer allows
	// kcp to make workspaces entirely invisible to users that have not been given access, by making system:authenticated
	// mean nothing unless they also have `verb=access` on `/`
	chain = authz.NewWorkspaceContentAuthorizer(kubeInformers, globalKubeInformers, localLogicalClusterLister, globalLogicalClusterLister)(chain)
	chain = authz.NewDecorator("02-content", chain).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// workspaces are annotated to list the groups required on users wishing to access the workspace -
	// this is mostly useful when adding a core set of groups to an org workspace and having them inherited
	// by child workspaces; this gives administrators of an org control over which users can be given access
	// to content in sub-workspaces
	chain = authz.NewRequiredGroupsAuthorizer(localLogicalClusterLister, globalLogicalClusterLister)(chain)
	chain = authz.NewDecorator("01-requiredgroups", chain).AddAuditLogging().AddAnonymization()

	authorizers = append(authorizers, chain)

	config.RuleResolver = union.NewRuleResolvers(bootstrapRules, localResolver)
	config.Authorization.Authorizer = union.New(authorizers...)
	return nil
}
