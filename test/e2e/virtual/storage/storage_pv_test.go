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
	"encoding/json"
	"testing"
	"time"

	kcpkubernetesclientset "github.com/kcp-dev/client-go/kubernetes"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apiextensionsclientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/features"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
	"github.com/kcp-dev/kcp/pkg/syncer/storage"
	kubefixtures "github.com/kcp-dev/kcp/test/e2e/fixtures/kube"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

func TestPersistentVolumeController(t *testing.T) {
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
			name: "process upstream PV",
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

				// Create Downstream PVC with a delayed status syncing annotation

				pvcNamespaceLocator := shared.NewNamespaceLocator(wsClusterName, syncer.SyncTargetClusterName, types.UID(syncer.SyncerConfig.SyncTargetUID), syncer.SyncerConfig.SyncTargetName, ns.Name)
				downstreamNamespaceName, err := shared.PhysicalClusterNamespaceName(pvcNamespaceLocator)
				require.NoError(t, err)
				pvcNamespaceLocatorAnnotation, err := json.Marshal(pvcNamespaceLocator)
				require.NoError(t, err)

				_, err = syncer.DownstreamKubeClient.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: downstreamNamespaceName,
						Annotations: map[string]string{
							shared.NamespaceLocatorAnnotation: string(pvcNamespaceLocatorAnnotation),
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)

				downstreamPVC := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: downstreamNamespaceName,
						Labels: map[string]string{
							workloadv1alpha1.InternalDownstreamClusterLabel:                  syncTargetKey,
							workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: "Sync",
						},
						Annotations: map[string]string{
							shared.NamespaceLocatorAnnotation: string(pvcNamespaceLocatorAnnotation),
							storage.DelayStatusSyncing:        "true",
						},
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{
							corev1.ReadWriteOnce,
						},
					},
				}

				downstreamPVC, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).Create(ctx, downstreamPVC, metav1.CreateOptions{})
				require.NoError(t, err)

				// Create DownstreamPV linked to the downstream PVC
				pvNamespaceLocator := shared.NewNamespaceLocator(wsClusterName, syncer.SyncTargetClusterName, types.UID(syncer.SyncerConfig.SyncTargetUID), syncer.SyncerConfig.SyncTargetName, ns.Name)

				pvNamespaceLocatorAnnotation, err := json.Marshal(pvNamespaceLocator)
				require.NoError(t, err)
				downstreamPV := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
						Labels: map[string]string{
							workloadv1alpha1.InternalDownstreamClusterLabel:                  syncTargetKey,
							workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: "Upsync",
						},
						Annotations: map[string]string{
							shared.NamespaceLocatorAnnotation: string(pvNamespaceLocatorAnnotation),
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
						ClaimRef: &corev1.ObjectReference{
							Kind:            downstreamPVC.Kind,
							Namespace:       downstreamPVC.Namespace,
							Name:            downstreamPVC.Name,
							UID:             downstreamPVC.UID,
							APIVersion:      downstreamPVC.APIVersion,
							ResourceVersion: downstreamPVC.ResourceVersion,
						},
					},
				}
				downstreamPV, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumes().Create(ctx, downstreamPV, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for downstreamPV to be created...")
				err = retry.OnError(retry.DefaultBackoff, errors.IsNotFound, func() error {
					downstreamPV, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumes().Get(ctx, downstreamPV.Name, metav1.GetOptions{})
					return err
				})
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for downstreamPVC PV to be created, and set status to Bound...")
				err = retry.OnError(retry.DefaultBackoff, errors.IsNotFound, func() error {
					downstreamPVC, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).Get(ctx, downstreamPVC.Name, metav1.GetOptions{})
					if err != nil {
						return err
					}
					downstreamPVC.Status = corev1.PersistentVolumeClaimStatus{
						Phase: corev1.ClaimBound,
					}
					_, err := syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).UpdateStatus(ctx, downstreamPVC, metav1.UpdateOptions{})
					return err
				})
				require.NoError(t, err)

				logWithTimestampf(t, "Create upstream PV")
				upstreamPV := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
						Labels: map[string]string{
							workloadv1alpha1.ClusterResourceStateLabelPrefix + syncTargetKey: "Upsync",
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
				upstreamPV, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(wsPath).Create(ctx, upstreamPV, metav1.CreateOptions{})
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for upstream PV to be created...")
				err = retry.OnError(retry.DefaultBackoff, errors.IsNotFound, func() error {
					upstreamPV, err = kubeClusterClient.CoreV1().PersistentVolumes().Cluster(wsPath).Get(ctx, upstreamPV.Name, metav1.GetOptions{})
					return err
				})
				require.NoError(t, err)

				logWithTimestampf(t, "Wait for downstream PVC to be synced...")
				framework.Eventually(t, func() (success bool, reason string) {
					downstreamPVC, err = syncer.DownstreamKubeClient.CoreV1().PersistentVolumeClaims(downstreamNamespaceName).Get(ctx, "test-pvc", metav1.GetOptions{})
					require.NoError(t, err)

					_, exists := downstreamPVC.Annotations[storage.DelayStatusSyncing]
					return !exists, "Delayed status syncing is still there"
				}, wait.ForeverTestTimeout, time.Millisecond*100, "Delayed status syncing annotation should have been removed")
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

			wsPath, ws := framework.NewWorkspaceFixture(t, server, orgPath, framework.WithName("pv-controller"))
			wsClusterName := logicalcluster.Name(ws.Spec.Cluster)

			logWithTimestampf(t, "Deploying syncer into workspace %s", wsPath)
			syncer := framework.NewSyncerFixture(t, server, wsPath,
				framework.WithSyncTargetName("synctarget"),
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
