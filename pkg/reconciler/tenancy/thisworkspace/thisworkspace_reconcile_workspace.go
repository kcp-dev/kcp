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

package thisworkspace

import (
	"context"
	"reflect"

	"github.com/kcp-dev/logicalcluster/v2"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancyv1beta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
)

// workspaceReconciler is a temporary reconciler that create real Workspace objects
// by-passing the projection to ClusterWorkspaces.
type workspaceReconciler struct {
	getWorkspace                     func(clusterName logicalcluster.Name, name string) (*tenancyv1beta1.Workspace, error)
	deleteWorkspaceWithoutProjection func(ctx context.Context, clusterName logicalcluster.Name, name string) error
	createWorkspaceWithoutProjection func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error)
	updateWorkspaceWithoutProjection func(ctx context.Context, clusterName logicalcluster.Name, this *tenancyv1beta1.Workspace) (*tenancyv1beta1.Workspace, error)
}

func (r *workspaceReconciler) reconcile(ctx context.Context, cw *tenancyv1alpha1.ClusterWorkspace) (reconcileStatus, error) {
	// not about new workspaces
	if cw.Status.Phase == tenancyv1alpha1.WorkspacePhaseScheduling {
		return reconcileStatusContinue, nil
	}

	if !cw.DeletionTimestamp.IsZero() {
		ws, err := r.getWorkspace(logicalcluster.From(cw), cw.Name)
		if err != nil && !errors.IsNotFound(err) {
			return reconcileStatusStopAndRequeue, err
		} else if err == nil && ws.DeletionTimestamp.IsZero() {
			if err := r.deleteWorkspaceWithoutProjection(ctx, logicalcluster.From(cw), cw.Name); err != nil && !errors.IsNotFound(err) {
				return reconcileStatusStopAndRequeue, err
			}
		}
		return reconcileStatusContinue, nil
	}

	if ws, err := r.getWorkspace(logicalcluster.From(cw), cw.Name); err != nil && !errors.IsNotFound(err) {
		return reconcileStatusStopAndRequeue, nil
	} else if errors.IsNotFound(err) {
		var ws tenancyv1beta1.Workspace
		projection.ProjectClusterWorkspaceToWorkspace(cw, &ws)
		ws.ObjectMeta = metav1.ObjectMeta{
			Name:        cw.Name,
			Annotations: ws.Annotations,
			Labels:      ws.Labels,
		}
		if _, err := r.createWorkspaceWithoutProjection(ctx, logicalcluster.From(cw), &ws); err != nil && !errors.IsAlreadyExists(err) {
			return reconcileStatusStopAndRequeue, nil
		}
	} else if !reflect.DeepEqual(ws.Annotations, cw.Annotations) || !reflect.DeepEqual(ws.Labels, cw.Labels) {
		ws := ws.DeepCopy()
		ws.Annotations = cw.Annotations
		ws.Labels = cw.Labels
		if _, err := r.updateWorkspaceWithoutProjection(ctx, logicalcluster.From(cw), ws); err != nil {
			return reconcileStatusStopAndRequeue, nil
		}
	}

	return reconcileStatusContinue, nil
}
