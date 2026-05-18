/*
Copyright 2026 The kcp Authors.

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

package testing

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"

	"github.com/kcp-dev/kcp/test/load/pkg/tree"
)

// workspacesExist checks whether count workspaces already exist by
// verifying that the last workspace name is present. This is a cheap heuristic
// that avoids listing all workspaces.
func workspacesExist(wt tree.WorkspaceTree, client kcpclientset.ClusterInterface, count int) (bool, error) {
	lastName := wt.WorkspaceName(count)
	parentPath := wt.PathForSequenceNumber(wt.ParentSequenceNumber(count))
	_, err := client.Cluster(parentPath).TenancyV1alpha1().Workspaces().Get(context.Background(), lastName, metav1.GetOptions{})
	if err != nil {
		// we also need IsForbidden here in case we are requesting a child ws whose
		// parent does not exist either.
		if apierrors.IsNotFound(err) || apierrors.IsForbidden(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
