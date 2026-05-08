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

package mutatingadmissionpolicy

import (
	"context"
	"io"
	"sync"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/admission/plugin/policy/generic"
	"k8s.io/apiserver/pkg/admission/plugin/policy/mutating"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	kcpinformers "github.com/kcp-dev/sdk/client/informers/externalversions"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/admission/kubequota"
)

const PluginName = "KCPMutatingAdmissionPolicy"

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(config io.Reader) (admission.Interface, error) {
			return NewKubeMutatingAdmissionPolicy(), nil
		},
	)
}

func NewKubeMutatingAdmissionPolicy() *KubeMutatingAdmissionPolicy {
	return &KubeMutatingAdmissionPolicy{
		Handler:   admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
		delegates: make(map[delegateKey]*stoppableMutatingAdmissionPolicy),
	}
}

type delegateKey struct {
	policyCluster logicalcluster.Name
	targetCluster logicalcluster.Name
}

type KubeMutatingAdmissionPolicy struct {
	*admission.Handler

	// Injected/set via initializers
	logicalClusterInformer          corev1alpha1informers.LogicalClusterClusterInformer
	kubeClusterClient               kcpkubernetesclientset.ClusterInterface
	dynamicClusterClient            kcpdynamic.ClusterInterface
	localKubeSharedInformerFactory  kcpkubernetesinformers.SharedInformerFactory
	globalKubeSharedInformerFactory kcpkubernetesinformers.SharedInformerFactory
	serverDone                      <-chan struct{}
	authorizer                      authorizer.Authorizer

	getAPIBindings func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error)

	delegatesLock sync.RWMutex
	delegates     map[delegateKey]*stoppableMutatingAdmissionPolicy

	logicalClusterDeletionMonitorStarter sync.Once
}

var _ admission.MutationInterface = &KubeMutatingAdmissionPolicy{}
var _ = initializers.WantsKubeClusterClient(&KubeMutatingAdmissionPolicy{})
var _ = initializers.WantsKubeInformers(&KubeMutatingAdmissionPolicy{})
var _ = initializers.WantsKcpInformers(&KubeMutatingAdmissionPolicy{})
var _ = initializers.WantsServerShutdownChannel(&KubeMutatingAdmissionPolicy{})
var _ = initializers.WantsDynamicClusterClient(&KubeMutatingAdmissionPolicy{})
var _ = initializer.WantsAuthorizer(&KubeMutatingAdmissionPolicy{})
var _ = admission.InitializationValidator(&KubeMutatingAdmissionPolicy{})

func (k *KubeMutatingAdmissionPolicy) SetKubeClusterClient(kubeClusterClient kcpkubernetesclientset.ClusterInterface) {
	k.kubeClusterClient = kubeClusterClient
}

func (k *KubeMutatingAdmissionPolicy) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	k.logicalClusterInformer = local.Core().V1alpha1().LogicalClusters()
	k.getAPIBindings = func(clusterName logicalcluster.Name) ([]*apisv1alpha2.APIBinding, error) {
		return local.Apis().V1alpha2().APIBindings().Lister().Cluster(clusterName).List(labels.Everything())
	}

	_, _ = local.Core().V1alpha1().LogicalClusters().Informer().AddEventHandler(
		cache.ResourceEventHandlerFuncs{
			DeleteFunc: func(obj interface{}) {
				cl, ok := obj.(*corev1alpha1.LogicalCluster)
				if !ok {
					return
				}

				clName := logicalcluster.Name(cl.Annotations[logicalcluster.AnnotationKey])

				k.delegatesLock.Lock()
				defer k.delegatesLock.Unlock()

				// Remove all delegates that involve this cluster (either as policy or target)
				for key, delegate := range k.delegates {
					if key.policyCluster == clName || key.targetCluster == clName {
						delete(k.delegates, key)
						delegate.stop()
					}
				}
			},
		},
	)
}

func (k *KubeMutatingAdmissionPolicy) SetKubeInformers(local, global kcpkubernetesinformers.SharedInformerFactory) {
	k.localKubeSharedInformerFactory = local
	k.globalKubeSharedInformerFactory = global
}

func (k *KubeMutatingAdmissionPolicy) SetServerShutdownChannel(ch <-chan struct{}) {
	k.serverDone = ch
}

func (k *KubeMutatingAdmissionPolicy) SetDynamicClusterClient(c kcpdynamic.ClusterInterface) {
	k.dynamicClusterClient = c
}

func (k *KubeMutatingAdmissionPolicy) SetAuthorizer(authz authorizer.Authorizer) {
	k.authorizer = authz
}

func (k *KubeMutatingAdmissionPolicy) ValidateInitialization() error {
	return nil
}

