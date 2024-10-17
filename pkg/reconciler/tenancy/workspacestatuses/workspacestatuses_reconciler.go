/*
Copyright 2024 The KCP Authors.

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

package workspacestatuses

import (
	"context"

	utilserrors "k8s.io/apimachinery/pkg/util/errors"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
)

type reconcileStatus int

const (
	reconcileStatusStopAndRequeue reconcileStatus = iota
	reconcileStatusContinue
)

type reconciler interface {
	reconcile(ctx context.Context, workspace *tenancyv1alpha1.Workspace) (reconcileStatus, error)
}

// reconcile reconciles the workspace objects and controls the phase of the workspace.
func (c *Controller) reconcile(ctx context.Context, ws *tenancyv1alpha1.Workspace) (bool, error) {
	reconcilers := []reconciler{
		&workspacePhaseUpdater{},
	}

	var errs []error

	requeue := false
	for _, r := range reconcilers {
		var err error
		var status reconcileStatus
		status, err = r.reconcile(ctx, ws)
		if err != nil {
			errs = append(errs, err)
		}
		if status == reconcileStatusStopAndRequeue {
			requeue = true
			break
		}
	}

	return requeue, utilserrors.NewAggregate(errs)
}
