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

package workspacemounts

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"

	"github.com/kcp-dev/kcp/pkg/indexers"
	"github.com/kcp-dev/kcp/pkg/reconciler/dynamicrestmapper"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	tenancyv1alpha1informers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions/tenancy/v1alpha1"
)

// InstallIndexers adds the additional indexers that this controller requires to the informers.
func InstallIndexers(
	dynamicRESTMapper *dynamicrestmapper.DynamicRESTMapper,
	workspaceInformer tenancyv1alpha1informers.WorkspaceClusterInformer,
) {
	indexers.AddIfNotPresentOrDie(workspaceInformer.Informer().GetIndexer(), cache.Indexers{
		workspaceMountsReferenceIndex: newIndexWorkspaceByMountObject(dynamicRESTMapper),
	})
}

const workspaceMountsReferenceIndex = "WorkspacesByMountReference"

type workspaceMountsReferenceKey struct {
	ClusterName string `json:"clusterName"`
	Group       string `json:"group"`
	Resource    string `json:"resource"`
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
}

func newIndexWorkspaceByMountObject(dynamicRESTMapper *dynamicrestmapper.DynamicRESTMapper) cache.IndexFunc {
	return func(obj interface{}) ([]string, error) {
		ws, ok := obj.(*tenancyv1alpha1.Workspace)
		if !ok {
			return []string{}, fmt.Errorf("obj is supposed to be a Workspace, but is %T", obj)
		}

		if ws.Spec.Mount == nil {
			return nil, nil
		}

		gv, err := schema.ParseGroupVersion(ws.Spec.Mount.Reference.APIVersion)
		if err != nil {
			return nil, fmt.Errorf("unable to parse APIVersion of mount reference: %w", err)
		}
		gvk := schema.GroupVersionKind{
			Group:   gv.Group,
			Version: gv.Version,
			Kind:    ws.Spec.Mount.Reference.Kind,
		}

		forCluster := dynamicRESTMapper.ForCluster(logicalcluster.From(ws))
		gvr, err := forCluster.RESTMapping(gvk.GroupKind(), gvk.Version)
		if err != nil {
			return nil, fmt.Errorf("unable to get REST mapping for %s: %w", gvk, err)
		}

		key := workspaceMountsReferenceKey{
			ClusterName: logicalcluster.From(ws).String(),
			Resource:    gvr.Resource.Resource,
			Name:        ws.Spec.Mount.Reference.Name,
			Namespace:   ws.Spec.Mount.Reference.Namespace,
		}
		cs := strings.SplitN(ws.Spec.Mount.Reference.APIVersion, "/", 2)
		if len(cs) == 2 {
			key.Group = cs[0]
		}
		bs, err := json.Marshal(key)
		if err != nil {
			return nil, fmt.Errorf("unable to marshal mount reference: %w", err)
		}

		return []string{string(bs)}, nil
	}
}

func indexWorkspaceByMountObjectValue(gvr schema.GroupVersionResource, obj *unstructured.Unstructured) (string, error) {
	key := workspaceMountsReferenceKey{
		ClusterName: logicalcluster.From(obj).String(),
		Group:       gvr.Group,
		Resource:    gvr.Resource,
		Name:        obj.GetName(),
		Namespace:   obj.GetNamespace(),
	}
	bs, err := json.Marshal(key)
	if err != nil {
		return "", fmt.Errorf("unable to marshal mount reference: %w", err)
	}
	return string(bs), nil
}
