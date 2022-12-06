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
	"sync"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/rules"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	apisv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/apis/v1alpha1"
)

type ClusterAwareSource interface {
	Webhooks(cluster logicalcluster.Name) []webhook.WebhookAccessor
	HasSynced() bool
}

type clusterAwareSource struct {
	factory   func(cluster logicalcluster.Name) generic.Source
	hasSynced func() bool

	lock    sync.RWMutex
	sources map[logicalcluster.Name]generic.Source
}

func (c *clusterAwareSource) Webhooks(cluster logicalcluster.Name) []webhook.WebhookAccessor {
	var source generic.Source
	var found bool
	c.lock.RLock()
	source, found = c.sources[cluster]
	c.lock.RUnlock()
	if found {
		return source.Webhooks()
	}

	c.lock.Lock()
	defer c.lock.Unlock()
	source, found = c.sources[cluster]
	if found {
		return source.Webhooks()
	}

	source = c.factory(cluster)
	c.sources[cluster] = source
	return source.Webhooks()
}

func (c *clusterAwareSource) HasSynced() bool {
	return c.hasSynced()
}

var _ initializers.WantsKcpInformers = &WebhookDispatcher{}

type WebhookDispatcher struct {
	dispatcher              generic.Dispatcher
	hookSource              ClusterAwareSource
	apiBindingClusterLister apisv1alpha1listers.APIBindingClusterLister
	apiBindingsHasSynced    cache.InformerSynced
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

	var whAccessor []webhook.WebhookAccessor

	// Determine the type of request, is it api binding or not.
	if workspace, isAPIBinding, err := p.getAPIExportCluster(attr, lcluster); err != nil {
		return err
	} else if isAPIBinding {
		whAccessor = p.hookSource.Webhooks(workspace)
		attr.SetCluster(workspace)
		klog.V(7).Infof("restricting call to api registration hooks in cluster: %v", workspace)
	} else {
		whAccessor = p.hookSource.Webhooks(lcluster)
		attr.SetCluster(lcluster)
		klog.V(7).Infof("restricting call to hooks in cluster: %v", lcluster)
	}

	return p.dispatcher.Dispatch(ctx, attr, o, whAccessor)
}

func (p *WebhookDispatcher) getAPIExportCluster(attr admission.Attributes, clusterName logicalcluster.Name) (logicalcluster.Name, bool, error) {
	objs, err := p.apiBindingClusterLister.Cluster(clusterName).List(labels.Everything())
	if err != nil {
		return logicalcluster.New(""), false, err
	}
	for _, apiBinding := range objs {
		for _, br := range apiBinding.Status.BoundResources {
			// this can never happen by OpenAPI validation
			if apiBinding.Spec.Reference.Export == nil {
				// this will never happen today. But as soon as we add other reference types (like exports), this log output will remind out of necessary work here.
				klog.Errorf("APIBinding %s has no referenced workspace", clusterName, apiBinding.Name)
				continue
			}
			if br.Group == attr.GetResource().Group && br.Resource == attr.GetResource().Resource {
				return apiBinding.Spec.Reference.Export.Cluster.Path(), true, nil
			}
		}
	}
	return logicalcluster.New(""), false, nil
}

func (p *WebhookDispatcher) SetHookSource(factory func(cluster logicalcluster.Name) generic.Source, hasSynced func() bool) {
	p.hookSource = &clusterAwareSource{
		hasSynced: hasSynced,
		factory:   factory,

		lock:    sync.RWMutex{},
		sources: map[logicalcluster.Name]generic.Source{},
	}
}

// SetKcpInformers implements the WantsExternalKcpInformerFactory interface.
func (p *WebhookDispatcher) SetKcpInformers(f kcpinformers.SharedInformerFactory) {
	p.apiBindingClusterLister = f.Apis().V1alpha1().APIBindings().Lister()
	p.apiBindingsHasSynced = f.Apis().V1alpha1().APIBindings().Informer().HasSynced
}
