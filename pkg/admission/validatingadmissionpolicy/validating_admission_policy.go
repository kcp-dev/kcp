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

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/admission/initializer"
	"k8s.io/apiserver/pkg/admission/plugin/validatingadmissionpolicy"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/pkg/admission/kubequota"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/core/v1alpha1"
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
		delegates: make(map[logicalcluster.Name]*stoppableValidatingAdmissionPolicy),
	}
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
	featureGates                    featuregate.FeatureGate

	lock      sync.RWMutex
	delegates map[logicalcluster.Name]*stoppableValidatingAdmissionPolicy

	logicalClusterDeletionMonitorStarter sync.Once
}

var _ admission.ValidationInterface = &KubeValidatingAdmissionPolicy{}
var _ = initializers.WantsKcpInformers(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsKubeClusterClient(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsServerShutdownChannel(&KubeValidatingAdmissionPolicy{})
var _ = initializers.WantsDynamicClusterClient(&KubeValidatingAdmissionPolicy{})
var _ = initializer.WantsFeatures(&KubeValidatingAdmissionPolicy{})
var _ = admission.InitializationValidator(&KubeValidatingAdmissionPolicy{})

func (k *KubeValidatingAdmissionPolicy) SetKubeClusterClient(kubeClusterClient kcpkubernetesclientset.ClusterInterface) {
	k.kubeClusterClient = kubeClusterClient
}

func (k *KubeValidatingAdmissionPolicy) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	k.logicalClusterInformer = local.Core().V1alpha1().LogicalClusters()
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

func (k *KubeValidatingAdmissionPolicy) InspectFeatureGates(featureGates featuregate.FeatureGate) {
	k.featureGates = featureGates
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

	delegate, err := k.getOrCreateDelegate(cluster.Name)
	if err != nil {
		return err
	}

	return delegate.Validate(ctx, a, o)
}

// getOrCreateDelegate creates an actual plugin for clusterName.
func (k *KubeValidatingAdmissionPolicy) getOrCreateDelegate(clusterName logicalcluster.Name) (*stoppableValidatingAdmissionPolicy, error) {
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

	plugin, err := validatingadmissionpolicy.NewPlugin()
	if err != nil {
		return nil, err
	}

	delegate = &stoppableValidatingAdmissionPolicy{
		CELAdmissionPlugin: plugin,
		stop:               cancel,
	}

	plugin.SetNamespaceInformer(k.localKubeSharedInformerFactory.Core().V1().Namespaces().Cluster(clusterName))
	plugin.SetValidatingAdmissionPoliciesInformer(k.globalKubeSharedInformerFactory.Admissionregistration().V1alpha1().ValidatingAdmissionPolicies().Cluster(clusterName))
	plugin.SetValidatingAdmissionPolicyBindingsInformer(k.globalKubeSharedInformerFactory.Admissionregistration().V1alpha1().ValidatingAdmissionPolicyBindings().Cluster(clusterName))
	plugin.SetExternalKubeClientSet(k.kubeClusterClient.Cluster(clusterName.Path()))

	// TODO(ncdc): this is super inefficient to do per workspace
	discoveryClient := memory.NewMemCacheClient(k.kubeClusterClient.Cluster(clusterName.Path()).Discovery())
	restMapper := restmapper.NewDeferredDiscoveryRESTMapper(discoveryClient)
	plugin.SetRESTMapper(restMapper)

	plugin.SetDynamicClient(k.dynamicClusterClient.Cluster(clusterName.Path()))
	plugin.SetDrainedNotification(ctx.Done())
	plugin.InspectFeatureGates(k.featureGates)

	if err := plugin.ValidateInitialization(); err != nil {
		cancel()
		return nil, err
	}

	k.delegates[clusterName] = delegate

	return delegate, nil
}

func (k *KubeValidatingAdmissionPolicy) logicalClusterDeleted(clusterName logicalcluster.Name) {
	k.lock.Lock()
	defer k.lock.Unlock()

	delegate := k.delegates[clusterName]

	logger := klog.Background().WithValues("clusterName", clusterName)

	if delegate == nil {
		logger.V(3).Info("received event to stop validating admission policy for logical cluster, but it wasn't in the map")
		return
	}

	logger.V(2).Info("stopping validating admission policy for logical cluster")

	delete(k.delegates, clusterName)
	delegate.stop()
}

type stoppableValidatingAdmissionPolicy struct {
	*validatingadmissionpolicy.CELAdmissionPlugin
	stop func()
}
