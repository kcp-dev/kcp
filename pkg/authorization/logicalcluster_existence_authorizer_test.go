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

package authorization

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapirequest "k8s.io/apiserver/pkg/endpoints/request"

	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
)

// stubLogicalClusterGetter serves the given objects, keyed by their cluster annotation.
func stubLogicalClusterGetter(objects ...*corev1alpha1.LogicalCluster) LogicalClusterGetter {
	byCluster := make(map[logicalcluster.Name]*corev1alpha1.LogicalCluster, len(objects))
	for _, obj := range objects {
		byCluster[logicalcluster.Name(obj.Annotations[logicalcluster.AnnotationKey])] = obj
	}

	return func(cluster logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) {
		if obj, found := byCluster[cluster]; found {
			return obj, nil
		}
		return nil, apierrors.NewNotFound(corev1alpha1.Resource("logicalclusters"), corev1alpha1.LogicalClusterName)
	}
}

func newLogicalCluster(cluster logicalcluster.Name, phase corev1alpha1.LogicalClusterPhaseType) *corev1alpha1.LogicalCluster {
	return &corev1alpha1.LogicalCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:        corev1alpha1.LogicalClusterName,
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster.String()},
		},
		Status: corev1alpha1.LogicalClusterStatus{Phase: phase},
	}
}

func TestLogicalClusterExistenceAuthorizer(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		requested    logicalcluster.Name
		groups       []string
		deepSAR      bool
		getErr       error
		wantDecision authorizer.Decision
		wantReason   string
		wantError    string
	}{
		"existing logical cluster is passed on": {
			requested:    logicalcluster.Name("root:ready"),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "LogicalCluster exists",
		},
		"missing logical cluster is denied": {
			requested:    logicalcluster.Name("root:gone"),
			wantDecision: authorizer.DecisionDeny,
			wantReason:   "LogicalCluster not found",
		},
		"system workspace is skipped": {
			requested:    logicalcluster.Name("system:admin"),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "empty or system workspace",
		},
		"empty cluster is skipped": {
			requested:    logicalcluster.Name(""),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "empty or system workspace",
		},
		"deep SAR is skipped": {
			requested:    logicalcluster.Name("root:gone"),
			deepSAR:      true,
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "deep SAR request",
		},
		"lookup error is passed on": {
			requested:    logicalcluster.Name("root:ready"),
			getErr:       errors.New("lister is down"),
			wantDecision: authorizer.DecisionNoOpinion,
			wantReason:   "error getting LogicalCluster",
			wantError:    "lister is down",
		},
	}

	for title, cas := range cases {
		t.Run(title, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			ctx = genericapirequest.WithCluster(ctx, genericapirequest.Cluster{Name: cas.requested})
			if cas.deepSAR {
				ctx = context.WithValue(ctx, deepSARKey, true)
			}

			getter := stubLogicalClusterGetter(newLogicalCluster(logicalcluster.Name("root:ready"), corev1alpha1.LogicalClusterPhaseReady))
			if cas.getErr != nil {
				getter = func(logicalcluster.Name) (*corev1alpha1.LogicalCluster, error) { return nil, cas.getErr }
			}
			a := NewLogicalClusterExistenceAuthorizer(getter)

			attr := authorizer.AttributesRecord{User: &user.DefaultInfo{Name: "user-1", Groups: cas.groups}}

			decision, reason, err := a.Authorize(ctx, attr)
			if cas.wantError != "" {
				require.EqualError(t, err, cas.wantError)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, cas.wantDecision, decision)
			require.Equal(t, cas.wantReason, reason)
		})
	}
}
