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

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/plugin/webhook"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/generic"
	"k8s.io/apiserver/pkg/admission/plugin/webhook/predicates/rules"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/indexers"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
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
	*admission.Handler

	dispatcher generic.Dispatcher
	hookSource ClusterAwareSource

	getAPIBindings func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error)

	informersHaveSynced func() bool
}

func NewWebhookDispatcher() *WebhookDispatcher {
	d := &WebhookDispatcher{
		Handler: admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
	}

	return d
}

func (p *WebhookDispatcher) HasSynced() bool {
	return p.hookSource.HasSynced() && p.informersHaveSynced()
}

func (p *WebhookDispatcher) SetDispatcher(dispatch generic.Dispatcher) {
	p.dispatcher = dispatch
}

func (p *WebhookDispatcher) Dispatch(ctx context.Context, attr admission.Attributes, o admission.ObjectInterfaces) error {
	// If the object is a Webhook configuration, do not call webhooks
	// This is because we need some way to recover if a webhook is preventing a cluster resources from being updated
	if rules.IsExemptAdmissionConfigurationResource(attr) {
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
	if workspace, err := p.getAPIExportCluster(attr, lcluster); err != nil {
		return err
	} else if !workspace.Empty() {
		whAccessor = p.hookSource.Webhooks(workspace)
		attr.SetCluster(workspace)
		klog.FromContext(ctx).V(7).WithValues("cluster", workspace).Info("restricting call to api registration hooks in cluster")
	} else {
		whAccessor = p.hookSource.Webhooks(lcluster)
		attr.SetCluster(lcluster)
		klog.FromContext(ctx).V(7).WithValues("cluster", lcluster).Info("restricting call to hooks in cluster")
	}

	return p.dispatcher.Dispatch(ctx, attr, o, whAccessor)
}

func (p *WebhookDispatcher) getAPIExportCluster(attr admission.Attributes, clusterName logicalcluster.Name) (logicalcluster.Name, error) {
	objs, err := p.getAPIBindings(clusterName)
	if err != nil {
		return "", err
	}
	for _, apiBinding := range objs {
		for _, br := range apiBinding.Status.BoundResources {
			if br.Group == attr.GetResource().Group && br.Resource == attr.GetResource().Resource {
				return logicalcluster.Name(apiBinding.Status.APIExportClusterName), nil
			}
		}
	}
	return "", nil
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
func (p *WebhookDispatcher) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	p.getAPIBindings = func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIBinding, error) {
		return local.Apis().V1alpha1().APIBindings().Lister().Cluster(clusterName).List(labels.Everything())
	}

	synced := func() bool {
		return local.Apis().V1alpha1().APIBindings().Informer().HasSynced() &&
			local.Apis().V1alpha1().APIExports().Informer().HasSynced() &&
			global.Apis().V1alpha1().APIExports().Informer().HasSynced()
	}
	p.SetReadyFunc(synced)
	p.informersHaveSynced = synced

	indexers.AddIfNotPresentOrDie(local.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})

	indexers.AddIfNotPresentOrDie(global.Apis().V1alpha1().APIExports().Informer().GetIndexer(), cache.Indexers{
		indexers.ByLogicalClusterPathAndName: indexers.IndexByLogicalClusterPathAndName,
	})
}
