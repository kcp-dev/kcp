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

package hops

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFromHeader(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value string
		want  int
	}{
		{"absent", "", 0},
		{"first hop", "1", 1},
		{"not a number", "lots", Max},
		{"negative", "-1", Max},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := http.Header{}
			if tc.value != "" {
				h.Set(Header, tc.value)
			}
			require.Equal(t, tc.want, FromHeader(h),
				"a header a client can forge must never yield fewer hops than it claims")
		})
	}
}

// The count has to survive a virtual workspace that answers by starting a fresh
// client request: header in, context, header out. Without the middle step a
// cycle restarts the count every lap and never reaches the limit.
func TestCountSurvivesReOrigination(t *testing.T) {
	t.Parallel()

	var forwarded http.Header
	var delegateErr error
	inbound := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/apis/s3.dev/v1alpha1/bucketinfos", http.NoBody)
	SetHeader(inbound.Header, 2)

	WithRequestHops(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		// What a virtual workspace's client-go delegation looks like: a new
		// request that inherits only the context.
		outbound := httptest.NewRequestWithContext(req.Context(), http.MethodGet, "/clusters/x/apis/s3.dev/v1alpha1/bucketinfos", http.NoBody)

		rt := roundTripper{delegate: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			forwarded = r.Header
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		})}
		resp, err := rt.RoundTrip(outbound)
		delegateErr = err
		if resp != nil {
			resp.Body.Close()
		}
	})).ServeHTTP(httptest.NewRecorder(), inbound)

	require.NoError(t, delegateErr)
	require.Equal(t, 2, FromHeader(forwarded),
		"the delegated request must carry the count of the request it serves")
}

// A cycle must terminate. Each lap is: the shard forwards (+1), the virtual
// workspace delegates back carrying the same count.
func TestCycleTerminates(t *testing.T) {
	t.Parallel()

	h := http.Header{}
	laps := 0
	for !Exceeded(FromHeader(h)) {
		SetHeader(h, FromHeader(h)+1)
		laps++
		require.Less(t, laps, 100, "forwarding did not terminate")
	}
	require.Equal(t, Max, laps, "a cycle should be cut off after Max laps")
}

// The chain a cached resource takes must still fit: a client reaches the
// APIExport virtual workspace, which delegates to the shard, which forwards to
// the replication virtual workspace.
func TestLegitimateChainFits(t *testing.T) {
	t.Parallel()

	clientToVW := 0                     // client -> APIExport VW, no header
	vwToShard := clientToVW             // delegated, carries the same count
	shardToReplication := vwToShard + 1 // forwarded, counts a hop

	require.False(t, Exceeded(vwToShard), "the shard must accept the delegated request")
	require.False(t, Exceeded(shardToReplication), "the replication VW must be reachable")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
