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

package apibinder

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/kcp-dev/logicalcluster"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"

	apisv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/apis/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/initialization"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	initializationv1alpha1 "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization/v1alpha1"
)

const (
	initializedCondition conditionsv1alpha1.ConditionType = "APIBindingsInitialized"

	failedBootstrappingReason = "Failed to bootstrap API Bindings."
)

func (b *APIBinder) reconcile(ctx context.Context, clusterWorkspace *tenancyv1alpha1.ClusterWorkspace, configuration *initializationv1alpha1.APISet) {
	var errors []error
	clusterName := logicalcluster.From(clusterWorkspace).Join(clusterWorkspace.Name)
	klog.V(2).Infof("Initializing %d APIBindings in %q", len(configuration.Spec.Bindings), clusterName)
	client := b.kcpClusterClient.Cluster(clusterName).ApisV1alpha1().APIBindings()
	for i, bindingSpec := range configuration.Spec.Bindings {
		if bindingSpec.Reference.Workspace == nil {
			errors = append(errors, fmt.Errorf("spec.bindings[%d] invalid: no workspace reference", i))
			continue
		}
		hash := sha256.Sum224([]byte(bindingSpec.Reference.Workspace.Path + bindingSpec.Reference.Workspace.ExportName))
		name := fmt.Sprintf("%x", hash)
		klog.V(2).Infof("Creating APIBinding %q|%q", clusterName, name)
		binding, err := client.Create(ctx, &apisv1alpha1.APIBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: bindingSpec,
		}, metav1.CreateOptions{})
		if err != nil && !k8serrors.IsAlreadyExists(err) {
			errors = append(errors, fmt.Errorf("could not create APIBinding %q: %w", name, err))
			continue
		}

		// TODO(skuznets,sttts): inline waiting like this is not ideal, for two reaons:
		// 1. it artificially delays the queue by taking up processing time with waiting
		// 2. users see no output in status until our timeout finishes
		// We're increasing the number of workers for now to counteract 1, and users can deal with
		// 2 for a small amount of time. We need to revisit this.
		klog.V(2).Infof("Waiting for APIBinding %q|%q to be bound", clusterName, name)
		if err := wait.ExponentialBackoffWithContext(ctx, wait.Backoff{
			Duration: 100 * time.Millisecond,
			Factor:   2,
			Steps:    10,
		}, func() (done bool, err error) {
			binding, err = client.Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			return conditions.IsTrue(binding, apisv1alpha1.InitialBindingCompleted), nil
		}); err != nil {
			errors = append(errors, fmt.Errorf("APIBinding %q was never bound: %w", name, err))
		}
	}
	if len(errors) > 0 {
		klog.V(2).Infof("Failed initializing APIBindings in %q", clusterName)
		conditions.MarkFalse(
			clusterWorkspace,
			initializedCondition,
			failedBootstrappingReason,
			conditionsv1alpha1.ConditionSeverityError,
			"encountered errors: %v",
			utilerrors.NewAggregate(errors),
		)
	} else {
		klog.V(2).Infof("Succeeded initializing APIBindings in %q", clusterName)
		conditions.MarkTrue(clusterWorkspace, initializedCondition)
		clusterWorkspace.Status.Initializers = initialization.EnsureInitializerAbsent(b.initializer, clusterWorkspace.Status.Initializers)
	}
}
