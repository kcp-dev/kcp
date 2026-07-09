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

package objectcountlimit

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/authentication/user"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	corev1alpha1listers "github.com/kcp-dev/sdk/client/listers/core/v1alpha1"

	"github.com/kcp-dev/kcp/pkg/objectcount"
)

const testCluster = logicalcluster.Name("root:ws")

func newAttr(gvr schema.GroupVersionResource, name string, op admission.Operation, subresource string) admission.Attributes {
	return admission.NewAttributesRecord(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"}},
		nil,
		gvr.GroupVersion().WithKind("ConfigMap"),
		"default",
		name,
		gvr,
		subresource,
		op,
		nil,
		false,
		&user.DefaultInfo{Name: "user"},
	)
}

func configMapCreate(name string) admission.Attributes {
	return newAttr(corev1.SchemeGroupVersion.WithResource("configmaps"), name, admission.Create, "")
}

func newPlugin(registry *objectcount.Registry, logicalClusters ...*corev1alpha1.LogicalCluster) *objectCountLimit {
	return &objectCountLimit{
		Handler:              admission.NewHandler(admission.Create, admission.Delete),
		registry:             registry,
		logicalClusterLister: fakeLogicalClusterClusterLister(logicalClusters),
	}
}

func newLogicalCluster(phase corev1alpha1.LogicalClusterPhaseType, annotations map[string]string) *corev1alpha1.LogicalCluster {
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[logicalcluster.AnnotationKey] = string(testCluster)
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        corev1alpha1.LogicalClusterName,
			Annotations: annotations,
		},
		Status: corev1alpha1.LogicalClusterStatus{
			Phase: phase,
		},
	}
}

func ctxWithCluster(t *testing.T) context.Context {
	t.Helper()
	return genericapirequest.WithCluster(context.Background(), genericapirequest.Cluster{Name: testCluster})
}

func TestValidateInactiveFastPath(t *testing.T) {
	t.Parallel()

	registry := objectcount.NewRegistry(1)
	// enforcement not activated by the scanner

	p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseReady, nil))
	require.NoError(t, p.Validate(ctxWithCluster(t), configMapCreate("cm"), nil))
	require.Equal(t, int64(0), registry.Count(testCluster), "inactive plugin must not track deltas")
}

func TestValidateRejectsOverLimit(t *testing.T) {
	t.Parallel()

	registry := objectcount.NewRegistry(2)
	registry.SetEnforcementActive(true)

	p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseReady, nil))
	ctx := ctxWithCluster(t)

	require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
	require.NoError(t, p.Validate(ctx, configMapCreate("cm2"), nil))

	err := p.Validate(ctx, configMapCreate("cm3"), nil)
	require.Error(t, err)
	require.True(t, apierrors.IsForbidden(err))
	require.Contains(t, err.Error(), "total object count limit")
	require.Contains(t, err.Error(), "2/2")
}

func TestValidateDeleteAlwaysAllowedAndDecrements(t *testing.T) {
	t.Parallel()

	registry := objectcount.NewRegistry(2)
	registry.SetEnforcementActive(true)

	p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseReady, nil))
	ctx := ctxWithCluster(t)

	require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
	require.NoError(t, p.Validate(ctx, configMapCreate("cm2"), nil))
	require.Error(t, p.Validate(ctx, configMapCreate("cm3"), nil))

	del := newAttr(corev1.SchemeGroupVersion.WithResource("configmaps"), "cm1", admission.Delete, "")
	require.NoError(t, p.Validate(ctx, del, nil))

	require.NoError(t, p.Validate(ctx, configMapCreate("cm3"), nil), "delete must free up capacity")
}

func TestValidateSkips(t *testing.T) {
	t.Parallel()

	registry := objectcount.NewRegistry(1)
	registry.SetEnforcementActive(true)

	p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseReady, nil))
	ctx := ctxWithCluster(t)

	// fill up the quota
	require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
	require.Error(t, p.Validate(ctx, configMapCreate("cm2"), nil))

	tests := []struct {
		name string
		attr admission.Attributes
	}{
		{
			name: "subresource",
			attr: newAttr(corev1.SchemeGroupVersion.WithResource("configmaps"), "cm", admission.Create, "status"),
		},
		{
			name: "logicalclusters",
			attr: newAttr(corev1alpha1.SchemeGroupVersion.WithResource("logicalclusters"), "cluster", admission.Create, ""),
		},
		{
			name: "workspace-admin clusterrolebinding",
			attr: newAttr(schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, "workspace-admin", admission.Create, ""),
		},
		{
			name: "core events",
			attr: newAttr(corev1.SchemeGroupVersion.WithResource("events"), "ev", admission.Create, ""),
		},
		{
			name: "events.k8s.io events",
			attr: newAttr(schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}, "ev", admission.Create, ""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.NoError(t, p.Validate(ctx, tt.attr, nil))
		})
	}
}

