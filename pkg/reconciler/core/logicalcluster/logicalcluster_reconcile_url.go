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

package logicalcluster

import (
	"context"
	"strings"

	"github.com/kcp-dev/logicalcluster/v3"

	corev1alpha1 "github.com/kcp-dev/kcp/pkg/apis/core/v1alpha1"
)

type urlReconciler struct {
	shardExternalURL func() string
}

func (r *urlReconciler) reconcile(ctx context.Context, this *corev1alpha1.LogicalCluster) (reconcileStatus, error) {
	this.Status.URL = strings.TrimSuffix(r.shardExternalURL(), "/") + logicalcluster.From(this).Path().RequestPath()
	return reconcileStatusContinue, nil
}
