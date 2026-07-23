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

// Package globalwebhook implements a GLOBAL, cluster-aware validating admission
// webhook: a single ValidatingWebhookConfiguration, supplied to the shard at
// startup (via --admission-control-config-file, this plugin's config section),
// that fires for requests in EVERY logical cluster - without a per-workspace
// WebhookConfiguration object, and tamper-proof against workspace admins.
//
// This is to admission what --authorization-webhook-config-file is to
// authorization: one endpoint, wired at bootstrap, applied globally. Unlike the
// authorization webhook it is an ADMISSION webhook, so the callout carries the
// full object body + operation and can DENY. The request's logical cluster is
// conveyed on the object via the kcp.io/cluster annotation (like kcp's per-
// cluster webhook plugin), stamped here for create/update (object) and delete
// (oldObject) so the external webhook is cluster-aware on every operation.
//
// Configure it by putting a normal ValidatingWebhookConfiguration in this
// plugin's admission-config section (see docs/content/...); leave it unset and
// the plugin is inert. Only URL clientConfig with a caBundle is required - no
// per-cluster objects, no APIBindings.
package globalwebhook

import (
	"context"
	"errors"
	"fmt"
	"io"

	admissionregistrationv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	admissionregistrationapiv1 "k8s.io/kubernetes/pkg/apis/admissionregistration/v1"
	"sigs.k8s.io/yaml"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/admission/validatingwebhook"
)

const (
	// PluginName is the admission plugin name.
	PluginName = "apis.kcp.io/GlobalValidatingWebhook"
)

// Plugin is a global, cluster-aware validating admission webhook. It holds a
// static webhook source parsed once from its admission config; when that config
// is absent it is inert.
type Plugin struct {
	*admission.Handler

	// source is the static, cluster-independent set of webhooks parsed from the
	// plugin config. nil => not configured => the plugin is a no-op.
	source generic.Source

	// Injected via initializers; needed by the inner validating webhook for
	// namespaceSelector matching and (service) endpoint resolution.
	kubeClusterClient              kcpkubernetesclientset.ClusterInterface
	localKubeSharedInformerFactory kcpkubernetesinformers.SharedInformerFactory
}

var (
	_ = admission.ValidationInterface(&Plugin{})
	_ = admission.InitializationValidator(&Plugin{})
	_ = kcpinitializers.WantsKubeClusterClient(&Plugin{})
	_ = kcpinitializers.WantsKubeInformers(&Plugin{})
)

// staticSource is a generic.Source backed by a fixed webhook list - the same for
// every logical cluster (that is what makes the webhook "global").
type staticSource struct {
	hooks []webhook.WebhookAccessor
}

func (s *staticSource) Webhooks() []webhook.WebhookAccessor { return s.hooks }
func (s *staticSource) HasSynced() bool                     { return true }

// NewGlobalValidatingWebhook parses the plugin config (a
// ValidatingWebhookConfiguration) into a static source. An empty config yields
// an inert plugin.
func NewGlobalValidatingWebhook(configFile io.Reader) (*Plugin, error) {
	p := &Plugin{
		Handler: admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
	}
	if configFile == nil {
		return p, nil
	}
	raw, err := io.ReadAll(configFile)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return p, nil
	}

	var cfg admissionregistrationv1.ValidatingWebhookConfiguration
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("globalwebhook: parse config as ValidatingWebhookConfiguration: %w", err)
	}
	// Apply API defaulting the same way object creation would: a parsed config
	// otherwise leaves namespaceSelector/objectSelector nil, and
	// LabelSelectorAsSelector(nil) matches NOTHING - so the webhook would silently
	// never fire. Defaulting sets empty (match-all) selectors, matchPolicy, scope,
	// failurePolicy and timeout.
	admissionregistrationapiv1.SetObjectDefaults_ValidatingWebhookConfiguration(&cfg)
	if len(cfg.Webhooks) == 0 {
		return p, nil
	}

	name := cfg.Name
	if name == "" {
		name = "global"
	}
	hooks := make([]webhook.WebhookAccessor, 0, len(cfg.Webhooks))
	for i := range cfg.Webhooks {
		h := &cfg.Webhooks[i]
		uid := fmt.Sprintf("%s/%s/%d", name, h.Name, i)
		hooks = append(hooks, webhook.NewValidatingWebhookAccessor(uid, name, h))
	}
	p.source = &staticSource{hooks: hooks}
	return p, nil
}

// Register registers the plugin.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewGlobalValidatingWebhook(configFile)
	})
}

// Validate dispatches the admission request to the globally-configured webhook,
// with the request's logical cluster stamped on the object so the external
// webhook is cluster-aware.
func (p *Plugin) Validate(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	if p.source == nil {
		return nil // not configured
	}

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return err
	}
	clusterName := cluster.Name

	plugin, err := validating.NewValidatingAdmissionWebhook(nil)
	if err != nil {
		return fmt.Errorf("globalwebhook: create validating admission webhook: %w", err)
	}
	plugin.SetExternalKubeClientSet(p.kubeClusterClient.Cluster(clusterName.Path()))
	plugin.SetNamespaceInformer(p.localKubeSharedInformerFactory.Core().V1().Namespaces().Cluster(clusterName))
	plugin.SetAPISource(p.source)
	if err := plugin.ValidateInitialization(); err != nil {
		return fmt.Errorf("globalwebhook: validate initialization: %w", err)
	}

	// Convey the logical cluster to the external webhook via the kcp.io/cluster
	// annotation, on whichever object the operation carries: the new object for
	// create/update, the old object for delete. Reverted right after dispatch so
	// it is never persisted.
	if obj, ok := attr.GetObject().(metav1.Object); ok && obj != nil {
		if undo := validatingwebhook.SetClusterAnnotation(obj, clusterName); undo != nil {
			defer undo()
		}
	}
	if old, ok := attr.GetOldObject().(metav1.Object); ok && old != nil {
		if undo := validatingwebhook.SetClusterAnnotation(old, clusterName); undo != nil {
			defer undo()
		}
	}

	return plugin.Validate(ctx, attr, o)
}

// ValidateInitialization ensures required dependencies were injected.
func (p *Plugin) ValidateInitialization() error {
	if p.source == nil {
		return nil // inert; no dependencies needed
	}
	if p.kubeClusterClient == nil {
		return errors.New("missing kubeClusterClient")
	}
	if p.localKubeSharedInformerFactory == nil {
		return errors.New("missing localKubeSharedInformerFactory")
	}
	return nil
}

// SetKubeClusterClient implements WantsKubeClusterClient.
func (p *Plugin) SetKubeClusterClient(client kcpkubernetesclientset.ClusterInterface) {
	p.kubeClusterClient = client
}

// SetKubeInformers implements WantsKubeInformers. Only the local factory is used
// (namespace informer); the source is static, so no global config informer.
func (p *Plugin) SetKubeInformers(local, global kcpkubernetesinformers.SharedInformerFactory) {
	p.localKubeSharedInformerFactory = local
}
