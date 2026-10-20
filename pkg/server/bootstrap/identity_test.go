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

package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
)

const (
	oldHash = "1111111111111111111111111111111111111111111111111111111111111111"
	newHash = "2222222222222222222222222222222222222222222222222222222222222222"
	// otherHash is the identity of an export that is not rotated.
	otherHash = "3333333333333333333333333333333333333333333333333333333333333333"
)

var (
	testGroupExportNames         = map[string]string{"tenancy.kcp.io": "tenancy.kcp.io"}
	testGroupResourceExportNames = map[schema.GroupResource]string{{Group: "core.kcp.io", Resource: "shards"}: "shards.core.kcp.io"}
)

type fakeProvider struct {
	hashes map[string]string
}

func (p *fakeProvider) get(_ context.Context, name string) (string, error) {
	return p.hashes[name], nil
}

// export is an APIExport of the given workspace with the given identity.
func export(cluster, name, hash string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{logicalcluster.AnnotationKey: cluster}},
		Status:     apisv1alpha2.APIExportStatus{IdentityHash: hash},
	}
}

func resolvedIdentities(t *testing.T, provider *fakeProvider) *identities {
	t.Helper()
	ids := newIdentities(testGroupExportNames, testGroupResourceExportNames)
	if err := wildcardIdentitiesResolver(ids, testGroupExportNames, testGroupResourceExportNames, provider.get)(context.Background()); err != nil {
		t.Fatalf("failed to resolve identities: %v", err)
	}
	return ids
}

func decorate(t *testing.T, ids *identities, urlPath string) string {
	t.Helper()
	got, _, err := decorateWildcardPathsWithResourceIdentities(urlPath, ids)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return got
}

func TestAPIExportHandler(t *testing.T) {
	t.Parallel()

	tenancyPath := "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces"
	shardsPath := "/clusters/*/apis/core.kcp.io/v1alpha1/shards"

	ignored := []struct {
		name string
		obj  interface{}
	}{
		{name: "the same identity", obj: export("root", "tenancy.kcp.io", oldHash)},
		{name: "an export that serves no core API", obj: export("root", "cowboys", newHash)},
		{name: "a same-named export of another workspace", obj: export("root:org", "tenancy.kcp.io", newHash)},
		{name: "an export without identity", obj: export("root", "tenancy.kcp.io", "")},
		{name: "a tombstone", obj: cache.DeletedFinalStateUnknown{Key: "root|tenancy.kcp.io", Obj: export("root", "tenancy.kcp.io", newHash)}},
	}
	for _, scenario := range ignored {
		t.Run("ignores "+scenario.name, func(t *testing.T) {
			t.Parallel()
			ids := resolvedIdentities(t, &fakeProvider{hashes: map[string]string{"tenancy.kcp.io": oldHash, "shards.core.kcp.io": oldHash}})
			watch := newExpirableWatchBody(io.NopCloser(strings.NewReader("")), true)
			ids.trackWatch(oldHash, watch)

			handler := apiExportHandler(ids)
			handler.OnAdd(scenario.obj, false)
			handler.OnUpdate(scenario.obj, scenario.obj)

			if got, want := decorate(t, ids, tenancyPath), tenancyPath+":"+oldHash; got != want {
				t.Errorf("expected %q, got %q", want, got)
			}
			if watch.expired {
				t.Error("watches must not be expired when no identity changed")
			}
		})
	}

	t.Run("replaces the identity of the changed export only", func(t *testing.T) {
		t.Parallel()
		ids := resolvedIdentities(t, &fakeProvider{hashes: map[string]string{"tenancy.kcp.io": oldHash, "shards.core.kcp.io": oldHash}})
		watch := newExpirableWatchBody(io.NopCloser(strings.NewReader("")), true)
		ids.trackWatch(oldHash, watch)

		apiExportHandler(ids).OnUpdate(export("root", "tenancy.kcp.io", oldHash), export("root", "tenancy.kcp.io", newHash))

		if got, want := decorate(t, ids, tenancyPath), tenancyPath+":"+newHash; got != want {
			t.Errorf("expected %q, got %q", want, got)
		}
		if got, want := decorate(t, ids, shardsPath), shardsPath+":"+oldHash; got != want {
			t.Errorf("an unchanged export must keep its identity: expected %q, got %q", want, got)
		}
		if !watch.expired {
			t.Error("watches using the replaced identity must be expired")
		}
	})

	t.Run("sets an identity that is not resolved yet without expiring anything", func(t *testing.T) {
		t.Parallel()
		ids := newIdentities(testGroupExportNames, testGroupResourceExportNames)
		watch := newExpirableWatchBody(io.NopCloser(strings.NewReader("")), true)
		ids.trackWatch(oldHash, watch)

		apiExportHandler(ids).OnAdd(export("root", "shards.core.kcp.io", newHash), true)

		if got, want := decorate(t, ids, shardsPath), shardsPath+":"+newHash; got != want {
			t.Errorf("expected %q, got %q", want, got)
		}
		if _, _, err := decorateWildcardPathsWithResourceIdentities(tenancyPath, ids); err == nil {
			t.Error("expected the unresolved tenancy identity to still be unknown")
		}
		if watch.expired {
			t.Error("learning an identity must not expire watches")
		}
	})
}

