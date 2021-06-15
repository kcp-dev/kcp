package deployment

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clusterfake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	clusterinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func init() {
	randInt = func(int) int { return 0 }
}

func TestReconcilePod(t *testing.T) {
	for _, c := range []struct {
		desc string

		// the state of existing root, leafs and clusters, before reconciling.
		root     *corev1.Pod
		leafs    []runtime.Object
		clusters []runtime.Object

		// the state of root and leafs after reconciling.
		wantRoot  *corev1.Pod
		wantLeafs []corev1.Pod
	}{{
		desc: "no clusters, no leafs",
		root: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		leafs:    nil, // no leafs yet.
		clusters: nil, // no clusters.
		wantRoot: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
			Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{{
					Type:    corev1.PodReady,
					Status:  corev1.ConditionFalse,
					Reason:  "NoRegisteredClusters",
					Message: "kcp has no clusters registered to receive Pods",
				}},
			},
		},
		wantLeafs: nil, // no leafs to create.
	}, {
		desc: "no ready clusters, delete all leafs",
		// This case simulates what happens when the only cluster becomes unready.
		// It had previously been assigned a leaf, so that leaf should now be deleted.
		root: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		leafs: []runtime.Object{
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
			},
		},
		clusters: []runtime.Object{
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:    v1alpha1.ClusterConditionReady,
						Status:  corev1.ConditionFalse, // cluster is not ready.
						Reason:  "MismatchedTypes",
						Message: "CRD puller determined this cluster can't talk Pods",
					}},
				},
			},
		},
		wantRoot: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
			Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{{
					Type:    corev1.PodReady,
					Status:  corev1.ConditionFalse,
					Reason:  "NoRegisteredClusters",
					Message: "kcp has no clusters registered to receive Pods",
				}},
			},
		},
		wantLeafs: nil, // leaf was deleted.
	}, {
		desc: "one cluster, no leafs",
		root: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		leafs: nil, // no leafs yet.
		clusters: []runtime.Object{
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
		},
		wantRoot: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		wantLeafs: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "root"}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		}},
	}, {
		desc: "one cluster, leaf exists",
		// The assignment of the leaf to the only cluster is already stable, so no changes should be made.
		root: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		leafs: []runtime.Object{
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster",
						"kcp.dev/owned-by": "root",
					},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "root"}},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
			},
		},
		clusters: []runtime.Object{
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
		},
		wantRoot: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		wantLeafs: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "root"}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		}},
	}, {
		desc: "two clusters, one leaf",
		// This simulates when a single cluster with a leaf assigned is joined by a second cluster.
		// The existing leaf is stable, no new leafs should be needed.
		root: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		leafs: []runtime.Object{
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-1",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-1",
						"kcp.dev/owned-by": "root",
					},
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "root"}},
				},
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
			},
		},
		clusters: []runtime.Object{
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-1"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-2"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
		},
		wantRoot: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		wantLeafs: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-1",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-1",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "root"}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		}},
	}, {
		desc: "two clusters, no leafs",
		// This simulates when a new unassigned root joins with two candidate clusters available.
		// Reconciling will choose one at random (deterministic for testing) and put a leaf copy there.
		root: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		leafs: nil, // no leafs yet.
		clusters: []runtime.Object{
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-1"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-2"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
		},
		wantRoot: &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		},
		wantLeafs: []corev1.Pod{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-1",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-1",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "root"}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Image: "my-image"}}},
		}},
	}} {
		t.Run(c.desc, func(t *testing.T) {
			ctx := context.Background()

			kubeClient := fake.NewSimpleClientset(c.leafs...)
			clusterClient := clusterfake.NewSimpleClientset(c.clusters...)
			sif := informers.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod)
			csif := clusterinformers.NewSharedInformerFactoryWithOptions(clusterClient, resyncPeriod)
			ctrl := podReconciler{
				kubeClient:    kubeClient,
				lister:        sif.Core().V1().Pods().Lister(),
				clusterLister: csif.Cluster().V1alpha1().Clusters().Lister(),
			}
			for _, d := range c.leafs {
				sif.Core().V1().Pods().Informer().GetIndexer().Add(d)
			}
			for _, c := range c.clusters {
				csif.Cluster().V1alpha1().Clusters().Informer().GetIndexer().Add(c)
			}

			// Reconcile the root.
			if err := ctrl.reconcile(ctx, c.root); err != nil {
				t.Fatalf("applyLeafs: %v", err)
			}

			// Check that root state is expected.
			if d := cmp.Diff(c.wantRoot, c.root); d != "" {
				t.Errorf("Root diff: (-want,+got)\n%s", d)
			}
			// Check that leafs state is expected.
			ls, err := kubeClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatalf("Listing: %v", err)
			}
			if d := cmp.Diff(c.wantLeafs, ls.Items); d != "" {
				t.Errorf("Leafs diff: (-want,+got)\n%s", d)
			}
		})
	}
}
