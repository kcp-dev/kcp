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

package plugin

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/core"
	schedulingv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/scheduling/v1alpha1"
	workloadv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/workload/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/cluster"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const (
	defaultRootDirectory = ".kcp"
	kindSubFolderName    = "kind"

	playgroundKubeconfigFileName = "playground.kubeconfig"
	kindAdminKubeconfigFileName  = "admin.kubeconfig"
)

var (
	scheme *runtime.Scheme
	codecs serializer.CodecFactory
)

func init() {
	scheme = runtime.NewScheme()
	_ = apisv1alpha1.AddToScheme(scheme)
	_ = apiextensionsv1.AddToScheme(scheme)

	codecs = serializer.NewCodecFactory(scheme)
}

type PlaygroundFixtureOption func(pf *PlaygroundFixture)

func NewPlaygroundFixture(t *testing.T, spec *PlaygroundSpec, opts ...PlaygroundFixtureOption) *PlaygroundFixture {
	t.Helper()

	if err := spec.Validate(field.NewPath("spec")); err != nil {
		t.Fatalf("failed to complet spec: %v", err)
	}

	pf := &PlaygroundFixture{
		Spec:          spec,
		rootDirectory: "",
		prettyPrint:   func(_ string, _ ...any) {}, // No-op pretty printer
	}
	for _, opt := range opts {
		opt(pf)
	}
	return pf
}

type PlaygroundFixture struct {
	Spec *PlaygroundSpec

	rootDirectory string
	prettyPrint   prettyPrintFunc
}

type prettyPrintFunc func(format string, args ...any)

func WithPlaygroundRootDirectory(rootDirectory string) PlaygroundFixtureOption {
	return func(pf *PlaygroundFixture) {
		pf.rootDirectory = rootDirectory
	}
}

func WithPrettyPrinter(f prettyPrintFunc) PlaygroundFixtureOption {
	return func(pf *PlaygroundFixture) {
		pf.prettyPrint = f
	}
}

func (pf *PlaygroundFixture) RootDirectory() string {
	return pf.rootDirectory
}

func (pf *PlaygroundFixture) ArtifactsDirectory() string {
	return filepath.Join(pf.rootDirectory, "artifacts")
}

func (pf *PlaygroundFixture) Start(t *testing.T) *StartedPlaygroundFixture {
	t.Helper()

	sp := newStartedPlaygroundFixture(pf)

	if sp.rootDirectory == "" {
		dir, err := framework.CreateTempDirForTest(t, defaultRootDirectory)
		if err != nil {
			t.Fatalf("failed to create a scratch folder: %v", err)
		}
		sp.rootDirectory = dir
	}

	// cleanup the root folder if existing (required for the plugin that always uses defaultRootDirectory)
	if err := os.RemoveAll(sp.rootDirectory); err != nil {
		t.Fatalf("failed to cleanup '%s': %v", sp.rootDirectory, err)
	}

	// create the root folder
	if err := os.MkdirAll(sp.rootDirectory, 0755); err != nil {
		t.Fatalf("failed to create '%s': %v", sp.rootDirectory, err)
	}

	// create a copy of the playground spec into the rootDirectory, so the playground do not depend anymore
	// on the file provided by the user.
	file := filepath.Join(sp.rootDirectory, defaultSpecPath)
	if err := sp.Spec.WriteToFile(file); err != nil {
		t.Fatalf("failed to write playground spec to '%s': %v", file, err)
	}

	// create shards, pClusters, and then initialise all the workspaces
	sp.createShards(t)
	sp.createPClusters(t)
	sp.initShards(t)

	// Set the initial context to use and write the playground config filed.
	sp.kubeConfig.CurrentContext = contextForShard(MainShardName)
	if err := clientcmd.WriteToFile(*sp.kubeConfig, sp.KubeConfigPath()); err != nil {
		t.Fatalf("failed to write playground config to '%s': %v", file, err)
	}

	return sp
}

