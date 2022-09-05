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

package framework

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/apimachinery/pkg/dynamic"
	"github.com/kcp-dev/logicalcluster/v2"
	"github.com/stretchr/testify/require"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/dynamic"
	kubernetesclient "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	kcpclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	workloadcliplugin "github.com/kcp-dev/kcp/pkg/cliplugins/workload/plugin"
	"github.com/kcp-dev/kcp/pkg/syncer"
	"github.com/kcp-dev/kcp/pkg/syncer/shared"
)

type SyncerOption func(t *testing.T, fs *syncerFixture)

func NewSyncerFixture(t *testing.T, server RunningServer, clusterName logicalcluster.Name, opts ...SyncerOption) *syncerFixture {
	sf := &syncerFixture{
		upstreamServer:        server,
		workspaceClusterName:  clusterName,
		syncTargetClusterName: clusterName,
		syncTargetName:        "psyncer-01",
	}
	for _, opt := range opts {
		opt(t, sf)
	}
	return sf
}

// syncerFixture configures a syncer fixture. Its `Start` method does the work of starting a syncer.
type syncerFixture struct {
	upstreamServer RunningServer

	workspaceClusterName logicalcluster.Name

	syncTargetClusterName logicalcluster.Name
	syncTargetName        string

	extraResourcesToSync []string
	prepareDownstream    func(config *rest.Config, isFakePCluster bool)
}

func WithSyncTarget(clusterName logicalcluster.Name, name string) SyncerOption {
	return func(t *testing.T, sf *syncerFixture) {
		sf.syncTargetClusterName = clusterName
		sf.syncTargetName = name
	}
}

func WithExtraResources(resources ...string) SyncerOption {
	return func(t *testing.T, sf *syncerFixture) {
		sf.extraResourcesToSync = append(sf.extraResourcesToSync, resources...)
	}
}

func WithDownstreamPreparation(prepare func(config *rest.Config, isFakePCluster bool)) SyncerOption {
	return func(t *testing.T, sf *syncerFixture) {
		sf.prepareDownstream = prepare
	}
}

