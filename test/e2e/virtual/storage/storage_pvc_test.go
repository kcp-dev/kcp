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

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/syncer/storage"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPersistentVolumeClaimController(t *testing.T) {
	t.Parallel()
	framework.Suite(t, "transparent-multi-cluster")
	server := framework.SharedKcpServer(t)

	kubeClusterClient, err := kcpkubernetesclientset.NewForConfig(server.BaseConfig(t))
	require.NoError(t, err)

	var testCases = []struct {
		name string
		work func(t *testing.T, syncer *framework.StartedSyncerFixture, wsPath logicalcluster.Path, wsClusterName logicalcluster.Name)
	}{
		{
			name: "update delay sync annotationn on pending PVC",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, wsPath logicalcluster.Path, wsClusterName logicalcluster.Name) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				syncTargetKey := syncer.ToSyncTargetKey()
				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
					},
				}
				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pvc",
						Labels: map[string]string{
							workloadv1alpha1.InternalDownstreamClusterLabel: syncTargetKey,
						},
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "test-pv",
					},
				}

				logWithTimestampf(t, "Creating downstream test namespace %s...", ns.Name)
				_, err = syncer.DownstreamKubeClient.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestampf(t, "Creating downstream PVC %s...", pvc.Name)
				pvc, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(ns.Name).Create(ctx, pvc, metav1.CreateOptions{})
				require.NoError(t, err)
				require.Empty(t, pvc.Status)

				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					pvc, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, pvc.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}

					pvc.Status.Phase = corev1.ClaimPending
					_, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(ns.Name).UpdateStatus(ctx, pvc, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				err = retry.OnError(retry.DefaultBackoff, isError, func() error {
					logWithTimestampf(t, "Getting downstream PVC...")
					pvc, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(ns.Name).Get(ctx, pvc.Name, metav1.GetOptions{})
					require.NoError(t, err)
					require.Equal(t, corev1.ClaimPending, pvc.Status.Phase)

					logWithTimestampf(t, "Checking delay sync annotation has been applied...")
					if _, ok := pvc.GetAnnotations()[storage.DelayStatusSyncing]; !ok {
						return wait.ErrWaitTimeout
					}

					return nil
				})
				require.NoError(t, err)
			},
		},
		{
			name: "downstream pvc bound",
			work: func(t *testing.T, syncer *framework.StartedSyncerFixture, wsPath logicalcluster.Path, wsClusterName logicalcluster.Name) {
				ctx, cancelFunc := context.WithCancel(context.Background())
				t.Cleanup(cancelFunc)

				ns := &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-ns",
					},
				}
				syncTargetKey := syncer.ToSyncTargetKey()

				logWithTimestampf(t, "Creating upstream test namespace %s...", ns.Name)
				_, err = kubeClusterClient.CoreV1().Cluster(wsPath).Namespaces().Create(ctx, ns, metav1.CreateOptions{})
				require.NoError(t, err)

				downstreamPV := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
						Labels: map[string]string{
							workloadv1alpha1.InternalDownstreamClusterLabel: syncTargetKey,
						},
					},
					Spec: corev1.PersistentVolumeSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
						PersistentVolumeSource: corev1.PersistentVolumeSource{
							HostPath: &corev1.HostPathVolumeSource{
								Path: "/tmp/data",
							},
						},
					},
				}
				logWithTimestampf(t, "Create downstream PV...")
				downstreamPV, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumes().Create(ctx, downstreamPV, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for downstream PV to be created...")
				err = retry.OnError(retry.DefaultBackoff, errors.IsNotFound, func() error {
					downstreamPV, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumes().Get(ctx, downstreamPV.Name, metav1.GetOptions{})
					return err
				})

				logWithTimestampf(t, "Create upstream PVC")
				upstreamPVC := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pvc",
						Labels: map[string]string{
							workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: "Sync",
						},
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						VolumeName: "test-pv",
					},
				}
				upstreamPVC, err = kubeClusterClient.CoreV1().PersistentVolumeClaims().Cluster(wsPath).Namespace(ns.Name).Create(ctx, upstreamPVC, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for upstream PVC to be created...")
				err = retry.OnError(retry.DefaultBackoff, errors.IsNotFound, func() error {
					upstreamPVC, err = kubeClusterClient.CoreV1().PersistentVolumeClaims().Cluster(wsPath).Namespace(ns.Name).Get(ctx, upstreamPVC.Name, metav1.GetOptions{})
					return err
				})

				upstreamKcpClient, err := kcpclientset.NewForConfig(syncer.SyncerConfig.UpstreamConfig)
				require.NoError(t, err)

				syncTarget, err := upstreamKcpClient.Cluster(wsPath).WorkloadV1alpha1().SyncTargets().Get(ctx,
					syncer.SyncerConfig.SyncTargetName,
					metav1.GetOptions{},
				)
				require.NoError(t, err)

				desiredNSLocator := shared.NewNamespaceLocator(wsClusterName, logicalcluster.From(syncTarget),
					syncTarget.GetUID(), syncTarget.Name, ns.Name)
				downstreamNamespaceName, err := shared.PhysicalClusterNamespaceName(desiredNSLocator)
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for downstream PVC to be synced...")
				framework.Eventually(t, func() (success bool, reason string) {
					_, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).Get(ctx, upstreamPVC.Name, metav1.GetOptions{})
					if errors.IsNotFound(err) {
						return false, "Downstream PVC not synced"
					}
					require.NoError(t, err)
					return true, ""
				}, wait.ForeverTestTimeout, time.Millisecond*100, "Upstream PVC should have been synced")

				logWithTimestampf(t, "Manually update downstream PVC status...")
				var downstreamPVC *corev1.PersistentVolumeClaim
				err = retry.RetryOnConflict(retry.DefaultBackoff, func() error {
					downstreamPVC, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).Get(ctx, upstreamPVC.Name, metav1.GetOptions{})
					if err != nil && !errors.IsNotFound(err) {
						return err
					}

					downstreamPVC.Status.Phase = corev1.ClaimBound
					downstreamPVC, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).UpdateStatus(ctx, downstreamPVC, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)
				require.Equal(t, corev1.ClaimBound, downstreamPVC.Status.Phase)

				upstreamPVC, err = kubeClusterClient.CoreV1().PersistentVolumeClaims().Cluster(wsPath).Namespace(ns.Name).Get(ctx, upstreamPVC.Name, metav1.GetOptions{})
				require.NoError(t, err)

				logWithTimestampf(t, "Get PV through downstream client with updated ns locator annotation and claim ref from upstream PVC...")
				framework.Eventually(t, func() (success bool, reason string) {
					downstreamPV, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumes().Get(ctx, downstreamPV.Name, metav1.GetOptions{})
					require.NoError(t, err)
					return downstreamPV.GetAnnotations()[shared.NamespaceLocatorAnnotation] != "", "the NamespaceLocator annotation should have been set"
				}, wait.ForeverTestTimeout, time.Millisecond*100, "Check that downstream PV has been updated")
				require.JSONEq(t,
					fmt.Sprintf(`{"syncTarget":{"name":%q, "uid":%q, "cluster":%q}, "cluster":%q}`, syncer.SyncerConfig.SyncTargetName, syncer.SyncerConfig.SyncTargetUID, wsClusterName, wsClusterName),
					downstreamPV.GetAnnotations()[shared.NamespaceLocatorAnnotation])
				require.JSONEq(t,
					fmt.Sprintf(`[{"op":"replace","path":"/spec/claimRef","value":{"apiVersion":"v1","kind":"PersistentVolumeClaim","name":"test-pvc","namespace":"test-ns","resourceVersion":%q, "uid":%q}}]`, upstreamPVC.ResourceVersion, upstreamPVC.UID),
					downstreamPV.GetAnnotations()["internal.workload.kcp.dev/upsyncdiff"])
			},
		},
	}

	for i := range testCases {
		testCase := testCases[i]
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			framework.Suite(t, "transparent-multi-cluster")
			defer featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, features.SyncerStorage, true)()

			ctx, cancelFunc := context.WithCancel(context.Background())
			t.Cleanup(cancelFunc)

			orgPath, _ := framework.NewOrganizationFixture(t, server)

			wsPath, ws := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("pvc-controller"))
			wsClusterName := logicalcluster.Name(ws.Spec.Cluster)

			logWithTimestampf(t, "Deploying syncer into workspace %s", wsPath)
			syncer := framework.NewSyncerFixture(t, server, wsPath,
				framework.WithSyncTargetName("syncer"),
				framework.WithExtraResources("persistentvolumes", "persistentvolumeclaims"),
				framework.WithAPIExports(""),
				framework.WithDownstreamPreparation(func(config *rest.Config, isFakePCluster bool) {
					if !isFakePCluster {
						// Only need to install services,ingresses and persistentvolumes in a logical cluster
						return
					}
					sinkCrdClient, err := apiextensionsclientset.NewForConfig(config)
					require.NoError(t, err, "failed to create apiextensions client")
					logWithTimestampf(t, "Installing test CRDs into sink cluster...")
					kubefixtures.Create(t, sinkCrdClient.ApiextensionsV1().CustomResourceDefinitions(),
						metav1.GroupResource{Group: "core.k8s.io", Resource: "persistentvolumes"},
						metav1.GroupResource{Group: "core.k8s.io", Resource: "persistentvolumeclaims"},
					)
					require.NoError(t, err)
				}),
			).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)

			logWithTimestampf(t, "Bind syncer workspace")
			framework.NewBindCompute(t, wsPath, server,
				framework.WithAPIExportsWorkloadBindOption(wsPath.Join("kubernetes").String()),
			).Bind(t)

			logWithTimestampf(t, "Waiting for the persistentvolumes crd to be imported and available in the syncer source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.CoreV1().PersistentVolumes().Cluster(wsPath).List(ctx, metav1.ListOptions{})
				if err != nil {
					logWithTimestampf(t, "error seen waiting for persistentvolumes crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			logWithTimestampf(t, "Waiting for the persistentvolumeclaims crd to be imported and available in the syncer source cluster...")
			require.Eventually(t, func() bool {
				_, err := kubeClusterClient.CoreV1().PersistentVolumeClaims().Cluster(wsPath).Namespace("").List(ctx, metav1.ListOptions{})
				if err != nil {
					logWithTimestampf(t, "error seen waiting for persistentvolumeclaims crd to become active: %v", err)
					return false
				}
				return true
			}, wait.ForeverTestTimeout, time.Millisecond*100)

			logWithTimestampf(t, "Starting test...")
			testCase.work(t, syncer, wsPath, wsClusterName)
		})
	}
}

func isError(err error) bool { return err != nil }

func logWithTimestampf(t *testing.T, format string, args ...interface{}) {
	t.Helper()
	t.Logf("[%s] %s", time.Now().Format("15:04:05.000000"), fmt.Sprintf(format, args...))
}