type StartedPlaygroundFixture struct {
	*PlaygroundFixture

	specPath       string
	kubeConfigPath string
	kubeConfig     *clientcmdapi.Config

	Shards    map[string]framework.RunningServer
	PClusters map[string]RunningPCluster

	objs map[string][]runtime.Object

	// TODO: consider if to collect other objects we create while initializing workspaces
	//  (this could be useful if the fixture will be used into actual E2E tests in future).
}

type RunningPCluster interface {
	Name() string
	Type() PClusterType
	KubeconfigPath() string
	RawConfig() (clientcmdapi.Config, error)
	SyncerImage() string
}

func newStartedPlaygroundFixture(pf *PlaygroundFixture) *StartedPlaygroundFixture {
	return &StartedPlaygroundFixture{
		PlaygroundFixture: pf,
		specPath:          filepath.Join(pf.rootDirectory, defaultSpecPath),
		kubeConfigPath:    filepath.Join(pf.rootDirectory, playgroundKubeconfigFileName),
		kubeConfig:        clientcmdapi.NewConfig(),
		Shards:            map[string]framework.RunningServer{},
		PClusters:         map[string]RunningPCluster{},
		objs:              map[string][]runtime.Object{},
	}
}

// DetachedStartedPlaygroundFixture must be used only by `kcp playground use` to get access to
// specPath and kubeConfigPath.
func newDetachedStartedPlaygroundFixture(spec *PlaygroundSpec, opts ...PlaygroundFixtureOption) *StartedPlaygroundFixture {
	pf := &PlaygroundFixture{
		Spec: spec,
	}
	for _, opt := range opts {
		opt(pf)
	}
	return newStartedPlaygroundFixture(pf)
}

func (sp *StartedPlaygroundFixture) SpecPath() string {
	return sp.specPath
}

func (sp *StartedPlaygroundFixture) KubeConfigPath() string {
	return sp.kubeConfigPath
}

func (sp *StartedPlaygroundFixture) RawConfig() clientcmdapi.Config {
	return *sp.kubeConfig
}

func (sp *StartedPlaygroundFixture) createShards(t *testing.T) {
	t.Helper()

	for _, shard := range sp.Spec.Shards {
		sp.prettyPrint(" 🔹 Adding KCP shard '%s' 🏈...\n", shard.Name)

		runningShard := framework.PrivateKcpServer(t, framework.WithScratchDirectories(sp.ArtifactsDirectory(), sp.RootDirectory()))
		sp.Shards[shard.Name] = runningShard

		err := mergeFromShard(runningShard, sp.kubeConfig)
		require.NoError(t, err, "failed to merge kubeconfig for shard '%s'", shard.Name)
	}
}

func (sp *StartedPlaygroundFixture) createPClusters(t *testing.T) {
	t.Helper()

	for _, pCluster := range sp.Spec.PClusters {
		switch pCluster.Type {
		case KindPClusterType:
			sp.prettyPrint(" 🔸 Adding pCluster '%s' of type Kind 🏐...\n", pCluster.Name)

			kindDirectory := filepath.Join(sp.rootDirectory, kindSubFolderName, pCluster.Name)
			err := os.MkdirAll(kindDirectory, 0755)
			require.NoError(t, err, "failed to create '%s'", kindDirectory)

			runningKind := kindCluster(t, kindConfig{
				Name:           pCluster.Name,
				KubeConfigPath: filepath.Join(kindDirectory, kindAdminKubeconfigFileName),
			})
			sp.PClusters[pCluster.Name] = runningKind

			err = mergeFromKind(runningKind, sp.kubeConfig)
			require.NoError(t, err, "failed to merge kubeconfig for pcluster '%s'", kindDirectory)
		default:
			t.Fatalf("unknown type '%s' for pcluster '%s'", pCluster.Type, pCluster.Name)
		}

		sp.applyOthers(t, sp.PClusters[pCluster.Name].KubeconfigPath(), pCluster.Others, fmt.Sprintf(" 🔸 pCluster '%s'", pCluster.Name))
	}
}