// TestWatchExpiresOnIdentityChange runs a real watch through the round
// tripper and checks that an identity change ends it with a 410 Expired
// error event, which makes reflectors relist.
func TestWatchExpiresOnIdentityChange(t *testing.T) {
	t.Parallel()

	paths := make(chan string, 10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"type":"ADDED","object":{"apiVersion":"tenancy.kcp.io/v1alpha1","kind":"Workspace","metadata":{"name":"ws","resourceVersion":"1"}}}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	provider := &fakeProvider{hashes: map[string]string{"tenancy.kcp.io": oldHash, "shards.core.kcp.io": oldHash}}
	ids := resolvedIdentities(t, provider)

	config := &rest.Config{Host: server.URL + "/clusters/*"}
	config.Wrap(injectKcpIdentities(ids))
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), wait30s)
	t.Cleanup(cancel)
	w, err := client.Resource(schema.GroupVersionResource{Group: "tenancy.kcp.io", Version: "v1alpha1", Resource: "workspaces"}).Watch(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatalf("failed to watch: %v", err)
	}
	t.Cleanup(w.Stop)

	if got, want := <-paths, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:"+oldHash; got != want {
		t.Fatalf("expected the watch to be sent to %q, got %q", want, got)
	}
	if event := next(ctx, t, w); event.Type != watch.Added {
		t.Fatalf("expected an ADDED event, got %s", event.Type)
	}

	apiExportHandler(ids).OnUpdate(export("root", "tenancy.kcp.io", oldHash), export("root", "tenancy.kcp.io", newHash))

	event := next(ctx, t, w)
	if event.Type != watch.Error {
		t.Fatalf("expected an ERROR event, got %s", event.Type)
	}
	if err := apierrors.FromObject(event.Object); !apierrors.IsResourceExpired(err) {
		t.Fatalf("expected a 410 Expired error, got %v", err)
	}
	select {
	case _, ok := <-w.ResultChan():
		if ok {
			t.Fatal("expected the watch to end after the expired event")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the watch to end")
	}

	ids.watchesLock.Lock()
	open := len(ids.watches)
	ids.watchesLock.Unlock()
	if open != 0 {
		t.Errorf("expected no tracked watches after the watch ended, got %d", open)
	}
}