// Start starts a new syncer against the given upstream kcp workspace. Whether the syncer run
// in-process or deployed on a pcluster will depend whether --pcluster-kubeconfig and
// --syncer-image are supplied to the test invocation.
func (sf *syncerFixture) Start(t *testing.T) *StartedSyncerFixture {
	// Write the upstream logical cluster config to disk for the workspace plugin
	upstreamRawConfig, err := sf.upstreamServer.RawConfig()
	require.NoError(t, err)
	_, kubeconfigPath := WriteLogicalClusterConfig(t, upstreamRawConfig, "base", sf.workspaceClusterName)

	useDeployedSyncer := len(TestConfig.PClusterKubeconfig()) > 0

	syncerImage := TestConfig.SyncerImage()
	if useDeployedSyncer {
		require.NotZero(t, len(syncerImage), "--syncer-image must be specified if testing with a deployed syncer")
	} else {
		// The image needs to be a non-empty string for the plugin command but the value doesn't matter if not deploying a syncer.
		syncerImage = "not-a-valid-image"
	}

	// Run the plugin command to enable the syncer and collect the resulting yaml
	t.Logf("Configuring workspace %s for syncing", sf.workspaceClusterName)
	pluginArgs := []string{
		"workload",
		"sync",
		sf.syncTargetName,
		"--syncer-image=" + syncerImage,
		"--output-file=-",
		"--qps=-1",
		"--feature-gates=" + fmt.Sprintf("%s", utilfeature.DefaultFeatureGate),
	}
	for _, resource := range sf.extraResourcesToSync {
		pluginArgs = append(pluginArgs, "--resources="+resource)
	}
	syncerYAML := RunKcpCliPlugin(t, kubeconfigPath, pluginArgs)

	var downstreamConfig *rest.Config
	var downstreamKubeconfigPath string
	if useDeployedSyncer {
		// The syncer will target the pcluster identified by `--pcluster-kubeconfig`.
		downstreamKubeconfigPath = TestConfig.PClusterKubeconfig()
		fs, err := os.Stat(downstreamKubeconfigPath)
		require.NoError(t, err)
		require.NotZero(t, fs.Size(), "%s points to an empty file", downstreamKubeconfigPath)
		rawConfig, err := clientcmd.LoadFromFile(downstreamKubeconfigPath)
		require.NoError(t, err, "failed to load pcluster kubeconfig")
		config := clientcmd.NewNonInteractiveClientConfig(*rawConfig, rawConfig.CurrentContext, nil, nil)
		downstreamConfig, err = config.ClientConfig()
		require.NoError(t, err)
	} else {
		// The syncer will target a logical cluster that is a peer to the current workspace. A
		// logical server provides as a lightweight approximation of a pcluster for tests that
		// don't need to validate running workloads or interaction with kube controllers.
		parentClusterName, ok := sf.workspaceClusterName.Parent()
		require.True(t, ok, "%s does not have a parent", sf.workspaceClusterName)
		downstreamServer := NewFakeWorkloadServer(t, sf.upstreamServer, parentClusterName)
		downstreamConfig = downstreamServer.BaseConfig(t)
		downstreamKubeconfigPath = downstreamServer.KubeconfigPath()
	}

	if sf.prepareDownstream != nil {
		// Attempt crd installation to ensure the downstream server has an api surface
		// compatible with the test.
		sf.prepareDownstream(downstreamConfig, !useDeployedSyncer)
	}

	// Apply the yaml output from the plugin to the downstream server
	KubectlApply(t, downstreamKubeconfigPath, syncerYAML)

	artifactDir, _, err := ScratchDirs(t)
	if err != nil {
		t.Errorf("failed to create temp dir for syncer artifacts: %v", err)
	}

	// collect both in deployed and in-process mode
	t.Cleanup(func() {
		ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
		defer cancelFn()

		t.Logf("Collecting imported resource info: %s", artifactDir)
		upstreamCfg := sf.upstreamServer.BaseConfig(t)

		gather := func(client dynamic.Interface, gvr schema.GroupVersionResource) {
			resourceClient := client.Resource(gvr)

			list, err := resourceClient.List(ctx, metav1.ListOptions{})
			if err != nil {
				// Don't fail the test
				t.Logf("Error gathering %s: %v", gvr, err)
				return
			}

			for i := range list.Items {
				item := list.Items[i]
				sf.upstreamServer.Artifact(t, func() (runtime.Object, error) {
					return &item, nil
				})
			}
		}

		upstreamClusterDynamic, err := kcpdynamic.NewClusterDynamicClientForConfig(upstreamCfg)
		require.NoError(t, err, "error creating upstream dynamic client")

		downstreamDynamic, err := dynamic.NewForConfig(downstreamConfig)
		require.NoError(t, err, "error creating downstream dynamic client")

		gather(upstreamClusterDynamic.Cluster(sf.workspaceClusterName), apiresourcev1alpha1.SchemeGroupVersion.WithResource("apiresourceimports"))
		gather(upstreamClusterDynamic.Cluster(sf.workspaceClusterName), apiresourcev1alpha1.SchemeGroupVersion.WithResource("negotiatedapiresources"))
		gather(upstreamClusterDynamic.Cluster(sf.workspaceClusterName), corev1.SchemeGroupVersion.WithResource("namespaces"))
		gather(downstreamDynamic, corev1.SchemeGroupVersion.WithResource("namespaces"))
		gather(upstreamClusterDynamic.Cluster(sf.workspaceClusterName), appsv1.SchemeGroupVersion.WithResource("deployments"))
		gather(downstreamDynamic, appsv1.SchemeGroupVersion.WithResource("deployments"))
	})

	// Extract the configuration for an in-process syncer from the resources that were
	// applied to the downstream server. This maximizes the parity between the
	// configuration of a deployed and in-process syncer.
	var syncerID string
	for _, doc := range strings.Split(string(syncerYAML), "\n---\n") {
		var manifest struct {
			metav1.ObjectMeta `json:"metadata"`
		}
		err := yaml.Unmarshal([]byte(doc), &manifest)
		require.NoError(t, err)
		if manifest.Namespace != "" {
			syncerID = manifest.Namespace
			break
		}
	}
	require.NotEmpty(t, syncerID, "failed to extract syncer namespace from yaml produced by plugin:\n%s", string(syncerYAML))

	syncerConfig := syncerConfigFromCluster(t, downstreamConfig, syncerID, syncerID)

	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	downstreamKubeClient, err := kubernetesclient.NewForConfig(downstreamConfig)
	require.NoError(t, err)

	if useDeployedSyncer {
		t.Cleanup(func() {
			ctx, cancelFn := context.WithDeadline(context.Background(), time.Now().Add(time.Second*30))
			defer cancelFn()

			// collect syncer logs
			t.Logf("Collecting syncer pod logs")
			func() {
				t.Logf("Listing downstream pods in namespace %s", syncerID)
				pods, err := downstreamKubeClient.CoreV1().Pods(syncerID).List(ctx, metav1.ListOptions{})
				if err != nil {
					t.Logf("failed to list pods in %s: %v", syncerID, err)
					return
				}

				for _, pod := range pods.Items {
					artifactPath := filepath.Join(artifactDir, fmt.Sprintf("syncer-%s-%s.log", syncerID, pod.Name))

					t.Logf("Collecting downstream logs for pod %s/%s: %s", syncerID, pod.Name, artifactPath)
					logs := Kubectl(t, downstreamKubeconfigPath, "-n", syncerID, "logs", pod.Name)

					err = ioutil.WriteFile(artifactPath, logs, 0644)
					if err != nil {
						t.Logf("failed to write logs for pod %s in %s to %s: %v", pod.Name, syncerID, artifactPath, err)
						continue // not fatal
					}
				}
			}()

			if preserveTestResources() {
				return
			}

			t.Logf("Deleting syncer resources for logical cluster %q, sync target %q", sf.workspaceClusterName, syncerConfig.SyncTargetName)
			err = downstreamKubeClient.CoreV1().Namespaces().Delete(ctx, syncerID, metav1.DeleteOptions{})
			if err != nil {
				t.Errorf("failed to delete Namespace %q: %v", syncerID, err)
			}
			err = downstreamKubeClient.RbacV1().ClusterRoleBindings().Delete(ctx, syncerID, metav1.DeleteOptions{})
			if err != nil {
				t.Errorf("failed to delete ClusterRoleBinding %q: %v", syncerID, err)
			}
			err = downstreamKubeClient.RbacV1().ClusterRoles().Delete(ctx, syncerID, metav1.DeleteOptions{})
			if err != nil {
				t.Errorf("failed to delete ClusterRole %q: %v", syncerID, err)
			}

			t.Logf("Deleting synced resources for logical cluster %s, sync target %s|%s", sf.workspaceClusterName, syncerConfig.SyncTargetWorkspace, syncerConfig.SyncTargetName)
			namespaces, err := downstreamKubeClient.CoreV1().Namespaces().List(ctx, metav1.ListOptions{})
			if err != nil {
				t.Errorf("failed to list namespaces: %v", err)
			}
			for _, ns := range namespaces.Items {
				locator, exists, err := shared.LocatorFromAnnotations(ns.Annotations)
				require.NoError(t, err, "failed to extract locator from namespace %s", ns.Name)
				if !exists {
					continue // Not a kcp-synced namespace
				}
				if locator.Workspace != sf.workspaceClusterName {
					continue // Not a namespace synced from this upstream workspace
				}
				if locator.SyncTarget.Workspace != syncerConfig.SyncTargetWorkspace.String() ||
					locator.SyncTarget.Name != syncerConfig.SyncTargetName {
					continue // Not a namespace synced by this syncer
				}
				if err = downstreamKubeClient.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err != nil {
					t.Logf("failed to delete Namespace %q: %v", ns.Name, err)
				}
			}
		})
	} else {
		// Start an in-process syncer
		err := syncer.StartSyncer(ctx, syncerConfig, 2, 5*time.Second)
		require.NoError(t, err, "syncer failed to start")
	}

	startedSyncer := &StartedSyncerFixture{
		SyncerConfig:         syncerConfig,
		SyncerID:             syncerID,
		DownstreamConfig:     downstreamConfig,
		DownstreamKubeClient: downstreamKubeClient,
	}

	// The sync target becoming ready indicates the syncer is healthy and has
	// successfully sent a heartbeat to kcp.
	startedSyncer.WaitForClusterReady(t, ctx)

	return startedSyncer
}

