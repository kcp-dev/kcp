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

	"github.com/kcp-dev/logicalcluster"

	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	resourcequotaapi "k8s.io/apiserver/pkg/admission/plugin/resourcequota/apis/resourcequota"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota/apis/resourcequota/validation"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	quota "k8s.io/apiserver/pkg/quota/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/informers/core"
	v1 "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	tenancyinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	kubequotacontroller "github.com/kcp-dev/kcp/pkg/reconciler/kubequota"
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
	clusterWorkspaceInformer tenancyinformers.ClusterWorkspaceInformer
	clusterWorkspaceLister   tenancylisters.ClusterWorkspaceLister
	kubeClusterClient        kubernetes.ClusterInterface
	kubeInformers            informers.SharedInformerFactory
	quotaConfiguration       quota.Configuration
	serverDone               <-chan struct{}

	// Manually set
	userSuppliedConfiguration *resourcequotaapi.Configuration

	lock      sync.RWMutex
	delegates map[logicalcluster.Name]*stoppableQuotaAdmission

	once sync.Once
}

// ValidateInitialization validates all the expected fields are set.
func (k *KubeResourceQuota) ValidateInitialization() error {
	if k.clusterWorkspaceLister == nil {
		return fmt.Errorf("missing clusterWorkspaceLister")
	}
	if k.kubeClusterClient == nil {
		return fmt.Errorf("missing kubeClusterClient")
	}
	if k.kubeInformers == nil {
		return fmt.Errorf("missing kubeInformers")
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
	k.once.Do(func() {
		m := newClusterWorkspaceDeletionMonitor(k.clusterWorkspaceInformer, k.stopQuotaAdmissionForCluster)
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

	// hackyA := &hackyAdmissionAttributesForBuiltInTypes{
	// 	Attributes: a,
	// }

	return delegate.Validate(ctx, a, o)
}

// TODO(ncdc): find a way to support the special evaluators for pods/pvcs/services.
// var (
// 	podsGVR                   = corev1.SchemeGroupVersion.WithResource("pods")
// 	servicesGVR               = corev1.SchemeGroupVersion.WithResource("services")
// 	persistentVolumeClaimsGVR = corev1.SchemeGroupVersion.WithResource("persistentvolumeclaims")
// )
//
// type hackyAdmissionAttributesForBuiltInTypes struct {
// 	admission.Attributes
// 	builtInCurrent runtime.Object
// 	builtInOld     runtime.Object
// }
//
// func (h *hackyAdmissionAttributesForBuiltInTypes) GetObject() runtime.Object {
// 	if h.builtInCurrent != nil {
// 		return h.builtInCurrent
// 	}
//
// 	obj, converted := objectFor(h.GetResource(), h.Attributes.GetObject())
// 	if converted {
// 		h.builtInCurrent = obj
// 	}
//
// 	return obj
// }
//
// func (h *hackyAdmissionAttributesForBuiltInTypes) GetOldObject() runtime.Object {
// 	if h.builtInOld != nil {
// 		return h.builtInOld
// 	}
//
// 	obj, converted := objectFor(h.GetResource(), h.Attributes.GetOldObject())
// 	if converted {
// 		h.builtInOld = obj
// 	}
//
// 	return obj
// }
//
// func objectFor(gvr schema.GroupVersionResource, obj runtime.Object) (out runtime.Object, converted bool) {
// 	switch gvr {
// 	case podsGVR:
// 		pod := &corev1.Pod{}
// 		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(runtime.Unstructured).UnstructuredContent(), pod); err != nil {
// 			utilruntime.HandleError(fmt.Errorf("error converting unstructured to pod: %w", err))
// 		}
//
// 		return pod, true
// 	case servicesGVR:
// 		service := &corev1.Service{}
// 		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(runtime.Unstructured).UnstructuredContent(), service); err != nil {
// 			utilruntime.HandleError(fmt.Errorf("error converting unstructured to service: %w", err))
// 		}
//
// 		return service, true
// 	case persistentVolumeClaimsGVR:
// 		pvc := &corev1.PersistentVolumeClaim{}
// 		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.(runtime.Unstructured).UnstructuredContent(), pvc); err != nil {
// 			utilruntime.HandleError(fmt.Errorf("error converting unstructured to persistent volume claim: %w", err))
// 		}
//
// 		return pvc, true
// 	}
//
// 	return obj, false
// }

// getOrCreateDelegate creates a resourcequota.QuotaAdmission plugin for clusterName.
func (k *KubeResourceQuota) getOrCreateDelegate(clusterName logicalcluster.Name) (*stoppableQuotaAdmission, error) {
	var delegate *stoppableQuotaAdmission

	k.lock.RLock()
	delegate = k.delegates[clusterName]
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

	// Set up a context that is cancelable and that is bounded by k.ServerDone
	ctx, cancel := context.WithCancel(context.Background())
	go func(done <-chan struct{}) {
		<-done
		cancel()
	}(k.serverDone)

	quotaAdmission, err := resourcequota.NewResourceQuota(k.userSuppliedConfiguration, 5, ctx.Done())
	if err != nil {
		return nil, err
	}

	delegate = &stoppableQuotaAdmission{
		QuotaAdmission: quotaAdmission,
		stopFunc:       cancel,
	}

	delegate.SetExternalKubeInformerFactory(&scopedResourceQuotaInformerFactory{
		SharedInformerFactory: k.kubeInformers,
		clusterName:           clusterName,
	})

	delegate.SetExternalKubeClientSet(k.kubeClusterClient.Cluster(clusterName))

	delegate.SetQuotaConfiguration(k.quotaConfiguration)

	if err := delegate.ValidateInitialization(); err != nil {
		return nil, err
	}

	k.delegates[clusterName] = delegate

	return delegate, nil
}

type stoppableQuotaAdmission struct {
	*resourcequota.QuotaAdmission
	stopFunc func()
}

func (s *stoppableQuotaAdmission) stop() {
	s.stopFunc()
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

func (k *KubeResourceQuota) SetKubeClusterClient(kubeClusterClient *kubernetes.Cluster) {
	k.kubeClusterClient = kubeClusterClient
}

func (k *KubeResourceQuota) SetKcpInformers(informers kcpinformers.SharedInformerFactory) {
	k.clusterWorkspaceLister = informers.Tenancy().V1alpha1().ClusterWorkspaces().Lister()
	k.clusterWorkspaceInformer = informers.Tenancy().V1alpha1().ClusterWorkspaces()
}

func (k *KubeResourceQuota) SetExternalKubeInformerFactory(informers informers.SharedInformerFactory) {
	k.kubeInformers = informers
	// Make sure the quota informer gets started
	_ = informers.Core().V1().ResourceQuotas().Informer()
}

func (k *KubeResourceQuota) SetQuotaConfiguration(quotaConfiguration quota.Configuration) {
	k.quotaConfiguration = quotaConfiguration
}

func (k *KubeResourceQuota) SetServerShutdownChannel(ch <-chan struct{}) {
	k.serverDone = ch
}

// scopedResourceQuotaInformerFactory embeds an informers.SharedInformerFactory instance, overriding
// .Core().V1().ResourceQuotas() to be scoped to a single logical cluster. All other informers are untouched.
type scopedResourceQuotaInformerFactory struct {
	informers.SharedInformerFactory
	clusterName logicalcluster.Name
}

func (f *scopedResourceQuotaInformerFactory) Core() core.Interface {
	return &scopedResourceQuotaInformerFactoryCore{
		Interface:   f.SharedInformerFactory.Core(),
		clusterName: f.clusterName,
	}
}

type scopedResourceQuotaInformerFactoryCore struct {
	core.Interface
	clusterName logicalcluster.Name
}

func (i *scopedResourceQuotaInformerFactoryCore) V1() v1.Interface {
	return &scopedResourceQuotaInformerFactoryCoreV1{
		Interface:   i.Interface.V1(),
		clusterName: i.clusterName,
	}
}

type scopedResourceQuotaInformerFactoryCoreV1 struct {
	v1.Interface
	clusterName logicalcluster.Name
}

func (i *scopedResourceQuotaInformerFactoryCoreV1) ResourceQuotas() v1.ResourceQuotaInformer {
	return kubequotacontroller.NewSingleClusterResourceQuotaInformer(i.clusterName, i.Interface.ResourceQuotas())
}