func (sp *StartedPlaygroundFixture) initShards(t *testing.T) {
	t.Helper()

	for _, shard := range sp.Spec.Shards {
		shardServer, ok := sp.Shards[shard.Name]
		require.True(t, ok, "server for '%s' shard not found", shard.Name)

		sp.createWorkspaces(t, shardServer, logicalcluster.None, shard.Workspaces)

		if shard.DeploymentCoordinator {
			sp.prettyPrint(" 🔹 Shard '%s', adding deployment-coordinator 🏐...\n", shard.Name)
			deploymentCoordinator(t, shardServer)
		}
	}
}

func (sp *StartedPlaygroundFixture) createWorkspaces(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspaces []Workspace) {
	t.Helper()

	for _, workspace := range workspaces {
		wsPath := core.RootCluster.Path()
		if parent != logicalcluster.None {
			sp.prettyPrint(" 🔹 Shard '%s', adding workspace '%s:%s' 🏐...\n", server.Name(), parent.String(), workspace.Name)

			wsOptions := []framework.UnprivilegedWorkspaceOption{framework.WithName(workspace.Name)}
			if workspace.Type.String() != "" {
				wsOptions = append(wsOptions, framework.WithType(logicalcluster.NewPath(workspace.Type.Path), workspace.Type.Name))
			}
			wsPath, _ = framework.NewWorkspaceFixture(t, server, parent, wsOptions...)
		}

		sp.createAPIResourceSchemas(t, server, parent, workspace, wsPath)
		sp.createAPIExports(t, server, parent, workspace, wsPath)
		sp.createAPIBindings(t, server, parent, workspace, wsPath)

		sp.createSyncTargets(t, server, parent, workspace, wsPath)
		sp.createLocations(t, server, parent, workspace, wsPath)
		sp.createPlacementsAndBindCompute(t, server, parent, workspace, wsPath)

		sp.createOthers(t, server, parent, workspace, wsPath)

		sp.createWorkspaces(t, server, wsPath, workspace.Workspaces)
	}
}

func (sp *StartedPlaygroundFixture) createSyncTargets(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	t.Helper()

	for _, syncTarget := range workspace.SyncTargets {
		pCluster, ok := sp.PClusters[syncTarget.PCluster]
		require.True(t, ok, "SyncTarget '%s' is referencing '%s' PCluster which does not exist", syncTarget.Name, syncTarget.PCluster)

		switch pCluster.Type() {
		case KindPClusterType:
			if pCluster.SyncerImage() == "" {
				kind, ok := pCluster.(*runningKindCluster)
				require.True(t, ok, "pcluster '%s' isn't of the expected type", pCluster.Name())

				syncerPackage := "./cmd/syncer"
				sp.prettyPrint(" 🔸 pCluster '%s', build and publish syncer image 🎾...\n", kind.Name())
				image := koBuild(t, koConfig{
					Package:         syncerPackage,
					KindClusterName: kind.Name(),
				})
				kind.SetSyncerImage(image)
			}
		default:
			t.Fatalf("unknown type '%s' for pcluster '%s'", pCluster.Type(), pCluster.Name())
		}

		framework.TestConfig.SetSyncerImage(pCluster.SyncerImage())
		framework.TestConfig.SetPClusterKubeconfig(pCluster.KubeconfigPath())

		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', creating SyncTarget '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, syncTarget.Name)
		sp.prettyPrint(" 🔸 pCluster '%s', applying syncer 🎾...\n", pCluster.Name())

		opts := []framework.SyncerOption{framework.WithSyncTargetName(syncTarget.Name)}
		if syncTarget.Labels != nil {
			opts = append(opts, framework.WithSyncTargetLabels(syncTarget.Labels))
		}
		if syncTarget.Labels != nil {
			opts = append(opts, framework.WithSyncTargetLabels(syncTarget.Labels))
		}
		if len(syncTarget.APIExports) > 0 {
			exports := []string{}
			for _, apiExport := range syncTarget.APIExports {
				exports = append(exports, fmt.Sprintf("%s:%s", apiExport.Path, apiExport.Export))
			}
			sort.Strings(exports)
			opts = append(opts, framework.WithAPIExports(exports...))
		}
		if len(syncTarget.Resources) > 0 {
			opts = append(opts, framework.WithExtraResources(syncTarget.Resources...))
		}
		syncer := framework.NewSyncerFixture(t, server, wsPath, opts...).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)
		syncer.WaitForSyncTargetReady(context.TODO(), t)

		// TODO: store upsyncer and syncer kubeconfig, make them accessible to users
	}
}

