/*
Copyright 2022 The kcp Authors.

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

	"github.com/spf13/pflag"

	"k8s.io/apimachinery/pkg/util/sets"
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

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"

	authz "github.com/kcp-dev/kcp/pkg/authorization"
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

	// AuthorizationOrder is the list of authorizers that allows to rearrange the default order.
	// The default is two authorizers in a union: RBAC and Webhook.
	AuthorizationOrder []string
}

const (
	// authorizerWebhook is the authorizer to make an external webhook call.
	authorizerWebhook string = "Webhook"
	// authorizerRBAC is the authorizer to use Role Based Access Control.
	authorizerRBAC string = "RBAC"
)

var defaultAuthorizers = []string{authorizerRBAC, authorizerWebhook}

func isValidAuthorizer(authorizer string) bool {
	return sets.NewString(defaultAuthorizers...).Has(authorizer)
}

func NewAuthorization() *Authorization {
	return &Authorization{
		// This allows the kubelet to always get health and readiness without causing an authorization check.
		// This field can be cleared by callers if they don't want this behavior.
		AlwaysAllowPaths:   []string{"/healthz", "/readyz", "/livez"},
		AlwaysAllowGroups:  []string{user.SystemPrivilegedGroup},
		Webhook:            kubeoptions.NewBuiltInAuthorizationOptions(),
		AuthorizationOrder: defaultAuthorizers,
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

	if len(s.AuthorizationOrder) > 0 {
		for _, authz := range s.AuthorizationOrder {
			if !isValidAuthorizer(authz) {
				allErrors = append(allErrors, fmt.Errorf("invalid authorizer: %q", authz))
			}
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

	fs.StringSliceVar(&s.AuthorizationOrder, "authorization-order", s.AuthorizationOrder,
		"A list of authorizers that should be enabled, allowing administrator rearrange the default order."+
			"The default order is: RBAC,Webhook.")

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
	// authorizers are sorted into three distinct groups:
	// 1. bypass: These _must_ be consulted first and if they Allow
	//       access MUST be allowed, as they e.g. let system identities
	//       bypass authorization checks.
	// 2. grant: These are user configurable. At the time of writing
	//       this is kube RBAC and the authz webhook.
	//       These may Allow or Deny.
	//       If they Deny access MUST be denied.
	//       If they Allow or NoOpinion the gates are checked.
	// 3. gates: These are vital authorization checks for kcp
	//       functionality. E.g. they protect system CRD
	//       APIExports/-Bindings and enforce the
	//       MaximalPermissionPolicy.
	//       If they Allow or Deny access MUST be allowed or denied.
	//       If they NoOpinion the decision of the grants is returned.

	// Always allow groups and paths must run before other checks.
	// Otherwise a webhook could deny access to e.g. health endpoints.
	var bypass []authorizer.Authorizer
	if len(s.AlwaysAllowGroups) > 0 {
		bypass = append(bypass, authorizerfactory.NewPrivilegedGroups(s.AlwaysAllowGroups...))
	}
	if len(s.AlwaysAllowPaths) > 0 {
		a, err := path.NewAuthorizer(s.AlwaysAllowPaths)
		if err != nil {
			return err
		}
		bypass = append(bypass, a)
	}

	// grants are the configured authorizers.
	// They may grant or deny access.
	// If they grant the additional gate authorizers below can still
	// overrule, e.g. to let a MaximalPermissionPolicy deny access
	// a webhook (which has no knowledge of the MPP) may grant.
	var grants []authorizer.Authorizer

	for _, authorizer := range s.AuthorizationOrder {
		switch authorizer {
		case authorizerRBAC:
			// bootstrap rules defined once for every workspace
			bootstrapAuth, bootstrapRules := authz.NewBootstrapPolicyAuthorizer(kubeInformers)
			bootstrapAuth = authz.NewDecorator("05-bootstrap", bootstrapAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

			// resolves RBAC resources in the workspace
			localAuth, localResolver := authz.NewLocalAuthorizer(kubeInformers)
			localAuth = authz.NewDecorator("05-local", localAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

			globalAuth, _ := authz.NewGlobalAuthorizer(kubeInformers, globalKubeInformers)
			globalAuth = authz.NewDecorator("05-global", globalAuth).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

			grants = append(grants, union.New(bootstrapAuth, localAuth, globalAuth))
			config.RuleResolver = union.NewRuleResolvers(bootstrapRules, localResolver)
		case authorizerWebhook:
			// Re-use the authorizer from the generic control plane (this is only set for webhooks).
			// This is a grant: it may open access, the gates below can still take it away.
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
				authorizer = authz.WithWarrantsAndScopes(authorizer)
				grants = append(grants, authorizer)
			}
		default:
			return fmt.Errorf("invalid authorizer: %q", authorizer)
		}
	}

	localLogicalClusterLister := kcpInformers.Core().V1alpha1().LogicalClusters().Lister()
	globalLogicalClusterLister := globalKcpInformers.Core().V1alpha1().LogicalClusters().Lister()

	// kcp gate authorizers - skipped for Deep SAR
	var gates authorizer.Authorizer = authorizer.AuthorizerFunc(func(context.Context, authorizer.Attributes) (authorizer.Decision, string, error) {
		return authorizer.DecisionNoOpinion, "", nil
	})

	// enforce maximal permission policy
	gates = authz.NewMaximalPermissionPolicyAuthorizer(kubeInformers, globalKubeInformers, kcpInformers, globalKcpInformers)(gates)
	gates = authz.NewDecorator("04-maxpermissionpolicy", gates).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// protect status updates to apiexport and apibinding
	gates = authz.NewSystemCRDAuthorizer(gates)
	gates = authz.NewDecorator("03-systemcrd", gates).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// content auth deteremines if users have access to the workspace itself - by default, in Kube there is a set
	// of default permissions given even to system:authenticated (like access to discovery) - this authorizer allows
	// kcp to make workspaces entirely invisible to users that have not been given access, by making system:authenticated
	// mean nothing unless they also have `verb=access` on `/`
	gates = authz.NewWorkspaceContentAuthorizer(kubeInformers, globalKubeInformers, localLogicalClusterLister, globalLogicalClusterLister)(gates)
	gates = authz.NewDecorator("02-content", gates).AddAuditLogging().AddAnonymization().AddReasonAnnotation()

	// workspaces are annotated to list the groups required on users wishing to access the workspace -
	// this is mostly useful when adding a core set of groups to an org workspace and having them inherited
	// by child workspaces; this gives administrators of an org control over which users can be given access
	// to content in sub-workspaces
	gates = authz.NewRequiredGroupsAuthorizer(localLogicalClusterLister, globalLogicalClusterLister)(gates)
	gates = authz.NewDecorator("01-requiredgroups", gates).AddAuditLogging().AddAnonymization()

	bypassAuth := union.New(bypass...)
	grantsAuth := union.New(grants...)

	config.Authorization.Authorizer = authorizer.AuthorizerFunc(func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		// check allow groups/paths
		if dec, reason, err := bypassAuth.Authorize(ctx, attr); err != nil {
			return authorizer.DecisionNoOpinion, "", err
		} else if dec == authorizer.DecisionAllow {
			return dec, reason, nil
		}

		// check grants
		dec, reason, err := grantsAuth.Authorize(ctx, attr)
		if err != nil {
			return authorizer.DecisionNoOpinion, reason, err
		}
		if dec == authorizer.DecisionDeny {
			return dec, reason, nil
		}

		// grants either Allow or NoOpinion, validate against gates.
		gateDec, gateReason, err := gates.Authorize(ctx, attr)
		if err != nil {
			return authorizer.DecisionNoOpinion, gateReason, err
		}
		if gateDec != authorizer.DecisionNoOpinion {
			return gateDec, gateReason, nil
		}

		return dec, reason, nil
	})

	return nil
}
