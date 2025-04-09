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

package workspacemounts

import (
	"context"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kcp-dev/logicalcluster/v3"

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

// reconcile reconciles the workspace objects. It is intended to be single reconciler for all the
// workspace replated operations. For now it has single reconciler that updates the status of the
// workspace based on the mount status.
func (c *Controller) reconcile(ctx context.Context, ws *tenancyv1alpha1.Workspace) (bool, error) {
	getMountObjectFunc := func(ctx context.Context, cluster logicalcluster.Path, ref tenancyv1alpha1.ObjectReference) (*unstructured.Unstructured, error) {
		// TODO(sttts): do proper REST mapping.
		resource := strings.ToLower(ref.Kind) + "s"
		gvr := schema.GroupVersionResource{Resource: resource}
		cs := strings.SplitN(ref.APIVersion, "/", 2)
		if len(cs) == 2 {
			gvr.Group = cs[0]
			gvr.Version = cs[1]
		} else {
			gvr.Version = ref.APIVersion
		}
		return c.dynamicClusterClient.Cluster(cluster).Resource(gvr).Get(ctx, ref.Name, metav1.GetOptions{})
	}

	reconcilers := []reconciler{
		&workspaceStatusUpdater{
			getMountObject: getMountObjectFunc,
		},
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

	return requeue, utilerrors.NewAggregate(errs)
}
