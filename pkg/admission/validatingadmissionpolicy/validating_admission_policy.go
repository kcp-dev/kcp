/*
Copyright 2023 The KCP Authors.

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

package validatingadmissionpolicy

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
	"k8s.io/apiserver/pkg/admission/plugin/policy/validating"
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

const PluginName = "KCPValidatingAdmissionPolicy"

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(config io.Reader) (admission.Interface, error) {
			return NewKubeValidatingAdmissionPolicy(), nil
		},
	)
}

func NewKubeValidatingAdmissionPolicy() *KubeValidatingAdmissionPolicy {
	return &KubeValidatingAdmissionPolicy{
		Handler:   admission.NewHandler(admission.Connect, admission.Create, admission.Delete, admission.Update),
		delegates: make(map[delegateKey]*stoppableValidatingAdmissionPolicy),
	}
}

type delegateKey struct {
	policyCluster logicalcluster.Name
	targetCluster logicalcluster.Name
}

type KubeValidatingAdmissionPolicy struct {
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
	delegates     map[delegateKey]*stoppableValidatingAdmissionPolicy

	logicalClusterDeletionMonitorStarter sync.Once
}

var _ admission.ValidationInterface = &KubeValidatingAdmissionPolicy{}
var _ = initializers.WantsKubeClusterClient(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsKubeInformers(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsKcpInformers(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsServerShutdownChannel(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsDynamicClusterClient(&KubeValidatingAdmissionPolicy{})
var _ = initializer.WantsAuthorizer(&KubeValidatingAdmissionPolicy{})
var _ = admission.InitializationValidator(&KubeValidatingAdmissionPolicy{})

func (k *KubeValidatingAdmissionPolicy) SetKubeClusterClient(kubeClusterClient kcpkubernetesclientset.ClusterInterface) {
	k.kubeClusterClient = kubeClusterClient
}

func (k *KubeValidatingAdmissionPolicy) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
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

func (k *KubeValidatingAdmissionPolicy) SetKubeInformers(local, global kcpkubernetesinformers.SharedInformerFactory) {
	k.localKubeSharedInformerFactory = local
	k.globalKubeSharedInformerFactory = global
}

func (k *KubeValidatingAdmissionPolicy) SetServerShutdownChannel(ch <-chan struct{}) {
	k.serverDone = ch
}

func (k *KubeValidatingAdmissionPolicy) SetDynamicClusterClient(c kcpdynamic.ClusterInterface) {
	k.dynamicClusterClient = c
}

func (k *KubeValidatingAdmissionPolicy) SetAuthorizer(authz authorizer.Authorizer) {
	k.authorizer = authz
}

func (k *KubeValidatingAdmissionPolicy) ValidateInitialization() error {
	return nil
}

func (k *KubeValidatingAdmissionPolicy) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {
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

	return delegate.Validate(ctx, a, o)
}

func (k *KubeValidatingAdmissionPolicy) getSourceClusterForGroupResource(clusterName logicalcluster.Name, groupResource schema.GroupResource) (logicalcluster.Name, error) {
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
// targetClusterName is the cluster where the object being validated resides.
func (k *KubeValidatingAdmissionPolicy) getOrCreateDelegate(policyClusterName, targetClusterName logicalcluster.Name) (*stoppableValidatingAdmissionPolicy, error) {
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

	plugin := validating.NewPlugin(nil)

	delegate = &stoppableValidatingAdmissionPolicy{
		Plugin: plugin,
		stop:   cancel,
	}

	// Use the target cluster's namespace informer, as that's where the objects being validated reside.
	//
	// The namespace informer points to where the TARGET OBJECT (being admitted) is located,
	// NOT where the policy is defined.
	//
	// Flow:
	//   1. Admission request arrives for an object in namespace "foo"
	//   2. Matcher retrieves namespace "foo" using the informer: matcher.GetNamespace("foo")
	//   3. Namespace is used for:
	//      - Label matching: Check if namespace labels match policy's namespaceSelector
	//      - CEL evaluation: Available as "namespaceObject" variable in policy expressions
	//      - Policy decisions: Evaluate rules based on target namespace metadata
	plugin.SetNamespaceInformer(k.localKubeSharedInformerFactory.Core().V1().Namespaces().Cluster(targetClusterName))
	plugin.SetExternalKubeClientSet(k.kubeClusterClient.Cluster(policyClusterName.Path()))

	// TODO(ncdc): this is super inefficient to do per workspace
	discoveryClient := memory.NewMemCacheClient(k.kubeClusterClient.Cluster(policyClusterName.Path()).Discovery())
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	plugin.SetRESTMapper(restMapper)

	plugin.SetDynamicClient(k.dynamicClusterClient.Cluster(policyClusterName.Path()))
	plugin.SetDrainedNotification(ctx.Done())
	plugin.SetAuthorizer(k.authorizer)
	plugin.SetClusterName(policyClusterName)
	plugin.SetSourceFactory(func(_ informers.SharedInformerFactory, client kubernetes.Interface, dynamicClient dynamic.Interface, restMapper meta.RESTMapper, cn logicalcluster.Name) generic.Source[validating.PolicyHook] {
		return generic.NewPolicySource(
			k.globalKubeSharedInformerFactory.Admissionregistration().V1().ValidatingAdmissionPolicies().Informer().Cluster(cn),
			k.globalKubeSharedInformerFactory.Admissionregistration().V1().ValidatingAdmissionPolicyBindings().Informer().Cluster(cn),
			validating.NewValidatingAdmissionPolicyAccessor,
			validating.NewValidatingAdmissionPolicyBindingAccessor,
			validating.CompilePolicy,
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

func (k *KubeValidatingAdmissionPolicy) logicalClusterDeleted(clusterName logicalcluster.Name) {
	k.delegatesLock.Lock()
	defer k.delegatesLock.Unlock()

	logger := klog.Background().WithValues("clusterName", clusterName)

	// Remove all delegates that involve this cluster (either as policy or target)
	found := false
	for key, delegate := range k.delegates {
		if key.policyCluster == clusterName || key.targetCluster == clusterName {
			logger.V(2).Info("stopping validating admission policy for logical cluster")
			delete(k.delegates, key)
			delegate.stop()
			found = true
		}
	}

	if !found {
		logger.V(3).Info("received event to stop validating admission policy for logical cluster, but it wasn't in the map")
	}
}

type stoppableValidatingAdmissionPolicy struct {
	*validating.Plugin
	stop func()
}
