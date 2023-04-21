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

package apireconciler

import (
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2/ktesting"

	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
)

func TestEnqueueAPIResourceSchema(t *testing.T) {
	c := &APIReconciler{
		queue: workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), ControllerName),
		listAPIExports: func(clusterName logicalcluster.Name) ([]*apisv1alpha1.APIExport, error) {
			return []*apisv1alpha1.APIExport{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "export1",
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "export2",
					},
				},
			}, nil
		},
	}

	schema := &apisv1alpha1.APIResourceSchema{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: "mycluster",
			},
			Name: "schema1",
		},
	}

	logger, _ := ktesting.NewTestContext(t)
	c.enqueueAPIResourceSchema(schema, logger)

	require.Equal(t, c.queue.Len(), 2)

	// get the queue keys
	actual := sets.New[string]()
	item, _ := c.queue.Get()
	actual.Insert(item.(string))
	item, _ = c.queue.Get()
	actual.Insert(item.(string))

	expected := sets.New[string]("export1", "export2")
	require.True(t, expected.Equal(actual))
}
