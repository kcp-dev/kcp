package deployment

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	clusterfake "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/fake"
	clusterinformers "github.com/kcp-dev/kcp/pkg/client/informers/externalversions"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
)

func ptr(i int32) *int32 { return &i }

func TestReconcileDeployments(t *testing.T) {
	for _, c := range []struct {
		desc string

		// the state of existing deployments and clusters, before reconciling.
		root     *appsv1.Deployment
		leafs    []runtime.Object
		clusters []runtime.Object

		// the state of root and leaf deployments after reconciling.
		wantRoot  *appsv1.Deployment
		wantLeafs []appsv1.Deployment
	}{{
		desc: "no clusters, no leafs",
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		leafs:    nil, // no leafs yet.
		clusters: nil, // no clusters.
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{{
					Type:    appsv1.DeploymentProgressing,
					Status:  corev1.ConditionFalse,
					Reason:  "NoRegisteredClusters",
					Message: "kcp has no clusters registered to receive Deployments",
				}},
			},
		},
		wantLeafs: nil, // no leafs to create.
	}, {
		desc: "no ready clusters, delete all leafs",
		// This case simulates what happens when the only cluster becomes unready.
		// It had previously been assigned a leaf, so that leaf should now be deleted.
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		leafs: []runtime.Object{
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(10)},
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
						Message: "CRD puller determined this cluster can't talk deployments",
					}},
				},
			},
		},
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
			Status: appsv1.DeploymentStatus{
				Conditions: []appsv1.DeploymentCondition{{
					Type:    appsv1.DeploymentProgressing,
					Status:  corev1.ConditionFalse,
					Reason:  "NoRegisteredClusters",
					Message: "kcp has no clusters registered to receive Deployments",
				}},
			},
		},
		wantLeafs: nil, // leaf was deleted.
	}, {
		desc: "one cluster, no leafs",
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
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
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		wantLeafs: []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(10)},
		}},
	}, {
		desc: "one cluster, leaf exists",
		// The assignment of the leaf to the only cluster is already stable, so no changes should be made.
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		leafs: []runtime.Object{
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(10)},
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
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		wantLeafs: []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(10)},
		}},
	}, {
		desc: "two clusters, one leaf",
		// This simulates when a single cluster with a leaf assigned is joined by a second cluster.
		// The existing leaf should have its replicas updated, and another leaf is created.
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		leafs: []runtime.Object{
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-1",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-1",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(10)},
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
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		wantLeafs: []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-1",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-1",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(5)},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-2",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-2",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(5)},
		}},
	}, {
		desc: "two clusters, three leafs",
		// This simulates when a third cluster is deleted, and its replicas are rebalanced among two remaining clusters.
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		leafs: []runtime.Object{
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-1",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-1",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(4)},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-2",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-2",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(3)},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-3",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-3",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(3)},
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
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(10)},
		},
		wantLeafs: []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-1",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-1",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(5)},
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-2",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-2",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(5)},
		}},
	}, {
		desc: "three clusters, uneven rebalance",
		// This demonstrates uneven balancing of replicas across leafs.
		// A set of two leafs together carrying 11 replicas (6+5) is
		// split into 3 leafs carrying (4+4+3) replicas.
		root: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root"},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(11)},
		},
		leafs: []runtime.Object{
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-1",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-1",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(6)},
			},
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name: "root--cluster-2",
					Labels: map[string]string{
						"kcp.dev/cluster":  "cluster-2",
						"kcp.dev/owned-by": "root",
					},
				},
				Spec: appsv1.DeploymentSpec{Replicas: ptr(5)},
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
			&v1alpha1.Cluster{
				ObjectMeta: metav1.ObjectMeta{Name: "cluster-3"},
				Status: v1alpha1.ClusterStatus{
					Conditions: []v1alpha1.Condition{{
						Type:   v1alpha1.ClusterConditionReady,
						Status: corev1.ConditionTrue,
					}},
				},
			},
		},
		wantRoot: &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "root", Labels: map[string]string{}},
			Spec:       appsv1.DeploymentSpec{Replicas: ptr(11)},
		},
		wantLeafs: []appsv1.Deployment{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-1",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-1",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(4)}, // the first leaf gets an extra replica.
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-2",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-2",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(4)}, // the second leaf also gets an extra replica.
		}, {
			ObjectMeta: metav1.ObjectMeta{
				Name: "root--cluster-3",
				Labels: map[string]string{
					"kcp.dev/cluster":  "cluster-3",
					"kcp.dev/owned-by": "root",
				},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root"}},
			},
			Spec: appsv1.DeploymentSpec{Replicas: ptr(3)},
		}},
	}} {
		t.Run(c.desc, func(t *testing.T) {
			ctx := context.Background()

			kubeClient := fake.NewSimpleClientset(c.leafs...)
			clusterClient := clusterfake.NewSimpleClientset(c.clusters...)
			sif := informers.NewSharedInformerFactoryWithOptions(kubeClient, resyncPeriod)
			csif := clusterinformers.NewSharedInformerFactoryWithOptions(clusterClient, resyncPeriod)
			ctrl := Controller{
				kubeClient:    kubeClient,
				lister:        sif.Apps().V1().Deployments().Lister(),
				clusterLister: csif.Cluster().V1alpha1().Clusters().Lister(),
			}
			for _, d := range c.leafs {
				sif.Apps().V1().Deployments().Informer().GetIndexer().Add(d)
			}
			for _, c := range c.clusters {
				csif.Cluster().V1alpha1().Clusters().Informer().GetIndexer().Add(c)
			}

			// Reconcile the root deployment.
			if err := ctrl.reconcile(ctx, c.root); err != nil {
				t.Fatalf("applyLeafs: %V", err)
			}

			// Check that root state is expected.
			if d := cmp.Diff(c.wantRoot, c.root); d != "" {
				t.Errorf("Root diff: (-want,+got)\n%s", d)
			}
			// Check that leafs state is expected.
			ls, err := kubeClient.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Fatalf("Listing deployments: %v", err)
			}
			if d := cmp.Diff(c.wantLeafs, ls.Items); d != "" {
				t.Errorf("Leafs diff: (-want,+got)\n%s", d)
			}
		})
	}
}
