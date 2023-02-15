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
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/kcp-dev/kcp/pkg/apis/core"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

const (
	defaultRootDirectory = ".kcp"
	kindSubFolderName    = "kind"

	playgroundKubeconfigFileName = "playground.kubeconfig"
	kindAdminKubeconfigFileName  = "admin.kubeconfig"
)

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

	// TODO: consider if to collect other objects we create while initializing workspaces
	//  (this could be useful if the fixture will be used into actual E2E tests in future).
}

// TODO: check  if we really need SetSyncerImage

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
	for _, shard := range sp.Spec.Shards {
		sp.prettyPrint(" 🔹 Adding KCP shard '%s' 🏈...\n", shard.Name)

		runningShard := framework.PrivateKcpServer(t, framework.WithScratchDirectories(path.Join(sp.rootDirectory, "artifacts"), sp.rootDirectory))

		sp.Shards[shard.Name] = runningShard
		if err := mergeFromShard(runningShard, sp.kubeConfig); err != nil {
			t.Fatalf("failed to merge kubeconfig for shard '%s': %v", shard.Name, err)
		}
	}
}

func (sp *StartedPlaygroundFixture) createPClusters(t *testing.T) {
	for _, pCluster := range sp.Spec.PClusters {
		switch pCluster.Type {
		case KindPClusterType:
			sp.prettyPrint(" 🔸 Adding pCluster '%s' of type Kind 🏐...\n", pCluster.Name)

			kindDirectory := filepath.Join(sp.rootDirectory, kindSubFolderName, pCluster.Name)
			if err := os.MkdirAll(kindDirectory, 0755); err != nil {
				t.Fatalf("failed to create '%s': %v", kindDirectory, err)
			}

			runningKind := kindCluster(t, kindConfig{
				Name:           pCluster.Name,
				KubeConfigPath: filepath.Join(kindDirectory, kindAdminKubeconfigFileName),
			})

			sp.PClusters[pCluster.Name] = runningKind
			if err := mergeFromKind(runningKind, sp.kubeConfig); err != nil {
				t.Fatalf("failed to merge kubeconfig for pcluster '%s': %v", pCluster.Name, err)
			}
		default:
			t.Fatalf("unknown type '%s' for pcluster '%s'", pCluster.Type, pCluster.Name)
		}
	}
}

func (sp *StartedPlaygroundFixture) initShards(t *testing.T) {
	for _, shard := range sp.Spec.Shards {
		shardServer, ok := sp.Shards[shard.Name]
		if !ok {
			t.Fatalf("server for '%s' shard not found", shard.Name)
		}
		sp.createWorkspaces(t, shardServer, core.RootCluster.Path(), shard.Workspaces)
	}
}

func (sp *StartedPlaygroundFixture) createWorkspaces(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspaces []Workspace) {
	for _, workspace := range workspaces {
		sp.prettyPrint(" 🔹 Shard '%s', adding workspace '%s:%s' 🏐...\n", server.Name(), parent.String(), workspace.Name)

		wsOptions := []framework.UnprivilegedWorkspaceOption{framework.WithName(workspace.Name)}
		if workspace.Type.String() != "" {
			wsOptions = append(wsOptions, framework.WithType(logicalcluster.NewPath(workspace.Type.Path), workspace.Type.Name))
		}
		wsPath, _ := framework.NewWorkspaceFixture(t, server, parent, wsOptions...)

		sp.createSyncTargets(t, server, parent, workspace, wsPath)

		sp.createPlacementsAndBindCompute(t, server, parent, workspace, wsPath)

		sp.createWorkspaces(t, server, wsPath, workspace.Workspaces)
	}
}

func (sp *StartedPlaygroundFixture) createSyncTargets(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	for _, syncTarget := range workspace.SyncTargets {
		pCluster, ok := sp.PClusters[syncTarget.PCluster]
		if !ok {
			t.Fatalf("synctarget '%s' is referencing '%s' pcluster which does not exist", syncTarget.Name, syncTarget.PCluster)
		}

		switch pCluster.Type() {
		case KindPClusterType:
			if pCluster.SyncerImage() == "" {
				kind, ok := pCluster.(*runningKindCluster)
				if !ok {
					t.Fatalf("pcluster '%s' isn't of the expected type", pCluster.Name())
				}

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

		sp.prettyPrint(" 🔸 pCluster '%s', install syncer 🎾...\n", pCluster.Name())
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', add synctarget '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, syncTarget.Name)
		_ = framework.NewSyncerFixture(t, server, wsPath, framework.WithSyncTargetName(syncTarget.Name)).CreateSyncTargetAndApplyToDownstream(t).StartSyncer(t)

		// TODO: store upsyncer and syncer kubeconfig, make them accessible by use
	}
}

func (sp *StartedPlaygroundFixture) createPlacementsAndBindCompute(t *testing.T, server framework.RunningServer, parent logicalcluster.Path, workspace Workspace, wsPath logicalcluster.Path) {
	for _, placement := range workspace.Placements {
		sp.prettyPrint(" 🔹 Shard '%s', workspace '%s:%s', add placement '%s' 🏐...\n", server.Name(), parent.String(), workspace.Name, placement.Name)
		framework.NewBindCompute(t, wsPath, server,
			framework.WithPlacementNameBindOption(placement.Name),
			framework.WithLocationWorkspaceWorkloadBindOption(logicalcluster.NewPath(placement.LocationWorkspace)),
		).Bind(t)
	}
}