func (k *KubeMutatingAdmissionPolicy) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
	k.logicalClusterDeletionMonitorStarter.Do(func() {
		m := kubequota.NewLogicalClusterDeletionMonitor("kubequota-logicalcluster-deletion-monitor", k.logicalClusterInformer, k.logicalClusterDeleted)
		go m.Start(k.serverDone)
	})

	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return err
	}

	sourceCluster, err := k.getSourceClusterForGroupResource(cluster.Name, a.GetResource().GroupResource())
	if err != nil {
		return err
	}

	delegate, err := k.getOrCreateDelegate(sourceCluster, cluster.Name)
	if err != nil {
		return err
	}

	return delegate.Admit(ctx, a, o)
}

func (k *KubeMutatingAdmissionPolicy) getSourceClusterForGroupResource(clusterName logicalcluster.Name, groupResource schema.GroupResource) (logicalcluster.Name, error) {
	objs, err := k.getAPIBindings(clusterName)
	if err != nil {
		return "", err
	}

	for _, apiBinding := range objs {
		for _, br := range apiBinding.Status.BoundResources {
			if br.Group == groupResource.Group && br.Resource == groupResource.Resource {
				return logicalcluster.Name(apiBinding.Status.APIExportClusterName), nil
			}
		}
	}

	return clusterName, nil
}

// getOrCreateDelegate creates an actual plugin for policyClusterName (where policies are defined).
// targetClusterName is the cluster where the object being mutated resides.
func (k *KubeMutatingAdmissionPolicy) getOrCreateDelegate(policyClusterName, targetClusterName logicalcluster.Name) (*stoppableMutatingAdmissionPolicy, error) {
	key := delegateKey{
		policyCluster: policyClusterName,
		targetCluster: targetClusterName,
	}

	k.delegatesLock.RLock()
	delegate := k.delegates[key]
	k.delegatesLock.RUnlock()

	if delegate != nil {
		return delegate, nil
	}

	k.delegatesLock.Lock()
	defer k.delegatesLock.Unlock()

	delegate = k.delegates[key]
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

	plugin, err := mutating.NewPlugin(nil)
	if err != nil {
		return nil, err
	}

	delegate = &stoppableMutatingAdmissionPolicy{
		Plugin: plugin,
		stop:   cancel,
	}

	plugin.SetNamespaceInformer(k.localKubeSharedInformerFactory.Core().V1().Namespaces().Cluster(targetClusterName))
	plugin.SetExternalKubeClientSet(k.kubeClusterClient.Cluster(policyClusterName.Path()))

	discoveryClient := memory.NewMemCacheClient(k.kubeClusterClient.Cluster(policyClusterName.Path()).Discovery())
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	plugin.SetRESTMapper(restMapper)

	plugin.SetDynamicClient(k.dynamicClusterClient.Cluster(policyClusterName.Path()))
	plugin.SetDrainedNotification(ctx.Done())
	plugin.SetAuthorizer(k.authorizer)
	plugin.SetClusterName(policyClusterName)
	plugin.SetSourceFactory(func(_ informers.SharedInformerFactory, client kubernetes.Interface, dynamicClient dynamic.Interface, restMapper meta.RESTMapper, cn logicalcluster.Name) generic.Source[mutating.PolicyHook] {
		return generic.NewPolicySource(
			k.globalKubeSharedInformerFactory.Admissionregistration().V1().MutatingAdmissionPolicies().Informer().Cluster(cn),
			k.globalKubeSharedInformerFactory.Admissionregistration().V1().MutatingAdmissionPolicyBindings().Informer().Cluster(cn),
			mutating.NewMutatingAdmissionPolicyAccessor,
			mutating.NewMutatingAdmissionPolicyBindingAccessor,
			mutating.CompilePolicy,
			nil,
			dynamicClient,
			restMapper,
			cn,
		)
	})

	if err := plugin.ValidateInitialization(); err != nil {
		cancel()
		return nil, err
	}

	k.delegates[key] = delegate

	return delegate, nil
}

func (k *KubeMutatingAdmissionPolicy) logicalClusterDeleted(clusterName logicalcluster.Name) {
	k.delegatesLock.Lock()
	defer k.delegatesLock.Unlock()

	logger := klog.Background().WithValues("clusterName", clusterName)

	// Remove all delegates that involve this cluster (either as policy or target)
	found := false
	for key, delegate := range k.delegates {
		if key.policyCluster == clusterName || key.targetCluster == clusterName {
			logger.V(2).Info("stopping mutating admission policy for logical cluster")
			delete(k.delegates, key)
			delegate.stop()
			found = true
		}
	}

	if !found {
		logger.V(3).Info("received event to stop mutating admission policy for logical cluster, but it wasn't in the map")
	}
}

type stoppableMutatingAdmissionPolicy struct {
	*mutating.Plugin
	stop func()
}