func (sp *StartedPlaygroundFixture) createLocations(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	t.Helper()
	if len(workspace.Locations) == 0 {
		return
	}

	kcpClusterClient, err := newKCPClusterClient(server, "root")
	require.NoError(t, err, "failed to get client for '%s'", server.Name())

	for _, location := range workspace.Locations {
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', creating Location '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, location.Name)

		obj := &schedulingv1alpha1.Location{
			ObjectMeta: metav1.ObjectMeta{
				Name:   location.Name,
				Labels: location.Labels,
			},
			Spec: schedulingv1alpha1.LocationSpec{
				Resource: schedulingv1alpha1.GroupVersionResource{
					Group:    workloadv1alpha1.SchemeGroupVersion.Group,
					Version:  workloadv1alpha1.SchemeGroupVersion.Version,
					Resource: "synctargets",
				},
				InstanceSelector: location.InstanceSelector,
			},
		}

		_, err = kcpClusterClient.Cluster(wsPath).SchedulingV1alpha1().Locations().Create(context.TODO(), obj, metav1.CreateOptions{})
		require.NoError(t, err, "error creating location")
	}
}

func (sp *StartedPlaygroundFixture) createPlacementsAndBindCompute(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	t.Helper()

	for _, placement := range workspace.Placements {
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', add placement '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, placement.Name)
		framework.NewBindCompute(t, wsPath, server,
			framework.WithPlacementNameBindOption(placement.Name),
			framework.WithLocationWorkspaceWorkloadBindOption(logicalcluster.NewPath(placement.LocationWorkspace)),
			framework.WithLocationSelectorWorkloadBindOption(placement.LocationSelectors...),
		).Bind(t)
	}
}

