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
	"context"
	"embed"
	"fmt"
	"math/rand"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	kcpapiextensionsclientset "github.com/kcp-dev/client-go/apiextensions/client"
	kcpdynamic "github.com/kcp-dev/client-go/dynamic"
	"github.com/kcp-dev/logicalcluster/v3"
	"github.com/martinlindhe/base36"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/kcp-dev/kcp/config/helpers"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/util/conditions"
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

type ArtifactFunc func(*testing.T, func() (runtime.Object, error))

// Eventually asserts that given condition will be met in waitFor time, periodically checking target function
// each tick. In addition to require.Eventually, this function t.Logs the reason string value returned by the condition
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

// LoadKubeConfig loads a kubeconfig from disk. This method is
// intended to be common between fixture for servers whose lifecycle
// is test-managed and fixture for servers whose lifecycle is managed
// separately from a test run.
func LoadKubeConfig(kubeconfigPath, contextName string) (clientcmd.ClientConfig, error) {
	fs, err := os.Stat(kubeconfigPath)
	if err != nil {
		return nil, err
	}
	if fs.Size() == 0 {
		return nil, fmt.Errorf("%s points to an empty file", kubeconfigPath)
	}

	rawConfig, err := clientcmd.LoadFromFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load admin kubeconfig: %w", err)
	}

	return clientcmd.NewNonInteractiveClientConfig(*rawConfig, contextName, nil, nil), nil
}
