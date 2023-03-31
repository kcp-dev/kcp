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

package namespace

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
)

type reconcileResult struct {
	stop         bool
	requeueAfter time.Duration
}

type reconcileFunc func(ctx context.Context, key string, ns *corev1.Namespace) (reconcileResult, error)

func (c *controller) reconcile(ctx context.Context, key string, ns *corev1.Namespace) error {
	reconcilers := []reconcileFunc{
		c.reconcilePlacementBind,
		c.reconcileScheduling,
		c.reconcileStatus,
	}

	for _, r := range reconcilers {
		result, err := r(ctx, key, ns)
		if err != nil {
			return err
		}

		if result.stop {
			break
		}

		if result.requeueAfter > 0 {
			c.queue.AddAfter(key, result.requeueAfter)
		}
	}

	return nil
}
