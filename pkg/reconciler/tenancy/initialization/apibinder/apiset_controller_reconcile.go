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
	"fmt"

	"github.com/kcp-dev/logicalcluster"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	conditionsv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/kcp/pkg/apis/third_party/conditions/util/conditions"
	initializationv1alpha1 "github.com/kcp-dev/kcp/pkg/reconciler/tenancy/initialization/apis/initialization/v1alpha1"
)

func (b *APISetValidator) reconcile(ctx context.Context, apiSet *initializationv1alpha1.APISet) {
	var errors []error
	clusterName := logicalcluster.From(apiSet)
	klog.V(2).Infof("Validating %d APIBindings in %q|%q", len(apiSet.Spec.Bindings), clusterName, apiSet.Name)
	for i, bindingSpec := range apiSet.Spec.Bindings {
		workspace := bindingSpec.Reference.Workspace
		if workspace == nil {
			errors = append(errors, fmt.Errorf("spec.bindings[%d] invalid: no workspace reference", i))
			continue
		}

		if _, err := b.apiExportLister.Get(clusters.ToClusterAwareKey(logicalcluster.New(workspace.Path), workspace.ExportName)); err != nil {
			errors = append(errors, fmt.Errorf("spec.bindings[%d] invalid: %w", i, err))
		}
	}
	if len(errors) > 0 {
		klog.V(2).Infof("Failed validating APIBindings in %s|%s", clusterName, apiSet.Name)
		conditions.MarkFalse(
			apiSet,
			initializationv1alpha1.APIBindingsValid,
			initializationv1alpha1.APIBindingInvalidReason,
			conditionsv1alpha1.ConditionSeverityError,
			"encountered errors: %v",
			utilerrors.NewAggregate(errors),
		)
	} else {
		klog.V(2).Infof("Succeeded validating APIBindings in %s|%s", clusterName, apiSet.Name)
		conditions.MarkTrue(apiSet, initializationv1alpha1.APIBindingsValid)
	}
}
