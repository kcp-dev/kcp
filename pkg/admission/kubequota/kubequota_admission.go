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

package kubequota

import (
	"context"
	"fmt"
	"io"
	"sync"

	kcpcorev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	resourcequotaapi "k8s.io/apiserver/pkg/admission/plugin/resourcequota/apis/resourcequota"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota/apis/resourcequota/validation"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/informerfactoryhack"
	quota "k8s.io/apiserver/pkg/quota/v1"
	"k8s.io/client-go/informers"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyv1alpha1listers "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
)

// PluginName is the name of this admission plugin.
const PluginName = "KCPKubeResourceQuota"

// Register registers this admission plugin.
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(config io.Reader) (admission.Interface, error) {
			configuration, err := resourcequota.LoadConfiguration(config)
			if err != nil {
				return nil, err
			}

			if configuration != nil {
				if errs := validation.ValidateConfiguration(configuration); len(errs) > 0 {
					return nil, errs.ToAggregate()
				}
			}

			return NewKubeResourceQuota(configuration), nil
		},
	)
}

// NewKubeResourceQuota returns a new KubeResourceQuota admission plugin.
func NewKubeResourceQuota(config *resourcequotaapi.Configuration) *KubeResourceQuota {
	return &KubeResourceQuota{
		Handler: admission.NewHandler(admission.Create, admission.Update),

		userSuppliedConfiguration: config,

		delegates: map[logicalcluster.Name]*stoppableQuotaAdmission{},
	}
}

// KubeResourceQuota is an admission plugin that handles quota per logical cluster.
type KubeResourceQuota struct {
	*admission.Handler

	// Injected/set via initializers
	thisWorkspaceInformer        tenancyv1alpha1informers.ThisWorkspaceClusterInformer
	thisWorkspaceLister          tenancyv1alpha1listers.ThisWorkspaceClusterLister
	kubeClusterClient            kcpkubernetesclientset.ClusterInterface
	scopingResourceQuotaInformer kcpcorev1informers.ResourceQuotaClusterInformer
	quotaConfiguration           quota.Configuration
	serverDone                   <-chan struct{}

	// Manually set
	userSuppliedConfiguration *resourcequotaapi.Configuration

	lock      sync.RWMutex
	delegates map[logicalcluster.Name]*stoppableQuotaAdmission

	clusterWorkspaceDeletionMonitorStarter sync.Once
}

// ValidateInitialization validates all the expected fields are set.
func (k *KubeResourceQuota) ValidateInitialization() error {
	if k.thisWorkspaceLister == nil {
		return fmt.Errorf("missing thisWorkspaceLister")
	}
	if k.kubeClusterClient == nil {
		return fmt.Errorf("missing kubeClusterClient")
	}
	if k.scopingResourceQuotaInformer == nil {
		return fmt.Errorf("missing scopingResourceQuotaInformer")
	}
	if k.quotaConfiguration == nil {
		return fmt.Errorf("missing quotaConfiguration")
	}
	if k.serverDone == nil {
		return fmt.Errorf("missing serverDone")
	}

	return nil
}

var _ admission.ValidationInterface = &KubeResourceQuota{}
var _ = initializers.WantsKcpInformers(&KubeResourceQuota{})
var _ = initializer.WantsExternalKubeInformerFactory(&KubeResourceQuota{})
var _ = initializers.WantsKubeClusterClient(&KubeResourceQuota{})
var _ = initializer.WantsQuotaConfiguration(&KubeResourceQuota{})
var _ = initializers.WantsServerShutdownChannel(&KubeResourceQuota{})

