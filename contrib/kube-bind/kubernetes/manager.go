/*
Copyright 2022 The Kube Bind Authors.

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

package kubernetes

import (
	"context"
	"fmt"

	corev1informers "github.com/kcp-dev/client-go/informers/core/v1"
	kubeclient "github.com/kcp-dev/client-go/kubernetes"
	corev1listers "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"
	bindclient "github.com/kube-bind/kube-bind/pkg/client/clientset/versioned"
	"github.com/kube-bind/kube-bind/pkg/indexers"
	bindinformers "github.com/kube-bind/kube-bind/sdk/kcp/informers/externalversions"
	bindlisters "github.com/kube-bind/kube-bind/sdk/kcp/listers/kubebind/v1alpha1"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	kuberesources "github.com/kcp-dev/kcp/contrib/kube-bind/kubernetes/resources"
)

type Manager struct {
	namespacePrefix    string
	providerPrettyName string

	clusterConfig         *rest.Config
	externalAddress       string
	externalCA            []byte
	externalTLSServerName string

	kubeClient kubeclient.ClusterInterface
	bindClient bindclient.Interface

	namespaceLister  corev1listers.NamespaceClusterLister
	namespaceIndexer cache.Indexer

	exportLister  bindlisters.APIServiceExportClusterLister
	exportIndexer cache.Indexer
}

func NewKubernetesManager(
	namespacePrefix, providerPrettyName string,
	config *rest.Config,
	externalAddress string,
	externalCA []byte,
	externalTLSServerName string,
	namespaceInformer corev1informers.NamespaceClusterInformer,
	exportInformer bindinformers.SharedInformerFactory,
) (*Manager, error) {
	config = rest.CopyConfig(config)
	config = rest.AddUserAgent(config, "kube-bind-example-backend-kubernetes-manager")

	kubeClient, err := kubeclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	bindClient, err := bindclient.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		namespacePrefix:    namespacePrefix,
		providerPrettyName: providerPrettyName,

		clusterConfig:         config,
		externalAddress:       externalAddress,
		externalCA:            externalCA,
		externalTLSServerName: externalTLSServerName,

		kubeClient: kubeClient,
		bindClient: bindClient,

		namespaceLister:  namespaceInformer.Lister(),
		namespaceIndexer: namespaceInformer.Informer().GetIndexer(),

		exportLister:  exportInformer.KubeBind().V1alpha1().APIServiceExports().Lister(),
		exportIndexer: exportInformer.KubeBind().V1alpha1().APIServiceBindings().Informer().GetIndexer(),
	}

	indexers.AddIfNotPresentOrDie(m.namespaceIndexer, cache.Indexers{
		NamespacesByIdentity: IndexNamespacesByIdentity,
	})

	return m, nil
}

func (m *Manager) HandleResources(ctx context.Context, identity, resource, group string) ([]byte, error) {
	logger := klog.FromContext(ctx).WithValues("identity", identity, "resource", resource, "group", group)
	ctx = klog.NewContext(ctx, logger)

	// TODO: Fix this
	fakeCluster := logicalcluster.NewPath("fake-cluster")

	// try to find an existing namespace by annotation, or create a new one.
	nss, err := m.namespaceIndexer.ByIndex(NamespacesByIdentity, identity)
	if err != nil {
		return nil, err
	}
	if len(nss) > 1 {
		logger.Error(fmt.Errorf("found multiple namespaces for identity %q", identity), "found multiple namespaces for identity")
		return nil, fmt.Errorf("found multiple namespaces for identity %q", identity)
	}
	var ns string
	if len(nss) == 1 {
		ns = nss[0].(*corev1.Namespace).Name
	} else {
		nsObj, err := kuberesources.CreateNamespace(ctx, m.kubeClient, fakeCluster, m.namespacePrefix, identity)
		if err != nil {
			return nil, err
		}
		logger.Info("Created namespace", "namespace", nsObj.Name)
		ns = nsObj.Name
	}
	logger = logger.WithValues("namespace", ns)
	ctx = klog.NewContext(ctx, logger)

	// first look for ClusterBinding to get old secret name
	kubeconfigSecretName := kuberesources.KubeconfigSecretName
	cb, err := m.bindClient.KubeBindV1alpha1().ClusterBindings(ns).Get(ctx, kuberesources.ClusterBindingName, metav1.GetOptions{})
	if err != nil && !errors.IsNotFound(err) {
		return nil, err
	} else if errors.IsNotFound(err) {
		if err := kuberesources.CreateClusterBinding(ctx, m.bindClient, ns, "kubeconfig", m.providerPrettyName); err != nil {
			return nil, err
		}
	} else {
		logger.V(3).Info("Found existing ClusterBinding")
		kubeconfigSecretName = cb.Spec.KubeconfigSecretRef.Name // reuse old name
	}

	sa, err := kuberesources.CreateServiceAccount(ctx, m.kubeClient, fakeCluster, ns, kuberesources.ServiceAccountName)
	if err != nil {
		return nil, err
	}

	saSecret, err := kuberesources.CreateSASecret(ctx, m.kubeClient, fakeCluster, ns, sa.Name)
	if err != nil {
		return nil, err
	}

	kfgSecret, err := kuberesources.GenerateKubeconfig(ctx, m.kubeClient, m.clusterConfig, fakeCluster, m.externalAddress, m.externalCA, m.externalTLSServerName, saSecret.Name, ns, kubeconfigSecretName)
	if err != nil {
		return nil, err
	}

	return kfgSecret.Data["kubeconfig"], nil
}
