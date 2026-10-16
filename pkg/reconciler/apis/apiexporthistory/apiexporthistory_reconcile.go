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

package apiexporthistory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"

	"github.com/kcp-dev/kcp/pkg/logging"
)

// HistoryName returns the name of the history of an APIExport.
func HistoryName(clusterName logicalcluster.Name, name string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(clusterName.String()+"|"+name)))
}

// reconcile records the scope of every group resource the APIExport serves. Entries are
// only ever added, so a scope cannot silently change when a schema is swapped out.
func (c *controller) reconcile(ctx context.Context, apiExport *apisv1alpha2.APIExport) error {
	logger := klog.FromContext(ctx)

	observed, err := c.observedScopes(apiExport)
	if err != nil {
		return err
	}

	name := HistoryName(logicalcluster.From(apiExport), apiExport.Name)
	existing, err := c.getHistory(name)
	if apierrors.IsNotFound(err) {
		history := &adminv1alpha1.APIExportHistory{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		}

		created, err := c.createHistory(ctx, history)
		if apierrors.IsAlreadyExists(err) {
			return nil // the informer will trigger another reconcile
		} else if err != nil {
			return fmt.Errorf("failed to create scope history for APIExport %s|%s: %w", logicalcluster.From(apiExport), apiExport.Name, err)
		}

		existing = created
	} else if err != nil {
		return err
	}

	merged, changed := mergeScopes(existing.Status.Resources, observed)
	if !changed {
		return nil
	}

	history := existing.DeepCopy()
	history.Status.Resources = merged

	logging.WithObject(logger, history).V(2).Info("recording APIExport resource scopes")
	if _, err := c.updateHistoryStatus(ctx, history); err != nil {
		return fmt.Errorf("failed to update scope history %s: %w", name, err)
	}

	return nil
}

// observedScopes resolves the scopes of the schemas the APIExport currently references.
func (c *controller) observedScopes(apiExport *apisv1alpha2.APIExport) ([]adminv1alpha1.ResourceHistory, error) {
	clusterName := logicalcluster.From(apiExport)
	observed := make([]adminv1alpha1.ResourceHistory, 0, len(apiExport.Spec.Resources))
	for _, resource := range apiExport.Spec.Resources {
		schema, err := c.getAPIResourceSchema(clusterName, resource.Schema)
		if apierrors.IsNotFound(err) {
			continue
		} else if err != nil {
			return nil, err
		}

		observed = append(observed, adminv1alpha1.ResourceHistory{
			Group:    schema.Spec.Group,
			Resource: schema.Spec.Names.Plural,
			Scope:    schema.Spec.Scope,
		})
	}

	return observed, nil
}

// deleteHistories removes the history left behind by a deleted APIExport.
func (c *controller) deleteHistories(ctx context.Context, clusterName logicalcluster.Name, name string) error {
	if err := c.deleteHistory(ctx, HistoryName(clusterName, name)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete scope history for APIExport %s|%s: %w", clusterName, name, err)
	}

	return nil
}

// mergeScopes adds the observed scopes that are not recorded yet, never replacing one.
func mergeScopes(recorded, observed []adminv1alpha1.ResourceHistory) ([]adminv1alpha1.ResourceHistory, bool) {
	known := make(map[string]struct{}, len(recorded))
	for _, resource := range recorded {
		known[resource.Group+"/"+resource.Resource] = struct{}{}
	}

	merged := append([]adminv1alpha1.ResourceHistory{}, recorded...)
	changed := false
	for _, resource := range observed {
		key := resource.Group + "/" + resource.Resource
		if _, ok := known[key]; ok {
			continue
		}
		known[key] = struct{}{}
		merged = append(merged, resource)
		changed = true
	}

	if !changed {
		return recorded, false
	}

	sort.Slice(merged, func(i, j int) bool {
		if merged[i].Group != merged[j].Group {
			return merged[i].Group < merged[j].Group
		}
		return merged[i].Resource < merged[j].Resource
	})

	return merged, true
}