// Validate gets or creates a resourcequota.QuotaAdmission plugin for the logical cluster in the request and then
// delegates validation to it.
func (k *KubeResourceQuota) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	// TODO:(p0lyn0mial): the following checks are temporary only to get the phase A merged
	if a.GetResource() == tenancyv1alpha1.SchemeGroupVersion.WithResource("thisworkspaces") {
		return nil
	}
	if a.GetResource() == rbacv1.SchemeGroupVersion.WithResource("clusterrolebindings") {
		allowedNames := sets.NewString("workspace-admin", "workspace-admin-legacy", "workspace-access-legacy")
		if allowedNames.Has(a.GetName()) {
			return nil
		}
	}

	k.clusterWorkspaceDeletionMonitorStarter.Do(func() {
		m := newClusterWorkspaceDeletionMonitor(k.thisWorkspaceInformer, k.stopQuotaAdmissionForCluster)
		go m.Start(k.serverDone)
	})

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return err
	}

	delegate, err := k.getOrCreateDelegate(cluster.Name)
	if err != nil {
		return err
	}

	return delegate.Validate(ctx, a, o)
}

// getOrCreateDelegate creates a resourcequota.QuotaAdmission plugin for clusterName.
func (k *KubeResourceQuota) getOrCreateDelegate(clusterName logicalcluster.Name) (*stoppableQuotaAdmission, error) {
	k.lock.RLock()
	delegate := k.delegates[clusterName]
	k.lock.RUnlock()

	if delegate != nil {
		return delegate, nil
	}

	k.lock.Lock()
	defer k.lock.Unlock()

	delegate = k.delegates[clusterName]
	if delegate != nil {
		return delegate, nil
	}

	// Set up a context that is cancelable and that is bounded by k.serverDone
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// Wait for either the context or the server to be done. If it's the server, cancel the context.
		select {
		case <-ctx.Done():
		case <-k.serverDone:
			cancel()
		}
	}()

	const evaluatorWorkersPerWorkspace = 5
	quotaAdmission, err := resourcequota.NewResourceQuota(k.userSuppliedConfiguration, evaluatorWorkersPerWorkspace, ctx.Done())
	if err != nil {
		cancel()
		return nil, err
	}

	delegate = &stoppableQuotaAdmission{
		QuotaAdmission: quotaAdmission,
		stop:           cancel,
	}

	delegate.SetResourceQuotaLister(k.scopingResourceQuotaInformer.Cluster(clusterName).Lister())
	delegate.SetExternalKubeClientSet(k.kubeClusterClient.Cluster(clusterName.Path()))
	delegate.SetQuotaConfiguration(k.quotaConfiguration)

	if err := delegate.ValidateInitialization(); err != nil {
		cancel()
		return nil, err
	}

	k.delegates[clusterName] = delegate

	return delegate, nil
}

type stoppableQuotaAdmission struct {
	*resourcequota.QuotaAdmission
	stop func()
}

func (k *KubeResourceQuota) stopQuotaAdmissionForCluster(clusterName logicalcluster.Name) {
	k.lock.Lock()
	defer k.lock.Unlock()

	delegate := k.delegates[clusterName]

	if delegate == nil {
		klog.V(3).InfoS("Received event to stop quota admission for logical cluster, but it wasn't in the map", "clusterName", clusterName)
		return
	}

	klog.V(2).InfoS("Stopping quota admission for logical cluster", "clusterName", clusterName)

	delete(k.delegates, clusterName)
	delegate.stop()
}

func (k *KubeResourceQuota) SetKubeClusterClient(kubeClusterClient kcpkubernetesclientset.ClusterInterface) {
	k.kubeClusterClient = kubeClusterClient
}

func (k *KubeResourceQuota) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	k.thisWorkspaceLister = informers.Tenancy().V1alpha1().ThisWorkspaces().Lister()
	k.thisWorkspaceInformer = informers.Tenancy().V1alpha1().ThisWorkspaces()
}

func (k *KubeResourceQuota) SetExternalKubeInformerFactory(informers informers.SharedInformerFactory) {
	k.scopingResourceQuotaInformer = informerfactoryhack.Unwrap(informers).Core().V1().ResourceQuotas()

	// Make sure the quota informer gets started
	_ = informerfactoryhack.Unwrap(informers).Core().V1().ResourceQuotas().Informer()
}

func (k *KubeResourceQuota) SetQuotaConfiguration(quotaConfiguration quota.Configuration) {
	k.quotaConfiguration = quotaConfiguration
}

func (k *KubeResourceQuota) SetServerShutdownChannel(ch <-chan struct{}) {
	k.serverDone = ch
}
