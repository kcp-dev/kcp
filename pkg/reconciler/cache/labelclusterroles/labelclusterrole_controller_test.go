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

package labelclusterroles

import (
	"testing"

	"github.com/stretchr/testify/require"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	kcpkubernetesinformers "github.com/kcp-dev/client-go/informers"
	kcpfakekubeclient "github.com/kcp-dev/client-go/kubernetes/fake"
	"github.com/kcp-dev/logicalcluster/v3"
)

func TestEnqueueClusterRoles(t *testing.T) {
	t.Parallel()

	factory := kcpkubernetesinformers.NewSharedInformerFactory(nil, 0)
	clusterRoleInformer := factory.Rbac().V1().ClusterRoles()
	c := NewController(
		"test",
		"test.kcp.io",
		func(logicalcluster.Name, *rbacv1.ClusterRole) bool { return false },
		func(logicalcluster.Name, *rbacv1.ClusterRoleBinding) bool { return false },
		kcpfakekubeclient.NewClientset(),
		clusterRoleInformer,
		factory.Rbac().V1().ClusterRoleBindings(),
	).(*controller)
	t.Cleanup(c.queue.ShutDown)

	for _, cr := range []struct{ cluster, name string }{
		{"one", "admin"},
		{"one", "view"},
		{"two", "admin"},
		{"three", "edit"},
	} {
		require.NoError(t, clusterRoleInformer.Informer().GetIndexer().Add(&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:        cr.name,
				Annotations: map[string]string{logicalcluster.AnnotationKey: cr.cluster},
			},
		}))
	}

	c.EnqueueClusterRoles("one", "reason", "test")

	queued := sets.New[string]()
	for c.queue.Len() > 0 {
		key, _ := c.queue.Get()
		queued.Insert(key)
		c.queue.Done(key)
	}
	require.Equal(t, sets.New(
		kcpcache.ToClusterAwareKey("one", "", "admin"),
		kcpcache.ToClusterAwareKey("one", "", "view"),
	), queued)
}
