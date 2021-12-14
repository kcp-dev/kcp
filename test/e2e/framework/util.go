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
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/discovery"
	cacheddiscovery "k8s.io/client-go/discovery/cached"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/restmapper"

	apiresourcev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apiresource/v1alpha1"
	clusterv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/cluster/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// ScratchDirs determines where artifacts and data should live for a test case.
func ScratchDirs(t TestingTInterface) (string, string, error) {
	var baseDir string
	if dir, set := os.LookupEnv("ARTIFACT_DIR"); set {
		baseDir = dir
	} else {
		baseDir = t.TempDir()
	}
	baseDir = filepath.Join(baseDir, strings.NewReplacer("\\", "_", ":", "_").Replace(t.Name()))
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return "", "", fmt.Errorf("could not create base dir: %w", err)
	}
	baseTempDir, err := os.MkdirTemp(baseDir, "")
	if err != nil {
		return "", "", fmt.Errorf("could not create base temp dir: %w", err)
	}
	var directories []string
	for _, prefix := range []string{"artifacts", "data"} {
		dir := filepath.Join(baseTempDir, prefix)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", "", fmt.Errorf("could not create subdir: %w", err)
		}
		directories = append(directories, dir)
	}
	t.Logf("Saving test artifacts and data under %s.", baseTempDir)
	return directories[0], directories[1], nil
}

var localSchemeBuilder = runtime.SchemeBuilder{
	apiresourcev1alpha1.AddToScheme,
	tenancyv1alpha1.AddToScheme,
	clusterv1alpha1.AddToScheme,
}

func init() {
	utilruntime.Must(localSchemeBuilder.AddToScheme(scheme.Scheme))
}

// Artifact runs the data-producing function and dumps the YAML-formatted output
// to the artifact directory for the test. Normal usage looks like:
// defer framework.Artifact(t, kcp, client.Get(ctx, name, metav1.GetOptions{}))
func (c *kcpServer) Artifact(tinterface TestingTInterface, producer func() (runtime.Object, error)) {
	t, ok := tinterface.(*T)
	if !ok {
		tinterface.Logf("Artifact() called with %#v, not a framework.T", tinterface)
		return
	}
	data, err := producer()
	if err != nil {
		t.Logf("error fetching artifact: %v", err)
		return
	}
	accessor, ok := data.(metav1.Object)
	if !ok {
		t.Logf("artifact has no object meta: %#v", data)
		return
	}
	dir := path.Join(c.artifactDir, accessor.GetClusterName())
	if accessor.GetNamespace() != "" {
		dir = path.Join(dir, accessor.GetNamespace())
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Logf("could not create dir: %v", err)
		return
	}
	gvks, _, err := scheme.Scheme.ObjectKinds(data)
	if err != nil {
		t.Logf("error finding gvk for artifact: %v", err)
		return
	}
	if len(gvks) == 0 {
		t.Logf("found no gvk for artifact: %T", data)
		return
	}
	gvk := gvks[0]
	data.GetObjectKind().SetGroupVersionKind(gvk)

	cfg, err := c.Config()
	if err != nil {
		t.Logf("could not get config for server: %v", err)
		return
	}
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		t.Logf("could not get discovery client for server: %v", err)
		return
	}
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cacheddiscovery.NewMemCacheClient(discoveryClient))
	mapping, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		t.Logf("could not get REST mapping for artifact's GVK: %v", err)
		return
	}
	file := path.Join(dir, fmt.Sprintf("%s_%s.yaml", mapping.Resource.GroupResource().String(), accessor.GetName()))
	t.Logf("saving artifact to %s", file)

	serializer := json.NewSerializerWithOptions(json.DefaultMetaFactory, scheme.Scheme, scheme.Scheme, json.SerializerOptions{Yaml: true})
	raw := bytes.Buffer{}
	if err := serializer.Encode(data, &raw); err != nil {
		t.Logf("error marshalling artifact: %v", err)
		return
	}
	if err := ioutil.WriteFile(file, raw.Bytes(), 0644); err != nil {
		t.Logf("error writing artifact: %v", err)
		return
	}
}

// GetFreePort asks the kernel for a free open port that is ready to use.
func GetFreePort(t TestingTInterface) (string, error) {
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
