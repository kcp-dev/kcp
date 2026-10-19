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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

const (
	oldHash = "1111111111111111111111111111111111111111111111111111111111111111"
	newHash = "2222222222222222222222222222222222222222222222222222222222222222"
)

var (
	testGroupExportNames         = map[string]string{"tenancy.kcp.io": "tenancy.kcp.io"}
	testGroupResourceExportNames = map[schema.GroupResource]string{{Group: "core.kcp.io", Resource: "shards"}: "shards.core.kcp.io"}
)

type fakeProvider struct {
	lock   sync.Mutex
	hashes map[string]string
	err    error
}

func (p *fakeProvider) set(name, hash string) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.hashes[name] = hash
}

func (p *fakeProvider) setErr(err error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.err = err
}

func (p *fakeProvider) get(_ context.Context, name string) (ResolvedIdentity, error) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if p.err != nil {
		return ResolvedIdentity{}, p.err
	}
	return legacy(p.hashes[name]), nil
}

func resolvedIdentities(t *testing.T, provider *fakeProvider) *Identities {
	t.Helper()
	ids := NewIdentities(testGroupExportNames, testGroupResourceExportNames)
	if err := ids.Resolve(context.Background(), provider.get); err != nil {
		t.Fatalf("failed to resolve identities: %v", err)
	}
	return ids
}

func decorate(t *testing.T, ids *Identities, urlPath string) string {
	t.Helper()
	got, _, err := decorateWildcardPathsWithResourceIdentities(urlPath, ids)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return got
}

func TestRefreshIdentities(t *testing.T) {
	t.Parallel()

	provider := &fakeProvider{hashes: map[string]string{"tenancy.kcp.io": oldHash, "shards.core.kcp.io": oldHash}}
	ids := resolvedIdentities(t, provider)
	changes := 0
	ids.AddChangeListener(func() { changes++ })
	refresh := func() { ids.Refresh(context.Background(), provider.get) }

	unchanged := newExpirableWatchBody(io.NopCloser(strings.NewReader("")), true)
	ids.trackWatch(oldHash, unchanged)

	refresh()
	if unchanged.expired || changes != 0 {
		t.Fatal("a refresh without identity changes must not expire watches or notify listeners")
	}

	provider.set("tenancy.kcp.io", newHash)
	refresh()
	if got, want := decorate(t, ids, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces"), "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:"+newHash; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
	if got, want := decorate(t, ids, "/clusters/*/apis/core.kcp.io/v1alpha1/shards"), "/clusters/*/apis/core.kcp.io/v1alpha1/shards:"+oldHash; got != want {
		t.Errorf("an unchanged export must keep its identity: expected %q, got %q", want, got)
	}
	if !unchanged.expired {
		t.Error("watches using the replaced identity must be expired")
	}
	if changes != 1 {
		t.Errorf("expected the change listener to be called once, got %d", changes)
	}
	if got := ids.Snapshot()["tenancy.kcp.io"].Hash; got != newHash {
		t.Errorf("expected the snapshot to report the new identity, got %q", got)
	}

	// an export that cannot be resolved keeps its identity.
	provider.setErr(errors.New("root shard unavailable"))
	refresh()
	if got, want := decorate(t, ids, "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces"), "/clusters/*/apis/tenancy.kcp.io/v1alpha1/workspaces:"+newHash; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
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

	provider.set("tenancy.kcp.io", newHash)
	ids.Refresh(ctx, provider.get)

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