func (sp *StartedPlaygroundFixture) createAPIResourceSchemas(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	t.Helper()
	if len(workspace.APIResourceSchemas) == 0 {
		return
	}

	kcpClusterClient, err := newKCPClusterClient(server, "root")
	require.NoError(t, err, "failed to get client for '%s'", server.Name())

	for _, apiResourceSchema := range workspace.APIResourceSchemas {
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', creating APIResourceSchema '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, apiResourceSchema.Name)

		if apiResourceSchema.Source.File != nil {
			prefix := apiResourceSchema.Source.Prefix
			if prefix == "" {
				prefix = fmt.Sprintf("v%s", time.Now().Format("20060102"))
				if info, ok := debug.ReadBuildInfo(); ok {
					for _, setting := range info.Settings {
						if setting.Key == "vcs.revision" {
							prefix += fmt.Sprintf("-%s", setting.Value[:8])
						}
					}
				}
			}

			content, err := apiResourceSchema.Source.File.Get()
			require.NoError(t, err, "failed to read '%s'", apiResourceSchema.Source.File.Path)
			require.NotNil(t, content, "content read from file must not be nik")

			contentObjs, err := contentToObjs(content)
			require.NoError(t, err, "failed to convert file content to kubernetes objects")

			apiResourceSchemaObjs := make([]runtime.Object, 0, len(contentObjs))
			for _, obj := range contentObjs {
				if obj.GetObjectKind().GroupVersionKind().GroupKind() == apisv1alpha1.SchemeGroupVersion.WithKind("APIResourceSchema").GroupKind() {
					apiResourceSchemaObj, ok := obj.(*apisv1alpha1.APIResourceSchema)
					require.True(t, ok, "object is not an APIResourceSchema")

					_, err := kcpClusterClient.Cluster(wsPath).ApisV1alpha1().APIResourceSchemas().Create(context.TODO(), apiResourceSchemaObj, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create APIResourceSchema")

					sp.prettyPrint("    - 'apiresourceschema.apis.kcp.io/%s'\n", apiResourceSchemaObj.Name)
					apiResourceSchemaObjs = append(apiResourceSchemaObjs, apiResourceSchemaObj)

					continue
				}
				if obj.GetObjectKind().GroupVersionKind().GroupKind() == apiextensionsv1.SchemeGroupVersion.WithKind("CustomResourceDefinition").GroupKind() {
					crdObj, ok := obj.(*apiextensionsv1.CustomResourceDefinition)
					require.True(t, ok, "object is not a CustomResourceDefinition")

					apiResourceSchemaObj, err := apisv1alpha1.CRDToAPIResourceSchema(crdObj, prefix)
					require.NoError(t, err, "failed to conver crd with name '%s' to APIResourceSchema", crdObj.Name)

					_, err = kcpClusterClient.Cluster(wsPath).ApisV1alpha1().APIResourceSchemas().Create(context.TODO(), apiResourceSchemaObj, metav1.CreateOptions{})
					require.NoError(t, err, "failed to create APIResourceSchema")

					sp.prettyPrint("    - 'apiresourceschema.apis.kcp.io/%s' (generated from '%s' CRD)\n", apiResourceSchemaObj.Name, crdObj.Name)
					apiResourceSchemaObjs = append(apiResourceSchemaObjs, apiResourceSchemaObj)
					continue
				}
			}

			sp.objs[fmt.Sprintf("%s:apiresourceschema/%s", wsPath.String(), apiResourceSchema.Name)] = apiResourceSchemaObjs
		}
	}
}

func (sp *StartedPlaygroundFixture) createAPIExports(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	t.Helper()
	if len(workspace.APIExports) == 0 {
		return
	}

	kcpClusterClient, err := newKCPClusterClient(server, "root")
	require.NoError(t, err, "failed to get client for '%s'", server.Name())

	for _, apiExport := range workspace.APIExports {
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', creating APIExport '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, apiExport.Name)

		obj := &apisv1alpha1.APIExport{
			ObjectMeta: metav1.ObjectMeta{
				Name: apiExport.Name,
			},
			Spec: apisv1alpha1.APIExportSpec{},
		}

		for _, apiResourceSchema := range apiExport.APIResourceSchemas {
			apiResourceSchemaObjs, ok := sp.objs[fmt.Sprintf("%s:apiresourceschema/%s", wsPath.String(), apiResourceSchema)]
			if ok {
				for _, o := range apiResourceSchemaObjs {
					apiResourceSchemaObj, ok := o.(*apisv1alpha1.APIResourceSchema)
					require.True(t, ok, "object is not an APIResourceSchema")

					obj.Spec.LatestResourceSchemas = append(obj.Spec.LatestResourceSchemas, apiResourceSchemaObj.Name)
				}
			} else {
				obj.Spec.LatestResourceSchemas = append(obj.Spec.LatestResourceSchemas, apiResourceSchema)
			}
		}

		_, err := kcpClusterClient.Cluster(wsPath).ApisV1alpha1().APIExports().Create(context.TODO(), obj, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create APIExport")

		// TODO: store APIExport kubeconfig, make it accessible to users
	}
}