func TestValidateAllowsWithoutLogicalCluster(t *testing.T) {
	t.Parallel()

	registry := objectcount.NewRegistry(1)
	registry.SetEnforcementActive(true)

	p := newPlugin(registry) // no LogicalCluster object
	ctx := ctxWithCluster(t)

	require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
	require.NoError(t, p.Validate(ctx, configMapCreate("cm2"), nil), "bootstrap writes must be allowed")
	require.Equal(t, int64(2), registry.Count(testCluster), "creates must still be tracked")
}

func TestValidateAllowsNonReadyLogicalCluster(t *testing.T) {
	t.Parallel()

	registry := objectcount.NewRegistry(1)
	registry.SetEnforcementActive(true)

	p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseInitializing, nil))
	ctx := ctxWithCluster(t)

	require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
	require.NoError(t, p.Validate(ctx, configMapCreate("cm2"), nil), "initialization writes must be allowed")
}

func TestValidateAnnotationOverride(t *testing.T) {
	t.Parallel()

	t.Run("raises the default", func(t *testing.T) {
		t.Parallel()

		registry := objectcount.NewRegistry(1)
		registry.SetEnforcementActive(true)

		p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseReady,
			map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "3"}))
		ctx := ctxWithCluster(t)

		require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
		require.NoError(t, p.Validate(ctx, configMapCreate("cm2"), nil))
		require.NoError(t, p.Validate(ctx, configMapCreate("cm3"), nil))
		require.Error(t, p.Validate(ctx, configMapCreate("cm4"), nil))
	})

	t.Run("zero opts out", func(t *testing.T) {
		t.Parallel()

		registry := objectcount.NewRegistry(1)
		registry.SetEnforcementActive(true)

		p := newPlugin(registry, newLogicalCluster(corev1alpha1.LogicalClusterPhaseReady,
			map[string]string{corev1alpha1.LogicalClusterMaxTotalObjectsAnnotationKey: "0"}))
		ctx := ctxWithCluster(t)

		require.NoError(t, p.Validate(ctx, configMapCreate("cm1"), nil))
		require.NoError(t, p.Validate(ctx, configMapCreate("cm2"), nil))
	})
}

func TestValidateInitialization(t *testing.T) {
	t.Parallel()

	p := &objectCountLimit{Handler: admission.NewHandler(admission.Create, admission.Delete)}
	require.Error(t, p.ValidateInitialization())

	p.registry = objectcount.NewRegistry(0)
	require.Error(t, p.ValidateInitialization())

	p.logicalClusterLister = fakeLogicalClusterClusterLister{}
	require.NoError(t, p.ValidateInitialization())
}

type fakeLogicalClusterClusterLister []*corev1alpha1.LogicalCluster

func (l fakeLogicalClusterClusterLister) List(_ labels.Selector) ([]*corev1alpha1.LogicalCluster, error) {
	return l, nil
}

func (l fakeLogicalClusterClusterLister) Cluster(cluster logicalcluster.Name) corev1alpha1listers.LogicalClusterLister {
	var perCluster []*corev1alpha1.LogicalCluster
	for _, lc := range l {
		if logicalcluster.From(lc) == cluster {
			perCluster = append(perCluster, lc)
		}
	}
	return fakeLogicalClusterLister(perCluster)
}

type fakeLogicalClusterLister []*corev1alpha1.LogicalCluster

func (l fakeLogicalClusterLister) List(_ labels.Selector) ([]*corev1alpha1.LogicalCluster, error) {
	return l, nil
}

func (l fakeLogicalClusterLister) Get(name string) (*corev1alpha1.LogicalCluster, error) {
	for _, lc := range l {
		if lc.Name == name {
			return lc, nil
		}
	}
	return nil, apierrors.NewNotFound(corev1alpha1.Resource("logicalclusters"), name)
}