// StartedSyncerFixture contains the configuration used to start a syncer and interact with its
// downstream cluster.
type StartedSyncerFixture struct {
	SyncerConfig *syncer.SyncerConfig
	SyncerID     string
	// Provide cluster-admin config and client for test purposes. The downstream config in
	// SyncerConfig will be less privileged.
	DownstreamConfig     *rest.Config
	DownstreamKubeClient kubernetesclient.Interface
}

// WaitForClusterReady waits for the cluster to be ready with the given reason.
func (sf *StartedSyncerFixture) WaitForClusterReady(t *testing.T, ctx context.Context) {
	cfg := sf.SyncerConfig

	kcpClusterClient, err := kcpclient.NewForConfig(cfg.UpstreamConfig)
	require.NoError(t, err)
	EventuallyReady(t, func() (conditions.Getter, error) {
		return kcpClusterClient.WorkloadV1alpha1().SyncTargets().Get(logicalcluster.WithCluster(ctx, cfg.SyncTargetWorkspace), cfg.SyncTargetName, metav1.GetOptions{})
	}, "Waiting for cluster %q condition %q", cfg.SyncTargetName, conditionsv1alpha1.ReadyCondition)
	t.Logf("Cluster %q is %s", cfg.SyncTargetName, conditionsv1alpha1.ReadyCondition)
}

