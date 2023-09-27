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
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"embed"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"
	"github.com/stretchr/testify/require"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	kubernetesscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/cert"
	"sigs.k8s.io/yaml"

	"github.com/kcp-dev/kcp/config/helpers"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
	kcpscheme "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/scheme"
)

//go:embed *.csv
//go:embed *.yaml
var fs embed.FS

// WriteTokenAuthFile writes the embedded token file to the current
// test's data dir.
//
// Persistent servers can target the file in the source tree with
// `--token-auth-file` and test-managed servers can target a file
// written to a temp path. This avoids requiring a test to know the
// location of the token file.
//
// TODO(marun) Is there a way to avoid embedding by determining the
// path to the file during test execution?
func WriteTokenAuthFile(t *testing.T) string {
	t.Helper()
	return WriteEmbedFile(t, "auth-tokens.csv")
}

func WriteEmbedFile(t *testing.T, source string) string {
	t.Helper()
	data, err := fs.ReadFile(source)
	require.NoErrorf(t, err, "error reading embed file: %q", source)

	targetPath := path.Join(t.TempDir(), source)
	targetFile, err := os.Create(targetPath)
	require.NoErrorf(t, err, "failed to create target file: %q", targetPath)
	defer targetFile.Close()

	_, err = targetFile.Write(data)
	require.NoError(t, err, "error writing target file: %q", targetPath)

	return targetPath
}

// Persistent mapping of test name to base temp dir used to ensure
// artifact paths have a common root across servers for a given test.
var (
	baseTempDirs     = map[string]string{}
	baseTempDirsLock = sync.Mutex{}
)