// TestWatchExpiresWhenIdentityChangesMidRequest covers the window between
// reading the identity for the request path and tracking the watch once the
// response headers arrive: an identity replaced in that window has already
// expired the tracked watches, so the late one must be expired on arrival.
func TestWatchExpiresWhenIdentityChangesMidRequest(t *testing.T) {
	t.Parallel()

	ids := resolvedIdentities(t, &fakeProvider{hashes: map[string]string{"tenancy.kcp.io": oldHash, "shards.core.kcp.io": otherHash}})
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	// the transport swaps the identity while the request is in flight.
	transport := roundTripperFunc{fn: func(req *http.Request) (*http.Response, error) {
		if got, want := req.URL.Path, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:"+oldHash; got != want {
			t.Errorf("expected the request to be sent to %q, got %q", want, got)
		}
		apiExportHandler(ids).OnUpdate(export("root", "tenancy.kcp.io", oldHash), export("root", "tenancy.kcp.io", newHash))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       pr,
			Request:    req,
		}, nil
	}}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://kcp/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces?watch=true", http.NoBody)
	require.NoError(t, err)
	resp, err := injectKcpIdentities(ids)(transport).RoundTrip(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, ok := resp.Body.(*expirableWatchBody)
	require.True(t, ok, "expected the watch body to be tracked")
	require.True(t, body.expired, "a watch sent with an identity that was replaced in flight must be expired on arrival")
	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, expiredWatchEvent, rest, "the reader must get the expired event")

	// the same request after the swap goes out with the new identity and is not expired.
	plain := roundTripperFunc{fn: func(req *http.Request) (*http.Response, error) {
		require.Equal(t, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:"+newHash, req.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
	}}
	resp, err = injectKcpIdentities(ids)(plain).RoundTrip(req)
	require.NoError(t, err)
	require.False(t, resp.Body.(*expirableWatchBody).expired, "a watch on the current identity must not be expired")
}

// TestExpireDoesNotBlockOnStalledReader checks that expiring a watch whose
// reader is blocked in Read returns right away and unblocks the reader: the
// body lock is not held across the underlying read, and closing the stream
// ends it.
func TestExpireDoesNotBlockOnStalledReader(t *testing.T) {
	t.Parallel()

	pr, pw := io.Pipe()
	body := newExpirableWatchBody(pr, true)

	readDone := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(body) // blocks: nothing is ever written
		readDone <- err
	}()
	// give the reader time to block in the underlying Read.
	time.Sleep(50 * time.Millisecond)

	expired := make(chan struct{})
	go func() {
		body.expire()
		close(expired)
	}()
	select {
	case <-expired:
	case <-time.After(wait30s):
		t.Fatal("expire blocked on a stalled reader")
	}
	select {
	case err := <-readDone:
		require.NoError(t, err, "the stalled reader must end cleanly")
	case <-time.After(wait30s):
		t.Fatal("the stalled reader was not unblocked by expire")
	}
	_ = pw.Close()
}

const wait30s = 30 * time.Second

func next(ctx context.Context, t *testing.T, w watch.Interface) watch.Event {
	t.Helper()
	select {
	case event, ok := <-w.ResultChan():
		if !ok {
			t.Fatal("watch ended unexpectedly")
		}
		return event
	case <-ctx.Done():
		t.Fatal("timed out waiting for a watch event")
	}
	return watch.Event{}
}

func TestExpirableWatchBody(t *testing.T) {
	t.Parallel()

	scenarios := []struct {
		name      string
		stream    string
		json      bool
		wantEvent bool
	}{
		{name: "JSON stream between events gets the expired event", stream: "{\"type\":\"ADDED\"}\n", json: true, wantEvent: true},
		{name: "JSON stream in the middle of an event just ends", stream: "{\"type\":\"ADD", json: true},
		{name: "non-JSON stream just ends", stream: "\x00\x01\n"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			pr, pw := io.Pipe()
			body := newExpirableWatchBody(pr, scenario.json)
			go func() { _, _ = pw.Write([]byte(scenario.stream)) }()

			buf := make([]byte, len(scenario.stream))
			if _, err := io.ReadFull(body, buf); err != nil {
				t.Fatalf("failed to read the stream: %v", err)
			}
			body.expire()

			rest, err := io.ReadAll(body)
			if err != nil {
				t.Fatalf("expected the stream to end cleanly, got %v", err)
			}
			if got := bytes.Equal(rest, expiredWatchEvent); got != scenario.wantEvent {
				t.Fatalf("expected expired event %v, got remaining stream %q", scenario.wantEvent, rest)
			}
		})
	}
}
