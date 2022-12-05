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

package limitranger

import (
	"context"
	"fmt"
	"io"
	"sync"

	kcpkubernetesclient "github.com/kcp-dev/client-go/kubernetes"
	kcpcorev1listers "github.com/kcp-dev/client-go/listers/core/v1"
	"github.com/kcp-dev/logicalcluster/v3"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/clientsethack"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/apiserver/pkg/informerfactoryhack"
	"k8s.io/client-go/informers"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/plugin/pkg/admission/limitranger"
)

const (
	// PluginName indicates name of admission plugin.
	PluginName = "WorkspaceLimitRanger"
)

// Register registers a plugin
func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(config io.Reader) (admission.Interface, error) {
		return &workspaceLimitRanger{
			Handler:   admission.NewHandler(admission.Create, admission.Update),
			lock:      sync.RWMutex{},
			delegates: map[logicalcluster.Name]*limitranger.LimitRanger{},
		}, nil
	})
}

// workspaceLimitRanger is a delegating multiplexer for the Kubernetes LimitRanger admission control plugin
type workspaceLimitRanger struct {
	*admission.Handler
	client kcpkubernetesclient.ClusterInterface
	lister kcpcorev1listers.LimitRangeClusterLister

	lock      sync.RWMutex
	delegates map[logicalcluster.Name]*limitranger.LimitRanger
}

// SetExternalKubeInformerFactory implements the WantsExternalKubeInformerFactory interface.
func (l *workspaceLimitRanger) SetExternalKubeInformerFactory(f informers.SharedInformerFactory) {
	l.lister = informerfactoryhack.Unwrap(f).Core().V1().LimitRanges().Lister()
	l.SetReadyFunc(informerfactoryhack.Unwrap(f).Core().V1().LimitRanges().Informer().HasSynced)
}

// SetExternalKubeClientSet implements the WantsExternalKubeClientSet interface.
func (l *workspaceLimitRanger) SetExternalKubeClientSet(client kubernetesclient.Interface) {
	l.client = clientsethack.Unwrap(client)
}

// ValidateInitialization implements the InitializationValidator interface.
func (l *workspaceLimitRanger) ValidateInitialization() error {
	if l.client == nil {
		return fmt.Errorf("missing client")
	}
	if l.lister == nil {
		return fmt.Errorf("missing lister")
	}
	return nil
}

// Admit admits resources into cluster that do not violate any defined LimitRange in the namespace
func (l *workspaceLimitRanger) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	delegate, err := l.delegateFor(clusterName)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	return delegate.Admit(ctx, a, o)
}

// Validate admits resources into cluster that do not violate any defined LimitRange in the namespace
func (l *workspaceLimitRanger) Validate(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	delegate, err := l.delegateFor(clusterName)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	return delegate.Validate(ctx, a, o)
}

func (l *workspaceLimitRanger) delegateFor(cluster logicalcluster.Name) (*limitranger.LimitRanger, error) {
	var delegate *limitranger.LimitRanger
	l.lock.RLock()
	var found bool
	delegate, found = l.delegates[cluster]
	l.lock.RUnlock()
	if found {
		return delegate, nil
	}

	l.lock.Lock()
	defer l.lock.Unlock()
	delegate, found = l.delegates[cluster]
	if found {
		return delegate, nil
	}

	var err error
	delegate, err = limitranger.NewLimitRanger(&limitranger.DefaultLimitRangerActions{})
	if err != nil {
		return nil, err
	}
	delegate.SetExternalKubeClientSet(l.client.Cluster(cluster))
	delegate.SetExternalKubeLister(l.lister.Cluster(cluster))
	return delegate, nil
}
