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

package webhook

import (
	"context"
	"fmt"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	webhookconfiguration "k8s.io/apiserver/pkg/admission/configuration"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/rules"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

const byWorkspaceIndex = "webhookDispatcher-byWorkspace"

var _ initializers.WantsKcpInformers = &WebhookDispatcher{}

type WebhookDispatcher struct {
	dispatcher           generic.Dispatcher
	hookSource           generic.Source
	apiBindingsIndexer   cache.Indexer
	apiBindingsHasSynced func() bool
	*admission.Handler
}

func (p *WebhookDispatcher) HasSynced() bool {
	return p.hookSource.HasSynced() && p.apiBindingsHasSynced()
}

func (p *WebhookDispatcher) SetDispatcher(dispatch generic.Dispatcher) {
	p.dispatcher = dispatch
}

func (p *WebhookDispatcher) Dispatch(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	// If the object is a Webhook configuration, do not call webhooks
	// This is because we need some way to recover if a webhook is preventing a cluster resources from being updated
	if rules.IsWebhookConfigurationResource(attr) {
		return nil
	}
	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}
	if !p.WaitForReady() {
		return admission.NewForbidden(attr, fmt.Errorf("not yet ready to handle request"))
	}

	hooks := p.hookSource.Webhooks()
	var whAccessor []webhook.WebhookAccessor

	// Determine the type of request, is it api binding or not.
	if workspace, isAPIBinding, err := p.getAPIBindingWorkspace(attr, lcluster); err != nil {
		return err
	} else if isAPIBinding {
		whAccessor = p.restrictToLogicalCluster(hooks, workspace)
		klog.V(7).Infof("restricting call to api registration hooks in cluster: %v", workspace)
	} else {
		whAccessor = p.restrictToLogicalCluster(hooks, lcluster)
		klog.V(7).Infof("restricting call to hooks in cluster: %v", lcluster)
	}

	return p.dispatcher.Dispatch(ctx, attr, o, whAccessor)
}

func (p *WebhookDispatcher) getAPIBindingWorkspace(attr admission.Attributes, clusterName logicalcluster.Name) (logicalcluster.Name, bool, error) {
	objs, err := p.apiBindingsIndexer.ByIndex(byWorkspaceIndex, clusterName.String())
	if err != nil {
		return logicalcluster.New(""), false, err
	}
	for _, obj := range objs {
		apiBinding := obj.(*apisv1alpha1.APIBinding)
		for _, br := range apiBinding.Status.BoundResources {
			if apiBinding.Status.BoundAPIExport.Workspace == nil {
				// this will never happen today. But as soon as we add other reference types (like exports), this log output will remind out of necessary work here.
				klog.Errorf("APIBinding %s has no referenced workspace", clusterName, apiBinding.Name)
				continue
			}
			if br.Group == attr.GetResource().Group && br.Resource == attr.GetResource().Resource {
				return logicalcluster.New(apiBinding.Status.BoundAPIExport.Workspace.Path), true, nil
			}
		}
	}
	return logicalcluster.New(""), false, nil
}

// In the future use a restricted list call
func (p *WebhookDispatcher) restrictToLogicalCluster(hooks []webhook.WebhookAccessor, lc logicalcluster.Name) []webhook.WebhookAccessor {
	// TODO(sttts): this might not scale if there are many webhooks. This is called per request, and traverses all
	//              webhook registrations. The hope is that there are not many webhooks per shard.
	wh := []webhook.WebhookAccessor{}
	for _, hook := range hooks {
		if hook.(webhookconfiguration.WebhookClusterAccessor).GetLogicalCluster() == lc {
			wh = append(wh, hook)
		}
	}
	return wh
}

func (p *WebhookDispatcher) SetHookSource(s generic.Source) {
	p.hookSource = s
}

// SetKcpInformers implements the WantsExternalKcpInformerFactory interface.
func (p *WebhookDispatcher) SetKcpInformers(f kcpinformers.SharedInformerFactory) {
	if _, found := f.Apis().V1alpha1().APIBindings().Informer().GetIndexer().GetIndexers()[byWorkspaceIndex]; !found {
		if err := f.Apis().V1alpha1().APIBindings().Informer().AddIndexers(cache.Indexers{
			byWorkspaceIndex: func(obj interface{}) ([]string, error) {
				return []string{logicalcluster.From(obj.(metav1.Object)).String()}, nil
			},
		}); err != nil {
			// nothing we can do here. But this should also never happen. We check for existence before.
			klog.Errorf("failed to add indexer for APIBindings: %v", err)
		}
	}
	p.apiBindingsIndexer = f.Apis().V1alpha1().APIBindings().Informer().GetIndexer()
	p.apiBindingsHasSynced = f.Apis().V1alpha1().APIBindings().Informer().HasSynced
}