func (sp *StartedPlaygroundFixture) createAPIBindings(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	t.Helper()
	if len(workspace.APIBindings) == 0 {
		return
	}

	kcpClusterClient, err := newKCPClusterClient(server, "root")
	require.NoError(t, err, "failed to get client for '%s'", server.Name())

	for _, apiBinding := range workspace.APIBindings {
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', creating APIBinding '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, apiBinding.Name)

		obj := &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: apiBinding.Name,
			},
			Spec: apisv1alpha1.APIBindingSpec{
				Reference: apisv1alpha1.BindingReference{
					Export: &apiBinding.APIExport,
				},
			},
		}

		_, err = kcpClusterClient.Cluster(wsPath).ApisV1alpha1().APIBindings().Create(context.TODO(), obj, metav1.CreateOptions{})
		require.NoError(t, err, "failed to create APIBinding")

		// wait for phase to be bound
		if obj.Status.Phase != apisv1alpha1.APIBindingPhaseBound {
			err := wait.PollImmediate(time.Millisecond*500, time.Second*15, func() (done bool, err error) {
				createdBinding, err := kcpClusterClient.Cluster(wsPath).ApisV1alpha1().APIBindings().Get(context.TODO(), obj.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				if createdBinding.Status.Phase == apisv1alpha1.APIBindingPhaseBound {
					return true, nil
				}
				return false, nil
			})
			require.NoError(t, err, "APIBinding '%s' failed to bound", obj.Name)
		}
	}
}

func (sp *StartedPlaygroundFixture) createOthers(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	if len(workspace.Others) == 0 {
		return
	}

	raw, err := server.RawConfig()
	require.NoError(t, err, "failed to get Raw config for shard")

	logicalRawConfig := framework.LogicalClusterRawConfig(raw, wsPath, "base")
	pathSafeClusterName := strings.ReplaceAll(wsPath.String(), ":", "_")
	kubeconfigPath := filepath.Join(sp.ArtifactsDirectory(), fmt.Sprintf("%s.%s.kubeconfig", server.Name(), pathSafeClusterName))
	err = clientcmd.WriteToFile(logicalRawConfig, kubeconfigPath)
	require.NoError(t, err, "failed to write kubeconfig file for '%s'", wsPath)

	sp.applyOthers(t, kubeconfigPath, workspace.Others, fmt.Sprintf(" 🔹 Shard '%s', workspace '%s:%s'", server.Name(), parent.String(), workspace.Name))
}

func (sp *StartedPlaygroundFixture) applyOthers(t *testing.T, kubeconfigPath string, resources []Other, prettyMessagePrefix string) {
	t.Helper()
	if len(resources) == 0 {
		return
	}

	for _, resource := range resources {
		sp.prettyPrint("%s, applying resource '%s' 🏐...\n", prettyMessagePrefix, resource.Name)

		content, err := resource.Source.Get()
		require.NoError(t, err, "failed to get resource")
		require.NotNil(t, content, "resource content must not be nil")

		framework.KubectlApply(t, kubeconfigPath, content)
	}
}

func contentToObjs(content []byte) ([]runtime.Object, error) {
	objs := make([]runtime.Object, 0)

	d := kubeyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(content)))
	for {
		doc, err := d.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}

		obj, _, err := codecs.UniversalDecoder(apisv1alpha1.SchemeGroupVersion, apiextensionsv1.SchemeGroupVersion).Decode(doc, nil, nil)
		if err != nil {
			// TODO: consider if to tolerated this error, so we will ignore objects of different GroupVersion instead of failing
			return nil, fmt.Errorf("failed to decode object from: %v", err)
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

func newKCPClusterClient(server framework.RunningServer, context string) (kcpclientset.ClusterInterface, error) {
	raw, err := server.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get config for '%s' shard", server.Name())
	}

	config := clientcmd.NewNonInteractiveClientConfig(raw, context, nil, nil)
	restConfig, err := config.ClientConfig()
	if err != nil {
		return nil, err
	}

	clusterConfig := rest.CopyConfig(restConfig)
	u, err := url.Parse(restConfig.Host)
	if err != nil {
		return nil, err
	}
	u.Path = ""
	clusterConfig.Host = u.String()
	clusterConfig.UserAgent = rest.DefaultKubernetesUserAgent()

	return kcpclientset.NewForConfig(clusterConfig)
}
