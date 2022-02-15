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

package authorizer

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	kubeyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"

	kcp "github.com/kcp-dev/kcp/pkg/client/clientset/versioned"
	"github.com/kcp-dev/kcp/test/e2e/framework"
)

//go:embed *.yaml
var embeddedResources embed.FS

func createResources(ctx context.Context, filename string, client dynamic.Interface, decoder runtime.Decoder) error {
	f, err := embeddedResources.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to read resources: %w", err)
	}

	d := kubeyaml.NewYAMLReader(bufio.NewReader(f))
	for {
		doc, err := d.Read()
		if errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		u := &unstructured.Unstructured{}
		_, gvk, err := decoder.Decode(doc, nil, u)
		if err != nil {
			return fmt.Errorf("error decoding %s: %w", filename, err)
		}

		resource := gvk.Kind
		resource = strings.ToLower(resource)
		resource += "s"

		gvr := schema.GroupVersionResource{
			Group:    gvk.Group,
			Version:  gvk.Version,
			Resource: resource,
		}

		if _, err := client.Resource(gvr).Create(ctx, u, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("error creating %s: %w", gvr, err)
		}
	}

	return nil
}

type clients struct {
	KubeClient kubernetes.Interface
	KcpClient  kcp.Interface
	Dynamic    dynamic.Interface
}

func newUserClient(t *testing.T, username, orgWorkspace, workspace string, cfg *rest.Config) *clients {
	cfgCopy := rest.CopyConfig(cfg)
	cfgCopy.Host = cfg.Host + "/clusters/" + orgWorkspace + "_" + workspace
	cfgCopy.BearerToken = username + "-token"
	kubeClient, err := kubernetes.NewForConfig(cfgCopy)
	require.NoError(t, err)
	kcpClient, err := kcp.NewForConfig(cfgCopy)
	require.NoError(t, err)
	dynamicClient, err := dynamic.NewForConfig(cfgCopy)
	require.NoError(t, err)
	return &clients{
		KubeClient: kubeClient,
		KcpClient:  kcpClient,
		Dynamic:    dynamicClient,
	}
}

func TestAuthorizer(t *testing.T) {
	t.Parallel()

	usersKCPArgs, err := framework.Users([]framework.User{
		{
			Name:   "user-1",
			UID:    "1111-1111-1111-1111",
			Token:  "user-1-token",
			Groups: []string{"team-1"},
		},
		{
			Name:   "user-2",
			UID:    "1111-1111-1111-1111",
			Token:  "user-2-token",
			Groups: []string{"team-2"},
		},
		{
			Name:   "user-3",
			UID:    "1111-1111-1111-1111",
			Token:  "user-3-token",
			Groups: []string{"team-3"},
		},
	}).ArgsForKCP(t)
	require.NoError(t, err)

	f := framework.NewKcpFixture(t, framework.KcpConfig{
		Name: "main",
		Args: usersKCPArgs,
	})

	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		withDeadline, cancel := context.WithDeadline(ctx, deadline)
		t.Cleanup(cancel)
		ctx = withDeadline
	}
	require.Equalf(t, len(f.Servers), 1, "incorrect number of servers")

	server := f.Servers["main"]
	kcpCfg, err := server.Config()
	require.NoError(t, err)
	clusterName, err := framework.DetectClusterName(kcpCfg, ctx, "workspaces.tenancy.kcp.dev")
	require.NoError(t, err)
	kubeClients, err := kubernetes.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)
	kcpClients, err := kcp.NewClusterForConfig(kcpCfg)
	require.NoError(t, err)
	kcpClient := kcpClients.Cluster(clusterName)
	require.NoError(t, err)
	dynamicClient, err := dynamic.NewForConfig(kcpCfg)
	require.NoError(t, err)

	clients := map[string]*clients{
		"admin": {
			KubeClient: kubeClients.Cluster(clusterName),
			KcpClient:  kcpClient,
			Dynamic:    dynamicClient,
		},
		"user-1": newUserClient(t, "user-1", "admin", "workspace1", kcpCfg),
		"user-2": newUserClient(t, "user-2", "admin", "workspace1", kcpCfg),
		"user-3": newUserClient(t, "user-3", "admin", "workspace1", kcpCfg),
	}

	err = createResources(ctx, "resources.yaml", clients["admin"].Dynamic, serializer.NewCodecFactory(scheme.Scheme).UniversalDeserializer())
	require.NoError(t, err)

	_, err = clients["user-1"].KubeClient.CoreV1().Namespaces().Create(
		ctx,
		&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)
	_, err = clients["user-1"].KubeClient.CoreV1().ConfigMaps("default").Create(
		ctx,
		&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		metav1.CreateOptions{},
	)
	require.NoError(t, err)

	_, err = clients["user-2"].KubeClient.CoreV1().Namespaces().Create(
		ctx,
		&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "baz"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-2"].KubeClient.CoreV1().ConfigMaps("default").Create(
		ctx,
		&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)

	_, err = clients["user-2"].KubeClient.CoreV1().Namespaces().Get(
		ctx, "default",
		metav1.GetOptions{},
	)
	require.NoError(t, err)
	_, err = clients["user-2"].KubeClient.CoreV1().ConfigMaps("default").Get(
		ctx, "foo",
		metav1.GetOptions{},
	)
	require.NoError(t, err)

	_, err = clients["user-3"].KubeClient.CoreV1().Namespaces().Create(
		ctx,
		&v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "baz"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-3"].KubeClient.CoreV1().ConfigMaps("default").Create(
		ctx,
		&v1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "foo"}},
		metav1.CreateOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-3"].KubeClient.CoreV1().Namespaces().Get(
		ctx, "default",
		metav1.GetOptions{},
	)
	require.Error(t, err)
	_, err = clients["user-3"].KubeClient.CoreV1().ConfigMaps("default").Get(
		ctx, "foo",
		metav1.GetOptions{},
	)
	require.Error(t, err)
}
