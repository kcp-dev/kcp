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

package context

import (
	"context"
	"errors"
)

// workloadClusterNameContextKeyType is the type of the key for the request context value
// that will carry the name of the WorkloadCluster resources with be synced with.
type workloadClusterNameContextKeyType string

// apiDomainKeyContextKey is the key for the request context value
// that will carry the name of the WorkloadCluster resources with be synced with.
const workloadClusterNameContextKey workloadClusterNameContextKeyType = "SyncerVirtualWorkspaceWorkloadClusterKey"

// WithWorkloadClusterName adds a WorkloadCluster name to the context.
func WithWorkloadClusterName(ctx context.Context, workloadClusterName string) context.Context {
	return context.WithValue(ctx, workloadClusterNameContextKey, workloadClusterName)
}

// WorkloadClusterNameFrom retrieves the WorkloadCluster name key from the context, if any.
func WorkloadClusterNameFrom(ctx context.Context) (string, error) {
	wcn, hasWorkloadClusterName := ctx.Value(workloadClusterNameContextKey).(string)
	if !hasWorkloadClusterName {
		return "", errors.New("context must contain a valid non-empty WorkloadCluster name")
	}
	return wcn, nil
}
