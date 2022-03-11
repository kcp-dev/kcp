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

package mutators

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestMutators(t *testing.T) {
	tdm := &TestDeploymentMutator{}

	deployment := appsv1.Deployment{
		TypeMeta: v1.TypeMeta{
			Kind:       "Deployment",
			APIVersion: "apps/v1",
		},
		ObjectMeta: v1.ObjectMeta{
			Name: "test-deployment",
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32),
		},
		Status: appsv1.DeploymentStatus{
			ObservedGeneration: int64(0),
		},
	}

	// Convert to unstructured.
	syncedObject := &unstructured.Unstructured{}
	err := convert(&deployment, syncedObject)
	require.NoError(t, err, "convert failed: %v", err)

	// Apply mutators to the object.
	err = tdm.ApplySpec(syncedObject)
	require.NoError(t, err, "ApplySpec() = %v", err)

	err = tdm.ApplyStatus(syncedObject)
	require.NoError(t, err, "ApplyStatus() = %v", err)

	// Convert the unstructured object back to a deployment.
	err = runtime.DefaultUnstructuredConverter.FromUnstructured(syncedObject.UnstructuredContent(), &deployment)
	require.NoError(t, err, "FromUnstructured() = %v", err)

	desiredReplicas := int32(10)
	if *deployment.Spec.Replicas != desiredReplicas {
		t.Errorf("deployment.Spec.Replicas = %v, want %v", *deployment.Spec.Replicas, desiredReplicas)
	}

	if deployment.Status.ObservedGeneration != 100 {
		t.Errorf("deployment.Status.ObservedGeneration = %v, want %v", deployment.Status.ObservedGeneration, 100)
	}
}

type TestDeploymentMutator struct {
}

func (tdm *TestDeploymentMutator) ApplySpec(downstreamObj *unstructured.Unstructured) error {
	// modify the spec of the downstream object, as it is a simple change, we don't need to
	// convert it to a deployment object.
	downstreamObj.Object["spec"] = map[string]interface{}{
		"replicas": 10,
	}
	return nil
}

func (tdm *TestDeploymentMutator) ApplyStatus(upstreamObj *unstructured.Unstructured) error {
	// modify the status of the upstream object, as it is a simple change, we don't need to
	// convert it to a deployment object.
	upstreamObj.Object["status"] = map[string]interface{}{
		"observedGeneration": 100,
	}
	return nil
}

func convert(from interface{}, to runtime.Object) error {
	bs, err := json.Marshal(from)
	if err != nil {
		return fmt.Errorf("Marshal() = %w", err)
	}
	if err := json.Unmarshal(bs, to); err != nil {
		return fmt.Errorf("Unmarshal() = %w", err)
	}
	return nil
}
