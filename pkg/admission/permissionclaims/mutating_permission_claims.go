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

package permissionclaims

import (
	"context"
	"fmt"
	"io"

	"github.com/kcp-dev/logicalcluster"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1/permissionclaims"
	kcpinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
)

const (
	byWorkspaceIndex = "mutating-permission-byWorkspace"
	PluginName       = "apis.kcp.dev/PermissionClaims"
)

type mutatingPermissionClaims struct {
	*admission.Handler

	apiBindingsIndexer cache.Indexer
	apiExportIndexer   cache.Indexer
	bindingReady       func() bool
	exportReady        func() bool
}

var _ admission.MutationInterface = &mutatingPermissionClaims{}

func NewMutatingPermissionClaims() admission.MutationInterface {

	p := &mutatingPermissionClaims{}
	p.Handler = admission.NewHandler(admission.Create)
	p.SetReadyFunc(func() bool {
		if p.bindingReady() && p.exportReady() {
			return true
		}
		return false
	})
	return p
}

func (m *mutatingPermissionClaims) Admit(ctx context.Context, a admission.Attributes, o admission.ObjectInterfaces) error {

	lcluster, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return err
	}

	bindings, err := m.apiBindingsIndexer.ByIndex(byWorkspaceIndex, lcluster.String())
	if err != nil {
		return err
	}

	for _, b := range bindings {
		binding, ok := b.(*apisv1alpha1.APIBinding)
		if !ok {
			return fmt.Errorf("expected a certain type")
		}
		for _, pc := range binding.Status.ObservedAcceptedPermissionClaims {
			if pc.Group == a.GetKind().Group && pc.Resource == a.GetResource().Resource {
				key, label, err := permissionclaims.PermissionClaimToLabel(pc)
				if err != nil {
					return err
				}
				u, ok := a.GetObject().(metav1.Object)
				if !ok {
					return fmt.Errorf("expected type")
				}
				l := u.GetLabels()
				if l == nil {
					l = map[string]string{}
				}
				l[key] = label
				u.SetLabels(l)
			}
		}
	}

	return nil
}

// SetKcpInformers implements the WantsExternalKcpInformerFactory interface.
func (m *mutatingPermissionClaims) SetKcpInformers(f kcpinformers.SharedInformerFactory) {
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
	if _, found := f.Apis().V1alpha1().APIExports().Informer().GetIndexer().GetIndexers()[byWorkspaceIndex]; !found {
		if err := f.Apis().V1alpha1().APIExports().Informer().AddIndexers(cache.Indexers{
			byWorkspaceIndex: func(obj interface{}) ([]string, error) {
				return []string{logicalcluster.From(obj.(metav1.Object)).String()}, nil
			},
		}); err != nil {
			// nothing we can do here. But this should also never happen. We check for existence before.
			klog.Errorf("failed to add indexer for APIBindings: %v", err)
		}
	}
	m.apiBindingsIndexer = f.Apis().V1alpha1().APIBindings().Informer().GetIndexer()
	m.apiExportIndexer = f.Apis().V1alpha1().APIExports().Informer().GetIndexer()
	m.bindingReady = f.Apis().V1alpha1().APIBindings().Informer().HasSynced
	m.exportReady = f.Apis().V1alpha1().APIExports().Informer().HasSynced
}

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName, func(configFile io.Reader) (admission.Interface, error) {
		return NewMutatingPermissionClaims(), nil
	})
}
