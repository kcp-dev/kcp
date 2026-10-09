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

package clusterannotation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/admission"

	"github.com/kcp-dev/logicalcluster/v3"
)

const clusterName = logicalcluster.Name("test-cluster")

func TestSetClusterAnnotation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   *corev1.ConfigMap
	}{
		{
			name: "no annotations",
			in: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{},
			},
		},
		{
			name: "with annotations",
			in: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"foo": "bar",
					},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			origAnnotations := test.in.GetAnnotations()

			undo := setClusterAnnotation(test.in, clusterName)
			assert.NotNil(t, undo)

			require.NotNil(t, test.in.GetAnnotations())
			require.Contains(t, test.in.GetAnnotations(), logicalcluster.AnnotationKey)
			assert.Equal(t, test.in.GetAnnotations()[logicalcluster.AnnotationKey], clusterName.String())

			// simulate external modification
			test.in.Annotations["bar"] = "foo"
			if origAnnotations == nil {
				origAnnotations = map[string]string{}
			}
			origAnnotations["bar"] = "foo"

			undo()

			assert.NotContains(t, test.in.GetAnnotations(), logicalcluster.AnnotationKey)
			assert.Equal(t, origAnnotations, test.in.GetAnnotations())
		})
	}
}

func TestSetClusterAnnotation_AlreadySet(t *testing.T) {
	t.Parallel()
	in := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: clusterName.String(),
			},
		},
	}
	origAnnotations := in.GetAnnotations()
	assert.Nil(t, setClusterAnnotation(in, clusterName))
	assert.Equal(t, origAnnotations, in.GetAnnotations())
}

// newAttributes builds admission attributes for the given operation carrying a
// ConfigMap that already has a (possibly forged) kcp.io/cluster annotation.
func newAttributes(op admission.Operation, forged string) admission.Attributes {
	obj := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "cm",
			Namespace: "default",
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: forged,
			},
		},
	}
	return admission.NewAttributesRecord(
		obj, nil,
		corev1.SchemeGroupVersion.WithKind("ConfigMap"),
		"default", "cm",
		corev1.SchemeGroupVersion.WithResource("configmaps"),
		"", op, nil, false, nil,
	)
}

func TestStamp(t *testing.T) {
	t.Parallel()
	const real = logicalcluster.Name("real-cluster")
	const forged = "forged-cluster"

	tests := []struct {
		name      string
		op        admission.Operation
		wantStamp bool // whether the hook/policy should observe the real value
	}{
		{name: "create forces real value", op: admission.Create, wantStamp: true},
		{name: "update forces real value", op: admission.Update, wantStamp: true},
		{name: "delete left untouched", op: admission.Delete, wantStamp: false},
		{name: "connect left untouched", op: admission.Connect, wantStamp: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			attr := newAttributes(test.op, forged)
			obj := attr.GetObject().(metav1.Object)

			undo := Stamp(attr, real)

			if !test.wantStamp {
				assert.Nil(t, undo, "Stamp must not touch non create/update operations")
				assert.Equal(t, forged, obj.GetAnnotations()[logicalcluster.AnnotationKey])
				return
			}

			require.NotNil(t, undo, "expected Stamp to return an undo func")
			assert.Equal(t, real.String(), obj.GetAnnotations()[logicalcluster.AnnotationKey],
				"hook must observe the authoritative cluster, not the forged value")

			undo()
			assert.NotEqual(t, forged, obj.GetAnnotations()[logicalcluster.AnnotationKey],
				"forged value must not survive admission")
		})
	}
}
