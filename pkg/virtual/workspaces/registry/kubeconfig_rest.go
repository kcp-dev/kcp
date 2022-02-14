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

package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/registry/rest"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	tenancyclient "github.com/kcp-dev/kcp/pkg/client/clientset/versioned/typed/tenancy/v1alpha1"
	"github.com/kcp-dev/kcp/third_party/conditions/util/conditions"
)

type KubeconfigSubresourceREST struct {
	mainRest *REST

	// coreClient is useful to get secrets
	coreClient corev1client.CoreV1Interface
	// workspaceShardClient can get KCP workspace shards
	workspaceShardClient tenancyclient.WorkspaceShardInterface
}

var _ rest.Getter = &KubeconfigSubresourceREST{}
var _ rest.Scoper = &KubeconfigSubresourceREST{}

// Get retrieves a ClusterWorkspace KubeConfig by workspace name
func (s *KubeconfigSubresourceREST) Get(ctx context.Context, name string, options *metav1.GetOptions) (runtime.Object, error) {
	wrapError := func(err error) error {
		k8sErr := kerrors.NewNotFound(tenancyv1alpha1.SchemeGroupVersion.WithResource("workspaces/kubeconfig").GroupResource(), name)
		k8sErr.Status().Details.Causes = append(k8sErr.Status().Details.Causes, metav1.StatusCause{
			Type:    metav1.CauseTypeUnexpectedServerResponse,
			Message: err.Error(),
		})
		return k8sErr
	}

	workspace, err := s.mainRest.getClusterWorkspace(ctx, name, options)
	if kerrors.IsNotFound(err) {
		return nil, kerrors.NewNotFound(tenancyv1beta1.SchemeGroupVersion.WithResource("workspaces").GroupResource(), name)
	}
	if err != nil {
		return nil, err
	}
	scope := ctx.Value(WorkspacesScopeKey).(string)
	if !conditions.IsTrue(workspace, tenancyv1alpha1.WorkspaceURLValid) {
		return nil, wrapError(errors.New("ClusterWorkspace URL is not valid"))
	}
	shard, err := s.workspaceShardClient.Get(ctx, workspace.Status.Location.Current, metav1.GetOptions{})
	if err != nil {
		return nil, wrapError(err)
	}
	secret, err := s.coreClient.Secrets(shard.Spec.Credentials.Namespace).Get(ctx, shard.Spec.Credentials.Name, metav1.GetOptions{})
	if err != nil {
		return nil, wrapError(err)
	}
	data, ok := secret.Data[tenancyv1alpha1.WorkspaceShardCredentialsKey]
	if !ok {
		return nil, wrapError(fmt.Errorf("Key '%s' not found in workspace shard Kubeconfig secret", tenancyv1alpha1.WorkspaceShardCredentialsKey))
	}
	shardKubeConfig, err := clientcmd.Load(data)
	if err != nil {
		return nil, wrapError(fmt.Errorf("ClusterWorkspace shard Kubeconfig is invalid: %w", err))
	}

	currentContext := shardKubeConfig.Contexts[shardKubeConfig.CurrentContext]
	if currentContext == nil {
		return nil, wrapError(errors.New("Workspace shard Kubeconfig has no current context"))
	}
	currentCluster := shardKubeConfig.Clusters[currentContext.Cluster]
	if currentCluster == nil {
		return nil, wrapError(fmt.Errorf("ClusterWorkspace shard Kubeconfig has no cluster corresponding to the current context cluster key: %s", currentContext.Cluster))
	}
	currentCluster.Server = workspace.Status.BaseURL

	workspaceContextName := scope + "/" + workspace.Name

	// return a kubeconfig that lacks the user and its credentials,
	// i.e. it's only the cluster definition with its CA cert and URL, etc ...
	workspaceConfig := &api.Config{
		APIVersion:     "v1",
		Clusters:       map[string]*api.Cluster{workspaceContextName: currentCluster},
		Contexts:       map[string]*api.Context{workspaceContextName: {Cluster: workspaceContextName}},
		CurrentContext: workspaceContextName,
	}
	dataToReturn, err := clientcmd.Write(*workspaceConfig)
	if err != nil {
		return nil, wrapError(err)
	}
	return KubeConfig(string(dataToReturn)), nil
}

func (s *KubeconfigSubresourceREST) NamespaceScoped() bool {
	return false
}

// New creates a new ClusterWorkspace log options object
func (r *KubeconfigSubresourceREST) New() runtime.Object {
	// TODO - return a resource that represents a log
	return &tenancyv1beta1.Workspace{}
}

// ProducesMIMETypes returns a list of the MIME types the specified HTTP verb (GET, POST, DELETE,
// PATCH) can respond with.
func (r *KubeconfigSubresourceREST) ProducesMIMETypes(verb string) []string {
	// Since the default list does not include "plain/text", we need to
	// explicitly override ProducesMIMETypes, so that it gets added to
	// the "produces" section for workspaces/{name}/kubeconfig
	return []string{
		"application/yaml",
	}
}

// ProducesObject returns an object the specified HTTP verb respond with. It will overwrite storage object if
// it is not nil. Only the type of the return object matters, the value will be ignored.
func (r *KubeconfigSubresourceREST) ProducesObject(verb string) interface{} {
	return ""
}

type KubeConfig string

// a LocationStreamer must implement a rest.ResourceStreamer
var _ rest.ResourceStreamer = KubeConfig("")

func (obj KubeConfig) GetObjectKind() schema.ObjectKind {
	return schema.EmptyObjectKind
}
func (obj KubeConfig) DeepCopyObject() runtime.Object {
	panic("rest.LocationStreamer does not implement DeepCopyObject")
}

// InputStream returns a stream with the contents of the URL location. If no location is provided,
// a null stream is returned.
func (s KubeConfig) InputStream(ctx context.Context, apiVersion, acceptHeader string) (stream io.ReadCloser, flush bool, contentType string, err error) {
	return io.NopCloser(strings.NewReader(string(s))), true, "text/plain", nil
}
