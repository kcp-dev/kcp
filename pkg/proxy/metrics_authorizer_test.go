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

package proxy

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	bootstrappolicy "github.com/kcp-dev/kcp/pkg/authorization/bootstrap"
)

func TestMetricsGroupAuthorizer(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		groups []string
		want   authorizer.Decision
	}{
		{name: "kcp admin may scrape", groups: []string{bootstrappolicy.SystemKcpAdminGroup, user.AllAuthenticated}, want: authorizer.DecisionAllow},
		{name: "system:masters may scrape", groups: []string{bootstrappolicy.SystemMastersGroup}, want: authorizer.DecisionAllow},
		{name: "the metrics-reader group may scrape", groups: []string{bootstrappolicy.SystemKcpMetricsReader}, want: authorizer.DecisionAllow},
		// no opinion, not deny: a further authorizer in the union (the :root
		// SubjectAccessReview while --root-kubeconfig is given) may still allow.
		{name: "an ordinary user gets no opinion", groups: []string{user.AllAuthenticated}, want: authorizer.DecisionNoOpinion},
		{name: "no groups gets no opinion", groups: nil, want: authorizer.DecisionNoOpinion},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _, err := newMetricsGroupAuthorizer().Authorize(context.Background(), authorizer.AttributesRecord{
				User: &user.DefaultInfo{Name: "scraper", Groups: tc.groups},
				Verb: "get",
				Path: "/metrics",
			})
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