// ensureBaseTempDir returns the name of a base temp dir for the
// current test, creating it if needed.
func ensureBaseTempDir(t *testing.T) (string, error) {
	t.Helper()

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

// CreateTempDirForTest creates the named directory with a unique base
// path derived from the name of the current test.
func CreateTempDirForTest(t *testing.T, dirName string) (string, error) {
	t.Helper()
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
	t.Helper()
	artifactDir, err := CreateTempDirForTest(t, "artifacts")
	if err != nil {
		return "", "", err
	}
	return artifactDir, t.TempDir(), nil
}

func (c *kcpServer) CADirectory() string {
	return c.dataDir
}

func (c *kcpServer) Artifact(t *testing.T, producer func() (runtime.Object, error)) {
	t.Helper()
	artifact(t, c, producer)
}

// artifact registers the data-producing function to run and dump the YAML-formatted output
// to the artifact directory for the test before the kcp process is terminated.
func artifact(t *testing.T, server RunningServer, producer func() (runtime.Object, error)) {
	t.Helper()

	subDir := filepath.Join("artifacts", "kcp", server.Name())
	artifactDir, err := CreateTempDirForTest(t, subDir)
	require.NoError(t, err, "could not create artifacts dir")
	// Using t.Cleanup ensures that artifact collection is local to
	// the test requesting retention regardless of server's scope.
	t.Cleanup(func() {
		data, err := producer()
		require.NoError(t, err, "error fetching artifact")

		accessor, ok := data.(metav1.Object)
		require.True(t, ok, "artifact has no object meta: %#v", data)

		dir := path.Join(artifactDir, logicalcluster.From(accessor).String())
		dir = strings.ReplaceAll(dir, ":", "_") // github actions don't like colon because NTFS is unhappy with it in path names
		if accessor.GetNamespace() != "" {
			dir = path.Join(dir, accessor.GetNamespace())
		}
		err = os.MkdirAll(dir, 0755)
		require.NoError(t, err, "could not create dir")

		gvks, _, err := kubernetesscheme.Scheme.ObjectKinds(data)
		if err != nil {
			gvks, _, err = kcpscheme.Scheme.ObjectKinds(data)
		}
		require.NoError(t, err, "error finding gvk for artifact")
		require.NotEmpty(t, gvks, "found no gvk for artifact: %T", data)
		gvk := gvks[0]
		data.GetObjectKind().SetGroupVersionKind(gvk)

		group := gvk.Group
		if group == "" {
			group = "core"
		}

		gvkForFilename := fmt.Sprintf("%s_%s", group, gvk.Kind)

		file := path.Join(dir, fmt.Sprintf("%s-%s.yaml", gvkForFilename, accessor.GetName()))
		file = strings.ReplaceAll(file, ":", "_") // github actions don't like colon because NTFS is unhappy with it in path names

		bs, err := yaml.Marshal(data)
		require.NoError(t, err, "error marshalling artifact")

		err = os.WriteFile(file, bs, 0644)
		require.NoError(t, err, "error writing artifact")
	})
}

// GetFreePort asks the kernel for a free open port that is ready to use.
func GetFreePort(t *testing.T) (string, error) {
	t.Helper()

	for {
		addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
		if err != nil {
			return "", fmt.Errorf("could not resolve free port: %w", err)
		}

		l, err := net.ListenTCP("tcp", addr)
		if err != nil {
			return "", fmt.Errorf("could not listen on free port: %w", err)
		}
		// This defer is in a loop, although it should only be retried a fixed number of times, and return.
		defer func(c io.Closer) { //nolint:gocritic
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

// LogicalClusterRawConfig returns the raw cluster config of the given config.
func LogicalClusterRawConfig(rawConfig clientcmdapi.Config, logicalClusterName logicalcluster.Path, contextName string) clientcmdapi.Config {
	var (
		contextClusterName  = rawConfig.Contexts[contextName].Cluster
		contextAuthInfoName = rawConfig.Contexts[contextName].AuthInfo
		configCluster       = *rawConfig.Clusters[contextClusterName] // shallow copy
	)

	configCluster.Server += logicalClusterName.RequestPath()

	return clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			contextName: &configCluster,
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextName: {
				Cluster:  contextName,
				AuthInfo: contextAuthInfoName,
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			contextAuthInfoName: rawConfig.AuthInfos[contextAuthInfoName],
		},
		CurrentContext: contextName,
	}
}

// Eventually asserts that given condition will be met in waitFor time, periodically checking target function
// each tick. In addition to require.Eventually, this function t.Logs the raason string value returned by the condition
// function (eventually after 20% of the wait time) to aid in debugging.
func Eventually(t *testing.T, condition func() (success bool, reason string), waitFor time.Duration, tick time.Duration, msgAndArgs ...interface{}) {
	t.Helper()

	var last string
	start := time.Now()
	require.Eventually(t, func() bool {
		t.Helper()

		ok, msg := condition()
		if time.Since(start) > waitFor/5 {
			if !ok && msg != "" && msg != last {
				last = msg
				t.Logf("Waiting for condition, but got: %s", msg)
			} else if ok && msg != "" && last != "" {
				t.Logf("Condition became true: %s", msg)
			}
		}
		return ok
	}, waitFor, tick, msgAndArgs...)
}

// EventuallyReady asserts that the object returned by getter() eventually has a ready condition.
func EventuallyReady(t *testing.T, getter func() (conditions.Getter, error), msgAndArgs ...interface{}) {
	t.Helper()
	EventuallyCondition(t, getter, Is(conditionsv1alpha1.ReadyCondition), msgAndArgs...)
}

type ConditionEvaluator struct {
	conditionType   conditionsv1alpha1.ConditionType
	conditionStatus corev1.ConditionStatus
	conditionReason *string
}

func (c *ConditionEvaluator) matches(object conditions.Getter) (*conditionsv1alpha1.Condition, string, bool) {
	condition := conditions.Get(object, c.conditionType)
	if condition == nil {
		return nil, c.descriptor(), false
	}
	if condition.Status != c.conditionStatus {
		return condition, c.descriptor(), false
	}
	if c.conditionReason != nil && condition.Reason != *c.conditionReason {
		return condition, c.descriptor(), false
	}
	return condition, c.descriptor(), true
}

func (c *ConditionEvaluator) descriptor() string {
	var descriptor string
	switch c.conditionStatus {
	case corev1.ConditionTrue:
		descriptor = "to be"
	case corev1.ConditionFalse:
		descriptor = "not to be"
	case corev1.ConditionUnknown:
		descriptor = "to not know if it is"
	}
	descriptor += fmt.Sprintf(" %s", c.conditionType)
	if c.conditionReason != nil {
		descriptor += fmt.Sprintf(" (with reason %s)", *c.conditionReason)
	}
	return descriptor
}

func Is(conditionType conditionsv1alpha1.ConditionType) *ConditionEvaluator {
	return &ConditionEvaluator{
		conditionType:   conditionType,
		conditionStatus: corev1.ConditionTrue,
	}
}

func IsNot(conditionType conditionsv1alpha1.ConditionType) *ConditionEvaluator {
	return &ConditionEvaluator{
		conditionType:   conditionType,
		conditionStatus: corev1.ConditionFalse,
	}
}

func (c *ConditionEvaluator) WithReason(reason string) *ConditionEvaluator {
	c.conditionReason = &reason
	return c
}

// EventuallyCondition asserts that the object returned by getter() eventually has a condition that matches the evaluator.
func EventuallyCondition(t *testing.T, getter func() (conditions.Getter, error), evaluator *ConditionEvaluator, msgAndArgs ...interface{}) {
	t.Helper()
	Eventually(t, func() (bool, string) {
		obj, err := getter()
		require.NoError(t, err, "Error fetching object")
		condition, descriptor, done := evaluator.matches(obj)
		var reason string
		if !done {
			if condition != nil {
				reason = fmt.Sprintf("Not done waiting for object %s: %s: %s", descriptor, condition.Reason, condition.Message)
			} else {
				reason = fmt.Sprintf("Not done waiting for object %s: no condition present", descriptor)
			}
		}
		return done, reason
	}, wait.ForeverTestTimeout, 100*time.Millisecond, msgAndArgs...)
}

// ClientCAUserConfig returns a config based on a dynamically created client certificate.
// The returned client CA is signed by "test/e2e/framework/client-ca.crt".
func ClientCAUserConfig(t *testing.T, cfg *rest.Config, clientCAConfigDirectory, username string, groups ...string) *rest.Config {
	t.Helper()
	caBytes, err := os.ReadFile(filepath.Join(clientCAConfigDirectory, "client-ca.crt"))
	require.NoError(t, err, "error reading CA file")
	caKeyBytes, err := os.ReadFile(filepath.Join(clientCAConfigDirectory, "client-ca.key"))
	require.NoError(t, err, "error reading CA key")
	caCerts, err := cert.ParseCertsPEM(caBytes)
	require.NoError(t, err, "error parsing CA certs")
	caKeys, err := tls.X509KeyPair(caBytes, caKeyBytes)
	require.NoError(t, err, "error parsing CA keys")
	clientPublicKey, clientPrivateKey, err := newRSAKeyPair()
	require.NoError(t, err, "error creating client keys")
	currentTime := time.Now()
	clientCert := &x509.Certificate{
		Subject: pkix.Name{
			CommonName:   username,
			Organization: groups,
		},

		SignatureAlgorithm: x509.SHA256WithRSA,

		NotBefore:    currentTime.Add(-1 * time.Second),
		NotAfter:     currentTime.Add(time.Hour * 2),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	signedClientCertBytes, err := x509.CreateCertificate(cryptorand.Reader, clientCert, caCerts[0], clientPublicKey, caKeys.PrivateKey)
	require.NoError(t, err, "error creating client certificate")
	clientCertPEM := new(bytes.Buffer)
	require.NoError(t, pem.Encode(clientCertPEM, &pem.Block{Type: "CERTIFICATE", Bytes: signedClientCertBytes}), "error encoding client cert")
	clientKeyPEM := new(bytes.Buffer)
	require.NoError(t, pem.Encode(clientKeyPEM, &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(clientPrivateKey)}), "error encoding client private key")

	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.CertData = clientCertPEM.Bytes()
	cfgCopy.KeyData = clientKeyPEM.Bytes()
	cfgCopy.BearerToken = ""
	return cfgCopy
}

func newRSAKeyPair() (*rsa.PublicKey, *rsa.PrivateKey, error) {
	privateKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}
	return &privateKey.PublicKey, privateKey, nil
}

// StaticTokenUserConfig returns a user config based on static user tokens defined in "test/e2e/framework/auth-tokens.csv".
// The token being used is "[username]-token".
func StaticTokenUserConfig(username string, cfg *rest.Config) *rest.Config {
	return ConfigWithToken(username+"-token", cfg)
}

func ConfigWithToken(token string, cfg *rest.Config) *rest.Config {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.CertData = nil
	cfgCopy.KeyData = nil
	cfgCopy.BearerToken = token
	return cfgCopy
}

// UniqueGroup returns a unique API group with the given suffix by prefixing
// some random 8 character base36 string. suffix must start with a dot if the
// random string should be dot-separated.
func UniqueGroup(suffix string) string {
	ret := strings.ToLower(base36.Encode(rand.Uint64())[:8]) + suffix
	if ret[0] >= '0' && ret[0] <= '9' {
		return "a" + ret[1:]
	}
	return ret
}

// CreateResources creates all resources from a filesystem in the given workspace identified by upstreamConfig.
func CreateResources(ctx context.Context, fs embed.FS, upstreamConfig *rest.Config, clusterName logicalcluster.Path, transformers ...helpers.TransformFileFunc) error {
	dynamicClusterClient, err := kcpdynamic.NewForConfig(upstreamConfig)
	if err != nil {
		return err
	}

	client := dynamicClusterClient.Cluster(clusterName)

	apiextensionsClusterClient, err := kcpapiextensionsclientset.NewForConfig(upstreamConfig)
	if err != nil {
		return err
	}

	discoveryClient := apiextensionsClusterClient.Cluster(clusterName).Discovery()

	cache := memory.NewMemCacheClient(discoveryClient)
	mapper := restmapper.NewDeferredDiscoveryRESTMapper(cache)

	return helpers.CreateResourcesFromFS(ctx, client, mapper, sets.New[string](), fs, transformers...)
}
