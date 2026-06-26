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

package committer

import (
	"context"

	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

// ConditionsCommitFunc commits the owned-condition delta between old and new, optionally applying mutate to the same object.
type ConditionsCommitFunc[T conditions.Setter] func(ctx context.Context, old, new T, mutate func(T) bool) error

// NewConditionsCommitter returns a function that applies the owned conditions onto a copy of old and persists it via update.
func NewConditionsCommitter[T conditions.Setter](owned []conditionsv1alpha1.ConditionType, update func(context.Context, T) error) ConditionsCommitFunc[T] {
	return func(ctx context.Context, old, new T, mutate func(T) bool) error {
		condPatch := conditions.NewPatch(old, new)

		latest := old.DeepCopyObject().(T)
		changed := mutate != nil && mutate(latest)

		if condPatch.IsZero() && !changed {
			return nil
		}

		if err := condPatch.Apply(latest, conditions.WithOwnedConditions(owned...)); err != nil {
			return err
		}

		return update(ctx, latest)
	}
}
