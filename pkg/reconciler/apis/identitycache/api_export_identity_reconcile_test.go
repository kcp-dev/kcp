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

package identitycache

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/logicalcluster/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	clientgotesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/indexers"
)

func TestReconcile(t *testing.T) {
	scenarios := []struct {
		name              string
		initialApiExports []runtime.Object
		initialConfigMap  []runtime.Object
		validateFunc      func(ts *testing.T, actions []clientgotesting.Action)
	}{
		{
			name: "scenario 1: happy path, cm doesn't exist",
			initialApiExports: []runtime.Object{
				newAPIExport("export-1"),
				newAPIExport("export-2"),
			},
			validateFunc: func(ts *testing.T, actions []clientgotesting.Action) {
				wasConfigValidated := false
				for _, action := range actions {
					if action.Matches("create", "configmaps") {
						createAction := action.(clientgotesting.CreateAction)
						configMap := createAction.GetObject().(*corev1.ConfigMap)

						requiredConfigMap := newEmptyRequiredConfigmap()
						requiredConfigMap.Data["export-1"] = "export-1-identity"
						requiredConfigMap.Data["export-2"] = "export-2-identity"

						// copy the annotations since the logicalcluster.AnnotationKey is added on the server side
						configMap.Annotations = requiredConfigMap.Annotations

						if !equality.Semantic.DeepEqual(configMap, requiredConfigMap) {
							t.Errorf("unexpected ConfigMap:\n%s", cmp.Diff(configMap, requiredConfigMap))
						}
						wasConfigValidated = true
						break
					}
				}
				if !wasConfigValidated {
					ts.Errorf("the config map wasn't created")
				}
			},
		},
		{
			name: "scenario 2: no-op cm exist",
			initialApiExports: []runtime.Object{
				newAPIExport("export-1"),
				newAPIExport("export-2"),
			},
			initialConfigMap: func() []runtime.Object {
				requiredConfigMap := newEmptyRequiredConfigmap()
				requiredConfigMap.Data["export-1"] = "export-1-identity"
				requiredConfigMap.Data["export-2"] = "export-2-identity"
				return []runtime.Object{requiredConfigMap}
			}(),
			validateFunc: func(ts *testing.T, actions []clientgotesting.Action) {
				if len(actions) != 0 {
					t.Fatal("didn't expect any changes to the configmap")
				}
			},
		},
		{
			name: "scenario 3: cm updated",
			initialApiExports: []runtime.Object{
				newAPIExport("export-1"),
				newAPIExport("export-2"),
			},
			initialConfigMap: func() []runtime.Object {
				requiredConfigMap := newEmptyRequiredConfigmap()
				requiredConfigMap.Data["export-1"] = "export-1-identity"
				return []runtime.Object{requiredConfigMap}
			}(),
			validateFunc: func(ts *testing.T, actions []clientgotesting.Action) {
				wasConfigValidated := false
				for _, action := range actions {
					if action.Matches("update", "configmaps") {
						createAction := action.(clientgotesting.CreateAction)
						configMap := createAction.GetObject().(*corev1.ConfigMap)

						requiredConfigMap := newEmptyRequiredConfigmap()
						requiredConfigMap.Data["export-1"] = "export-1-identity"
						requiredConfigMap.Data["export-2"] = "export-2-identity"

						// copy the annotations since the logicalcluster.AnnotationKey is added on the server side
						configMap.Annotations = requiredConfigMap.Annotations

						if !equality.Semantic.DeepEqual(configMap, requiredConfigMap) {
							t.Errorf("unexpected ConfigMap:\n%s", cmp.Diff(configMap, requiredConfigMap))
						}
						wasConfigValidated = true
						break
					}
				}
				if !wasConfigValidated {
					ts.Errorf("the config map wasn't updated")
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			target := &controller{}
			fakeKubeClient := fake.NewSimpleClientset(scenario.initialConfigMap...)
			target.kubeClient = fakeKubeClient
			target.remoteShardApiExportsIndexer = cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{indexers.ByLogicalCluster: indexers.IndexByLogicalCluster})
			for _, obj := range scenario.initialApiExports {
				if err := target.remoteShardApiExportsIndexer.Add(obj); err != nil {
					t.Error(err)
				}
			}
			configMapIndexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
			for _, obj := range scenario.initialConfigMap {
				if err := configMapIndexer.Add(obj); err != nil {
					t.Error(err)
				}
			}
			target.configMapLister = corelisters.NewConfigMapLister(configMapIndexer)

			if err := target.reconcile(context.TODO()); err != nil {
				t.Fatal(err)
			}
			if scenario.validateFunc != nil {
				scenario.validateFunc(t, fakeKubeClient.Actions())
			}
		})
	}
}

func newAPIExport(name string) *apisv1alpha1.APIExport {
	return &apisv1alpha1.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "root",
			},
			Name: name,
		},
		Status: apisv1alpha1.APIExportStatus{
			IdentityHash: fmt.Sprintf("%s-identity", name),
		},
	}
}

func newEmptyRequiredConfigmap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "system:shard",
			},
			Namespace: "default",
			Name:      ConfigMapName,
		},
		Data: map[string]string{},
	}
}
