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

package crdnooverlappinggvr

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kcp-dev/logicalcluster/v3"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/admission"
	"k8s.io/apiserver/pkg/endpoints/request"
	"k8s.io/client-go/util/retry"
	"k8s.io/utils/ptr"

	"github.com/kcp-dev/kcp/pkg/reconciler/apis/apibinding"
	corev1alpha1 "github.com/kcp-dev/kcp/sdk/apis/core/v1alpha1"
	kcpclientset "github.com/kcp-dev/kcp/sdk/client/clientset/versioned/cluster"
	kcpinformers "github.com/kcp-dev/kcp/sdk/client/informers/externalversions"
	corev1alpha1listers "github.com/kcp-dev/kcp/sdk/client/listers/core/v1alpha1"
)

const (
	PluginName = "apis.kcp.io/CRDNoOverlappingGVR"
)

func Register(plugins *admission.Plugins) {
	plugins.Register(PluginName,
		func(_ io.Reader) (admission.Interface, error) {
			return &crdNoOverlappingGVRAdmission{
				Handler: admission.NewHandler(admission.Create),
				now:     metav1.Now,
			}, nil
		})
}

type crdNoOverlappingGVRAdmission struct {
	*admission.Handler

	updateLogicalCluster func(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster, opts metav1.UpdateOptions) (*corev1alpha1.LogicalCluster, error)
	logicalclusterLister corev1alpha1listers.LogicalClusterClusterLister

	now func() metav1.Time
}

// Ensure that the required admission interfaces are implemented.
var _ = admission.ValidationInterface(&crdNoOverlappingGVRAdmission{})
var _ = admission.InitializationValidator(&crdNoOverlappingGVRAdmission{})

func (p *crdNoOverlappingGVRAdmission) SetKcpInformers(local, global kcpinformers.SharedInformerFactory) {
	p.SetReadyFunc(local.Apis().V1alpha1().APIBindings().Informer().HasSynced)
	p.logicalclusterLister = local.Core().V1alpha1().LogicalClusters().Lister()
}

func (p *crdNoOverlappingGVRAdmission) SetKcpClusterClient(c kcpclientset.ClusterInterface) {
	p.updateLogicalCluster = func(ctx context.Context, logicalCluster *corev1alpha1.LogicalCluster, opts metav1.UpdateOptions) (*corev1alpha1.LogicalCluster, error) {
		return c.CoreV1alpha1().LogicalClusters().Cluster(logicalcluster.From(logicalCluster).Path()).Update(ctx, logicalCluster, opts)
	}
}

func (p *crdNoOverlappingGVRAdmission) ValidateInitialization() error {
	if p.logicalclusterLister == nil {
		return fmt.Errorf(PluginName + " plugin needs an LogicalCluster lister")
	}
	if p.updateLogicalCluster == nil {
		return fmt.Errorf(PluginName + " plugin needs a KCP cluster client")
	}
	return nil
}

// Validate checks if the given CRD's Group and Resource don't overlap with bound CRDs.
func (p *crdNoOverlappingGVRAdmission) Validate(ctx context.Context, a admission.Attributes, _ admission.ObjectInterfaces) error {
	if a.GetResource().GroupResource() != apiextensions.Resource("customresourcedefinitions") {
		return nil
	}
	if a.GetKind().GroupKind() != apiextensions.Kind("CustomResourceDefinition") {
		return nil
	}
	if a.GetOperation() != admission.Create {
		return nil
	}

	clusterName, err := request.ClusterNameFrom(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve cluster from context: %w", err)
	}

	if clusterName == apibinding.SystemBoundCRDsClusterName {
		// bound CRDs will have equal group and resource names.
		return nil
	}

	crd, ok := a.GetObject().(*apiextensions.CustomResourceDefinition)
	if !ok {
		return fmt.Errorf("unexpected type %T", a.GetObject())
	}

	// (optimistically) lock group resource for LogicalCluster. If this request
	// eventually fails, the logicalclustercleanup controller will clean them
	// up eventually.
	gr := schema.GroupResource{Group: crd.Spec.Group, Resource: crd.Spec.Names.Plural}
	var skipped map[schema.GroupResource]apibinding.Lock
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		lc, err := p.logicalclusterLister.Cluster(clusterName).Get(corev1alpha1.LogicalClusterName)
		if errors.IsNotFound(err) && strings.HasPrefix(string(clusterName), "system:") {
			// in system logical clusters this is not fatal. We usually don't have a LogicalCluster there.
			return nil
		} else if err != nil {
			return fmt.Errorf("failed to get LogicalCluster in logical cluster %q: %w", clusterName, err)
		}

		var updated *corev1alpha1.LogicalCluster
		updated, _, skipped, err = apibinding.WithLockedResources(nil, time.Now(), lc, []schema.GroupResource{gr}, apibinding.ExpirableLock{
			Lock:      apibinding.Lock{CRD: true},
			CRDExpiry: ptr.To(p.now()),
		})
		if err != nil {
			return fmt.Errorf("failed to lock resources %s in logical cluster %q: %w", gr, logicalcluster.From(crd), err)
		}

		_, err = p.updateLogicalCluster(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("failed to lock resources %s in logical cluster %q: %w", gr, logicalcluster.From(crd), err)
	}
	if len(skipped) > 0 {
		return admission.NewForbidden(a, fmt.Errorf("cannot create because resource is bound by APIBinding %q", skipped[gr].Name))
	}

	return nil
}
