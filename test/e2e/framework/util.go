/*
Copyright 2021 The KCP Authors.

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
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	cacheddiscovery "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

// Persistent mapping of test name to base temp dir used to ensure
// artifact paths have a common root across servers for a given test.
var baseTempDirs map[string]string = map[string]string{}
var baseTempDirsLock sync.Mutex = sync.Mutex{}

// ensureBaseTempDir returns the name of a base temp dir for the
// current test, creating it if needed.
func ensureBaseTempDir(t *testing.T) (string, error) {
	baseTempDirsLock.Lock()
	defer baseTempDirsLock.Unlock()
	name := t.Name()
	if _, ok := baseTempDirs[name]; !ok {
		var baseDir string
		if dir, set := os.LookupEnv("ARTIFACT_DIR"); set {
			baseDir = dir
		} else {
			baseDir = t.TempDir()
		}
		baseDir = filepath.Join(baseDir, strings.NewReplacer("\\", "_", ":", "_").Replace(t.Name()))
		if err := os.MkdirAll(baseDir, 0755); err != nil {
			return "", fmt.Errorf("could not create base dir: %w", err)
		}
		baseTempDir, err := os.MkdirTemp(baseDir, "")
		if err != nil {
			return "", fmt.Errorf("could not create base temp dir: %w", err)
		}
		baseTempDirs[name] = baseTempDir
		t.Logf("Saving test artifacts for test %q under %q.", name, baseTempDir)

		// Remove the path from the cache after test completion to
		// ensure subsequent invocations of the test (e.g. due to
		// -count=<val> for val > 1) don't reuse the same path.
		t.Cleanup(func() {
			baseTempDirsLock.Lock()
			defer baseTempDirsLock.Unlock()
			delete(baseTempDirs, name)
		})
	}
	return baseTempDirs[name], nil
}

// createTempDirForTest creates the named directory with a unique base
// path derived from the name of the current test.
func CreateTempDirForTest(t *testing.T, dirName string) (string, error) {
	baseTempDir, err := ensureBaseTempDir(t)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(baseTempDir, dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("could not create subdir: %w", err)
	}
	return dir, nil
}

// ScratchDirs determines where artifacts and data should live for a test server.
func ScratchDirs(t *testing.T) (string, string, error) {
	artifactDir, err := CreateTempDirForTest(t, "artifacts")
	if err != nil {
		return "", "", err
	}
	dataDir, err := CreateTempDirForTest(t, "data")
	if err != nil {
		return "", "", err
	}
	return artifactDir, dataDir, nil
}

var localSchemeBuilder = runtime.SchemeBuilder{
	apiresourcev1alpha1.AddToScheme,
	tenancyv1alpha1.AddToScheme,
	clusterv1alpha1.AddToScheme,
}

func init() {
	utilruntime.Must(localSchemeBuilder.AddToScheme(scheme.Scheme))
}

// Artifact registers the data-producing function to run and dump the YAML-formatted output
// to the artifact directory for the test before the kcp process is terminated.
func (c *kcpServer) Artifact(t *testing.T, producer func() (runtime.Object, error)) {
	subDir := filepath.Join("artifacts", "kcp", c.name)
	artifactDir, err := CreateTempDirForTest(t, subDir)
	require.NoError(t, err, "could not create artifacts dir")
	// Using t.Cleanup ensures that artifact collection is local to
	// the test requesting retention regardless of server's scope.
	t.Cleanup(func() {
		data, err := producer()
		require.NoError(t, err, "error fetching artifact")

		accessor, ok := data.(metav1.Object)
		require.True(t, ok, "artifact has no object meta: %#v", data)

		dir := path.Join(artifactDir, accessor.GetClusterName())
		if accessor.GetNamespace() != "" {
			dir = path.Join(dir, accessor.GetNamespace())
		}
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err, "could not create dir")

		gvks, _, err := scheme.Scheme.ObjectKinds(data)
		require.NoError(t, err, "error finding gvk for artifact")
		require.NotEmpty(t, gvks, "found no gvk for artifact: %T", data)
		gvk := gvks[0]
		data.GetObjectKind().SetGroupVersionKind(gvk)

		cfg, err := c.Config("system:admin") // TODO(sttts): this doesn't make sense: discovery from a random workspace
		require.NoError(t, err, "could not get config for server %q", c.name)

		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		require.NoError(t, err, "could not get discovery client for server")

		scopedDiscoveryClient := discoveryClient.WithCluster(accessor.GetClusterName())
		mapper := restmapper.NewDeferredDiscoveryRESTMapper(cacheddiscovery.NewMemCacheClient(scopedDiscoveryClient))
		mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
		require.NoError(t, err, "could not get REST mapping for artifact's GVK")

		file := path.Join(dir, fmt.Sprintf("%s_%s.yaml", mapping.Resource.GroupResource().String(), accessor.GetName()))
		t.Logf("saving artifact to %s", file)

		serializer := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, json.SerializerOptions{Yaml: true})
		raw := bytes.Buffer{}
		err = serializer.Encode(data, &raw)
		require.NoError(t, err, "error marshalling artifact")

		err = ioutil.WriteFile(file, raw.Bytes(), 0644)
		require.NoError(t, err, "error writing artifact")
	})
}

// GetFreePort asks the kernel for a free open port that is ready to use.
func GetFreePort(t *testing.T) (string, error) {
	for {
		addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
		if err != nil {
			return "", fmt.Errorf("could not resolve free port: %w", err)
		}

		l, err := net.ListenTCP("tcp", addr)
		if err != nil {
			return "", fmt.Errorf("could not listen on free port: %w", err)
		}
		defer func(c io.Closer) {
			if err := c.Close(); err != nil {
				t.Errorf("could not close listener: %v", err)
			}
		}(l)
		port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
		// Tests run in -parallel will run in separate processes, so we must use the file-system
		// for sharing state and locking across them to coordinate who gets which port. Without
		// some mechanism for sharing state, the following race is possible:
		// - process A calls net.ListenTCP() to resolve a new port
		// - process A calls l.Close() to close the listener to allow accessory to use it
		// - process B calls net.ListenTCP() and resolves the same port
		// - process A attempts to use the port, fails as it is in use
		// Therefore, holding the listener open is our kernel-based lock for this process, and while
		// we hold it open we must record our intent to disk.
		lockDir := filepath.Join(os.TempDir(), "kcp-e2e-ports")
		if err := os.MkdirAll(lockDir, 0755); err != nil {
			return "", fmt.Errorf("could not create port lockfile dir: %w", err)
		}
		lockFile := filepath.Join(lockDir, port)
		if _, err := os.Stat(lockFile); os.IsNotExist(err) {
			// we've never seen this before, we can use it
			f, err := os.Create(lockFile)
			if err != nil {
				return "", fmt.Errorf("could not record port lockfile: %w", err)
			}
			if err := f.Close(); err != nil {
				return "", fmt.Errorf("could not close port lockfile: %w", err)
			}
			// the lifecycle of an accessory (and thereby its ports) is the test lifecycle
			t.Cleanup(func() {
				if err := os.Remove(lockFile); err != nil {
					t.Errorf("failed to remove port lockfile: %v", err)
				}
			})
			return port, nil
		} else if err != nil {
			return "", fmt.Errorf("could not access port lockfile: %w", err)
		}
		t.Logf("found a previously-seen port, retrying: %s", port)
	}
}

type ArtifactFunc func(*testing.T, func() (runtime.Object, error))

// CreateClusterAndWait creates a new Cluster resource with the desired name on a given server and waits for it to be ready.
func CreateClusterAndWait(t *testing.T, ctx context.Context, artifacts ArtifactFunc, kcpClient kcpclientset.Interface, pcluster RunningServer) (*clusterv1alpha1.Cluster, error) {
	config, err := pcluster.RawConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get server config: %w", err)
	}
	pclusterConfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"pcluster": config.Clusters["system:admin"],
		},
		Contexts: map[string]*clientcmdapi.Context{
			"pcluster": {
				Cluster:  "pcluster",
				AuthInfo: "admin",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"admin": config.AuthInfos["admin"],
		},
		CurrentContext: "pcluster",
	}

	bs, err := clientcmd.Write(pclusterConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize server config: %w", err)
	}

	cluster, err := kcpClient.ClusterV1alpha1().Clusters().Create(ctx, &clusterv1alpha1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: pcluster.Name()},
		Spec:       clusterv1alpha1.ClusterSpec{KubeConfig: string(bs)},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create cluster: %w", err)
	}
	artifacts(t, func() (runtime.Object, error) {
		return kcpClient.ClusterV1alpha1().Clusters().Get(ctx, cluster.Name, metav1.GetOptions{})
	})

	if err := wait.PollImmediateWithContext(ctx, time.Millisecond*500, wait.ForeverTestTimeout, func(ctx context.Context) (done bool, err error) {
		cluster, err = kcpClient.ClusterV1alpha1().Clusters().Get(ctx, cluster.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if conditions.IsTrue(cluster, clusterv1alpha1.ClusterReadyCondition) {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return nil, err
	}

	return cluster, nil
}

func RequireDiff(t *testing.T, x, y interface{}, msgAndArgs ...interface{}) {
	diff := cmp.Diff(x, y)
	require.NotEmpty(t, diff, msgAndArgs...)
}

func RequireNoDiff(t *testing.T, x, y interface{}, msgAndArgs ...interface{}) {
	diff := cmp.Diff(x, y)
	require.Empty(t, diff, msgAndArgs...)
}
