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

package validatingwebhook

import (
	"context"
	"io"

	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	"github.com/kcp-dev/logicalcluster/v3"

	admissionv1 "k8s.io/api/admission/v1"
	admissionv1beta1 "k8s.io/api/admission/v1beta1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/config"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	webhookutil "k8s.io/apiserver/pkg/util/webhook"
	kubernetesinformers "k8s.io/client-go/informers"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/admission/webhook"
)

const (
	PluginName = "apis.kcp.io/ValidatingWebhook"
)

type Plugin struct {
	// Using validating plugin, for the dispatcher to use.
	// This plugins admit function will never be called.
	validating.Plugin
	*webhook.WebhookDispatcher
}

var (
	_ = admission.ValidationInterface(&Plugin{})
	_ = admission.InitializationValidator(&Plugin{})
	_ = kcpinitializers.WantsKcpInformers(&Plugin{})
	_ = kcpinitializers.WantsKubeInformers(&Plugin{})
)

func NewValidatingAdmissionWebhook(configfile io.Reader) (*Plugin, error) {
	p := &Plugin{
		Plugin:            validating.Plugin{Webhook: &generic.Webhook{}},
		WebhookDispatcher: webhook.NewWebhookDispatcher(),
	}
	p.WebhookDispatcher.Handler = admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update)

	dispatcherFactory := validating.NewValidatingDispatcher(&p.Plugin)

	// Making our own dispatcher so that we can control the webhook accessors.
	kubeconfigFile, err := config.LoadConfig(configfile)
	if err != nil {
		return nil, err
	}
	cm, err := webhookutil.NewClientManager(
		[]schema.GroupVersion{
			admissionv1beta1.SchemeGroupVersion,
			admissionv1.SchemeGroupVersion,
		},
		admissionv1beta1.AddToScheme,
		admissionv1.AddToScheme,
	)
	if err != nil {
		return nil, err
	}
	authInfoResolver, err := webhookutil.NewDefaultAuthenticationInfoResolver(kubeconfigFile)
	if err != nil {
		return nil, err
	}
	// Set defaults which may be overridden later.
	cm.SetAuthenticationInfoResolver(authInfoResolver)
	cm.SetServiceResolver(webhookutil.NewDefaultServiceResolver())

	p.WebhookDispatcher.SetDispatcher(dispatcherFactory(&cm))
	// Need to do this, to make sure that the underlying objects for the call to ShouldCallHook have the right values
	p.Plugin.Webhook, err = generic.NewWebhook(p.Handler, configfile, configuration.NewValidatingWebhookConfigurationManager, dispatcherFactory)
	if err != nil {
		return nil, err
	}

	// Override the ready func

	p.SetReadyFunc(func() bool {
		if p.WebhookDispatcher.HasSynced() && p.Plugin.WaitForReady() {
			return true
		}
		return false
	})
	return p, nil
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewValidatingAdmissionWebhook(configFile)
	})
}

func (p *Plugin) Validate(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	return p.WebhookDispatcher.Dispatch(ctx, attr, o)
}

// SetExternalKubeInformerFactory implements the WantsExternalKubeInformerFactory interface.
func (p *Plugin) SetExternalKubeInformerFactory(f kubernetesinformers.SharedInformerFactory) {
	p.Plugin.SetExternalKubeInformerFactory(f) // for namespaces
}

func (p *Plugin) SetKubeInformers(local, global kcpkubernetesinformers.SharedInformerFactory) {
	p.WebhookDispatcher.SetHookSource(func(cluster logicalcluster.Name) generic.Source {
		informer := global.Admissionregistration().V1().MutatingWebhookConfigurations().Cluster(cluster)
		return configuration.NewMutatingWebhookConfigurationManagerForInformer(informer)
	}, global.Admissionregistration().V1().MutatingWebhookConfigurations().Informer().HasSynced)
}
