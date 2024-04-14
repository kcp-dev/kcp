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

package faros

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"connectrpc.com/connect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apiserver/pkg/admission"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	kcpinitializers "github.com/kcp-dev/kcp/pkg/admission/initializers"
	"github.com/kcp-dev/kcp/sdk/apis/core"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"

	quotav1alpha1 "github.com/kcp-dev/cluster-proxy/apiserver/grpc/gen/private/faros/quota/v1alpha1"
	"github.com/kcp-dev/cluster-proxy/apiserver/grpc/gen/private/faros/quota/v1alpha1/quotav1alpha1connect"
)

// Validate and admit Workspace creation and updates for quota.

const (
	PluginName = "tenancy.faros.sh/Workspace"
)

func Register(plugins *admission.Plugins) {
	// TODO: Client url should be configurable.
	client := quotav1alpha1connect.NewQuotaServiceClient(
		http.DefaultClient,
		"http://api.cluster-proxy.svc:8080",
		connect.WithGRPC(),
	)

	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &workspace{
				client:  client,
				Handler: admission.NewHandler(admission.Create, admission.Update),
			}, nil
		})
}

// failureThreshold is the number of consecutive failures before the plugin is disabled.
const failureThreshold = 10

type workspace struct {
	*admission.Handler

	logicalClusterLister corev1alpha1listers.LogicalClusterClusterLister

	client quotav1alpha1connect.QuotaServiceClient

	failure int
}

// Ensure that the required admission interfaces are implemented.
var _ admission.MutationInterface = &workspace{}
var _ admission.ValidationInterface = &workspace{}
var _ = admission.InitializationValidator(&workspace{})
var _ = kcpinitializers.WantsKcpInformers(&workspace{})

// Admit ensures that
// TODO: add more details
func (o *workspace) Admit(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	return nil
}

// Validate ensures that
// - the workspace only does a valid phase transition
// - has a valid type and it is not mutated
// - the cluster is not removed
// - the user is recorded in annotations on create
// - the required groups match with the LogicalCluster.
func (o *workspace) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) (err error) {
	clusterName, err := genericapirequest.ClusterNameFrom(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}

	if a.GetResource().GroupResource() != tenancyv1alpha1.Resource("workspaces") {
		return nil
	}

	var action quotav1alpha1.Action
	if a.GetOperation() != admission.Create && a.GetOperation() != admission.Delete {
		return nil
	}
	if a.GetOperation() == admission.Create {
		action = quotav1alpha1.Action_ACTION_CREATE
	}
	if a.GetOperation() == admission.Delete {
		action = quotav1alpha1.Action_ACTION_DELETE
	}

	logicalCluster, err := o.logicalClusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
	if err != nil {
		return admission.NewForbidden(a, err)
	}

	path, ok := logicalCluster.Annotations[core.LogicalClusterPathAnnotationKey]
	if !ok { // no path annotation, nothing to do as might be system process
		return nil
	}

	req := connect.NewRequest(&quotav1alpha1.ValidateRequest{
		Path:   path,
		Action: action,
	})

	result, err := o.client.Validate(ctx, req)
	if err != nil {
		o.failure++
		if o.failure > failureThreshold { // passthrough after a number of consecutive failures
			return nil
		}
		return fmt.Errorf("failed to validate workspace creation, try again: %w", err)
	}
	o.failure = 0

	if result.Msg.Result == quotav1alpha1.QuotaResult_QUOTA_RESULT_EXCEEDED {
		return admission.NewForbidden(a, fmt.Errorf("quota exceeded: %s", result.Msg.GetMessage()))
	}
	if result.Msg.Result == quotav1alpha1.QuotaResult_QUOTA_RESULT_UNSPECIFIED {
		return admission.NewForbidden(a, fmt.Errorf("quota checking error: %s", result.Msg.GetMessage()))
	}

	return nil
}

func (o *workspace) ValidateInitialization() error {
	if o.logicalClusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an LogicalCluster lister")
	}
	return nil
}

func (o *workspace) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	logicalClustersReady := local.Core().V1alpha1().LogicalClusters().Informer().HasSynced
	o.SetReadyFunc(func() bool {
		return logicalClustersReady()
	})
	o.logicalClusterLister = local.Core().V1alpha1().LogicalClusters().Lister()
}
