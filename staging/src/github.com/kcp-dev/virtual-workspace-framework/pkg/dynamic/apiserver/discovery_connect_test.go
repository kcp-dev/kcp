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

package apiserver

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"
)

// connecter is a minimal streaming subresource storage, in the shape pods/exec has.
type connecter struct {
	methods []string
}

func (c *connecter) New() runtime.Object { return nil }
func (c *connecter) Destroy()            {}
func (c *connecter) Connect(ctx context.Context, id string, options runtime.Object, r rest.Responder) (http.Handler, error) {
	return nil, nil
}
func (c *connecter) NewConnectOptions() (runtime.Object, bool, string) { return nil, false, "" }
func (c *connecter) ConnectMethods() []string                          { return c.methods }

var _ rest.Connecter = (*connecter)(nil)

// TestSupportedVerbsForConnecter pins the rule that a streaming subresource is
// authorized under the verb derived from its HTTP method, not under a literal
// "connect". kubectl exec POSTs to pods/exec and therefore needs "create" on it.
func TestSupportedVerbsForConnecter(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		methods []string
		want    metav1.Verbs
	}{
		"exec-shaped, GET and POST": {
			methods: []string{"GET", "POST"},
			want:    metav1.Verbs{"create", "get"},
		},
		"portforward-shaped, POST only": {
			methods: []string{"POST"},
			want:    metav1.Verbs{"create"},
		},
		"lowercase methods are still recognised": {
			methods: []string{"get"},
			want:    metav1.Verbs{"get"},
		},
		"a method with no verb equivalent is dropped rather than guessed": {
			methods: []string{"GET", "OPTIONS"},
			want:    metav1.Verbs{"get"},
		},
		"duplicate methods collapse": {
			methods: []string{"GET", "GET"},
			want:    metav1.Verbs{"get"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, supportedVerbs(&connecter{methods: tc.methods}))
		})
	}
}

// TestSupportedVerbsConnecterTakesPrecedence guards the ordering in supportedVerbs:
// a Connecter is neither a Getter nor a Creater, so falling through to the interface
// assertions would advertise no verbs at all and make the subresource look unusable.
func TestSupportedVerbsConnecterTakesPrecedence(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, supportedVerbs(&connecter{methods: []string{"POST"}}),
		"a connect subresource must advertise the verbs its methods imply")
}