// syncerConfigFromCluster reads the configuration needed to start an in-process
// syncer from the resources applied to a cluster for a deployed syncer.
func syncerConfigFromCluster(t *testing.T, downstreamConfig *rest.Config, namespace, syncerID string) *syncer.SyncerConfig {
	ctx, cancelFunc := context.WithCancel(context.Background())
	t.Cleanup(cancelFunc)

	downstreamKubeClient, err := kubernetesclient.NewForConfig(downstreamConfig)
	require.NoError(t, err)

	// Read the upstream kubeconfig from the syncer secret
	secret, err := downstreamKubeClient.CoreV1().Secrets(namespace).Get(ctx, syncerID, metav1.GetOptions{})
	require.NoError(t, err)
	upstreamConfigBytes := secret.Data[workloadcliplugin.SyncerSecretConfigKey]
	require.NotEmpty(t, upstreamConfigBytes, "upstream config is required")
	upstreamConfig, err := clientcmd.RESTConfigFromKubeConfig(upstreamConfigBytes)
	require.NoError(t, err, "failed to load upstream config")

	// Read the arguments from the syncer deployment
	deployment, err := downstreamKubeClient.AppsV1().Deployments(namespace).Get(ctx, syncerID, metav1.GetOptions{})
	require.NoError(t, err)
	containers := deployment.Spec.Template.Spec.Containers
	require.NotEmpty(t, containers, "expected at least one container in syncer deployment")
	argMap, err := syncerArgsToMap(containers[0].Args)
	require.NoError(t, err)

	require.NotEmpty(t, argMap["--sync-target-name"], "--sync-target-name is required")
	syncTargetName := argMap["--sync-target-name"][0]
	require.NotEmpty(t, syncTargetName, "a value for --sync-target-name is required")

	require.NotEmpty(t, argMap["--from-cluster"], "--sync-target-name is required")
	fromCluster := argMap["--from-cluster"][0]
	require.NotEmpty(t, fromCluster, "a value for --from-cluster is required")
	kcpClusterName := logicalcluster.New(fromCluster)

	resourcesToSync := argMap["--resources"]
	require.NotEmpty(t, fromCluster, "--resources is required")

	syncTargetUID := argMap["--sync-target-uid"][0]

	// Read the downstream token from the deployment's service account secret
	var tokenSecret corev1.Secret
	Eventually(t, func() (bool, string) {
		secrets, err := downstreamKubeClient.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			t.Errorf("failed to list secrets: %v", err)
			return false, fmt.Sprintf("failed to list secrets downstream: %v", err)
		}
		for _, secret := range secrets.Items {
			t.Logf("checking secret %s/%s for annotation %s=%s", secret.Namespace, secret.Name, corev1.ServiceAccountNameKey, syncerID)
			if secret.Annotations[corev1.ServiceAccountNameKey] == syncerID {
				tokenSecret = secret
				return len(secret.Data["token"]) > 0, fmt.Sprintf("token secret %s/%s for service account %s found", namespace, secret.Name, syncerID)
			}
		}
		return false, fmt.Sprintf("token secret for service account %s/%s not found", namespace, syncerID)
	}, wait.ForeverTestTimeout, time.Millisecond*100, "token secret in namespace %q for syncer service account %q not found", namespace, syncerID)
	token := tokenSecret.Data["token"]
	require.NotEmpty(t, token, "token is required")

	// Compose a new downstream config that uses the token
	downstreamConfigWithToken := ConfigWithToken(string(token), rest.CopyConfig(downstreamConfig))
	return &syncer.SyncerConfig{
		UpstreamConfig:      upstreamConfig,
		DownstreamConfig:    downstreamConfigWithToken,
		ResourcesToSync:     sets.NewString(resourcesToSync...),
		SyncTargetWorkspace: kcpClusterName,
		SyncTargetName:      syncTargetName,
		SyncTargetUID:       syncTargetUID,
	}
}

// syncerArgsToMap converts the cli argument list from a syncer deployment into a map
// keyed by flags.
func syncerArgsToMap(args []string) (map[string][]string, error) {
	argMap := map[string][]string{}
	for _, arg := range args {
		argParts := strings.SplitN(arg, "=", 2)
		if len(argParts) != 2 {
			return nil, fmt.Errorf("arg %q isn't of the expected form `<key>=<value>`", arg)
		}
		key, value := argParts[0], argParts[1]
		if _, ok := argMap[key]; !ok {
			argMap[key] = []string{value}
		} else {
			argMap[key] = append(argMap[key], value)
		}
	}
	return argMap, nil
}
