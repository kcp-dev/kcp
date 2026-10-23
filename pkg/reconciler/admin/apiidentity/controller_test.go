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

package apiidentity

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
)

const identity = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeRegistry is an in-memory APIIdentity store.
type fakeRegistry struct {
	mu      sync.Mutex
	entries map[string]*adminv1alpha1.APIIdentity
}

func (r *fakeRegistry) Get(_ context.Context, name string, _ metav1.GetOptions) (*adminv1alpha1.APIIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[name]
	if !ok {
		return nil, apierrors.NewNotFound(adminv1alpha1.Resource("apiidentities"), name)
	}
	return entry.DeepCopy(), nil
}

func (r *fakeRegistry) Update(_ context.Context, entry *adminv1alpha1.APIIdentity, _ metav1.UpdateOptions) (*adminv1alpha1.APIIdentity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[entry.Name] = entry.DeepCopy()
	return entry.DeepCopy(), nil
}

// fixture is one shard's view: the exports it hosts (local), the exports
// replicated to the cache (global, which includes the local ones) and the
// registry.
type fixture struct {
	t        *testing.T
	registry *fakeRegistry
	local    map[string]*apisv1alpha2.APIExport // cluster|name
	global   map[string]*apisv1alpha2.APIExport
	requeued []time.Duration
	c        *Controller
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		t:        t,
		registry: &fakeRegistry{entries: map[string]*adminv1alpha1.APIIdentity{}},
		local:    map[string]*apisv1alpha2.APIExport{},
		global:   map[string]*apisv1alpha2.APIExport{},
	}
	lookup := func(m map[string]*apisv1alpha2.APIExport) func(logicalcluster.Name, string) (*apisv1alpha2.APIExport, error) {
		return func(clusterName logicalcluster.Name, name string) (*apisv1alpha2.APIExport, error) {
			if export, ok := m[clusterName.String()+"|"+name]; ok {
				return export, nil
			}
			return nil, apierrors.NewNotFound(apisv1alpha2.Resource("apiexports"), name)
		}
	}
	f.c = &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: ControllerName},
		),
		registry: f.registry,
		getAPIIdentity: func(name string) (*adminv1alpha1.APIIdentity, error) {
			return f.registry.Get(context.Background(), name, metav1.GetOptions{})
		},
		getLocalAPIExport:  lookup(f.local),
		getGlobalAPIExport: lookup(f.global),
		requeueAfter:       func(_ string, delay time.Duration) { f.requeued = append(f.requeued, delay) },
		isCore:             func(*apisv1alpha2.APIExport) bool { return false },
	}
	return f
}

// host puts an export on this shard, which also replicates it to the cache.
func (f *fixture) host(export *apisv1alpha2.APIExport) {
	key := logicalcluster.From(export).String() + "|" + export.Name
	f.local[key] = export
	f.global[key] = export
}

// elsewhere puts an export on another shard: in the cache only.
func (f *fixture) elsewhere(export *apisv1alpha2.APIExport) {
	f.global[logicalcluster.From(export).String()+"|"+export.Name] = export
}

func export(cluster, name, uid string) *apisv1alpha2.APIExport {
	return &apisv1alpha2.APIExport{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			UID:         types.UID(uid),
			Annotations: map[string]string{logicalcluster.AnnotationKey: cluster},
		},
		Status: apisv1alpha2.APIExportStatus{IdentityHash: identity},
	}
}

// entry records owner and holders; holders were recorded long enough ago to
// be past the grace period, the oldest first.
func (f *fixture) entry(owner *apisv1alpha2.APIExport, holders ...*apisv1alpha2.APIExport) *adminv1alpha1.APIIdentity {
	e := &adminv1alpha1.APIIdentity{
		ObjectMeta: metav1.ObjectMeta{Name: identity},
		Status:     adminv1alpha1.APIIdentityStatus{Owner: ref(owner)},
	}
	for i, h := range holders {
		e.Status.Holders = append(e.Status.Holders, adminv1alpha1.Holder{
			ExportReference: ref(h),
			Since:           metav1.NewTime(time.Now().Add(-3*MissingGracePeriod + time.Duration(i)*time.Second)),
		})
	}
	if len(holders) > 0 {
		conditions.MarkTrue(e, adminv1alpha1.APIIdentityShared)
	}
	f.registry.entries[identity] = e
	return e
}

// ownerMissingFor marks the entry's owner missing since the given duration
// ago.
func (f *fixture) ownerMissingFor(d time.Duration) {
	since := time.Now().Add(-d)
	e := f.registry.entries[identity]
	conditions.MarkTrue(e, adminv1alpha1.APIIdentityOwnerMissing)
	for i := range e.Status.Conditions {
		if e.Status.Conditions[i].Type == adminv1alpha1.APIIdentityOwnerMissing {
			e.Status.Conditions[i].LastTransitionTime = metav1.NewTime(since)
		}
	}
}

func (f *fixture) reconcile() *adminv1alpha1.APIIdentity {
	f.t.Helper()
	require.NoError(f.t, f.c.reconcile(context.Background(), identity))
	entry, err := f.registry.Get(context.Background(), identity, metav1.GetOptions{})
	require.NoError(f.t, err)
	return entry
}

func TestHostedOwnerIsMaintained(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.c.isCore = func(*apisv1alpha2.APIExport) bool { return true }

	owner := export("provider", "wildwest", "owner-uid")
	holder := export("consumer", "wildwest", "holder-uid")
	f.host(owner)
	f.elsewhere(holder)
	e := f.entry(owner, holder)
	conditions.MarkTrue(e, adminv1alpha1.APIIdentityOrphaned)
	conditions.MarkTrue(e, adminv1alpha1.APIIdentityOwnerMissing)

	entry := f.reconcile()
	require.True(t, entry.Status.Core)
	require.Len(t, entry.Status.Holders, 1)
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityShared))
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityOrphaned), "an owner in the cache is not orphaned")
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing))
	require.Empty(t, f.requeued)
}

func TestOwnerHostedElsewhereOnlyClearsOwnerMissing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	dead := export("consumer", "wildwest", "holder-uid")
	f.elsewhere(owner)
	e := f.entry(owner, dead) // the holder is not in the cache any more
	conditions.MarkTrue(e, adminv1alpha1.APIIdentityOwnerMissing)

	entry := f.reconcile()
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing), "the owner is back")
	require.Len(t, entry.Status.Holders, 1, "only the owner's shard maintains the holders")
}

func TestOwnerMissingStartsTheGracePeriod(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	holder := export("consumer", "wildwest", "holder-uid")
	f.host(holder)
	f.entry(owner, holder) // the owner is not in the cache: not replicated yet, or deleted

	entry := f.reconcile()
	require.Equal(t, ref(owner), entry.Status.Owner, "ownership must not move before the grace period")
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityOwnerMissing))
	require.Len(t, entry.Status.Holders, 1)
	require.Equal(t, []time.Duration{MissingGracePeriod}, f.requeued)

	// still within the grace period: wait for the rest of it
	f.ownerMissingFor(MissingGracePeriod / 4)
	entry = f.reconcile()
	require.Equal(t, ref(owner), entry.Status.Owner)
	require.Len(t, f.requeued, 2)
	require.InDelta(t, 3*MissingGracePeriod/4, f.requeued[1], float64(time.Second), "requeued for the rest of the grace period")
}

func TestGoneOwnerPassesToOldestHolderHostedHere(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	oldest := export("consumer-a", "wildwest", "a-uid")
	newer := export("consumer-b", "wildwest", "b-uid")
	f.host(oldest)
	f.elsewhere(newer)
	f.entry(owner, oldest, newer)
	f.ownerMissingFor(MissingGracePeriod)

	entry := f.reconcile()
	require.Equal(t, ref(oldest), entry.Status.Owner, "the identity passes to the oldest holder")
	require.Len(t, entry.Status.Holders, 1)
	require.Equal(t, ref(newer), entry.Status.Holders[0].ExportReference)
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityShared))
	require.False(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityOrphaned))
	require.Equal(t, adminv1alpha1.APIIdentityOwnerSucceededReason, conditions.GetReason(entry, adminv1alpha1.APIIdentityOrphaned))
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing))
	require.Empty(t, f.requeued)
}

func TestGoneOwnerSkipsHoldersNotInTheCache(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	oldestButGone := export("consumer-a", "wildwest", "a-uid")
	alive := export("consumer-b", "wildwest", "b-uid")
	f.host(alive)
	f.entry(owner, oldestButGone, alive)
	f.ownerMissingFor(MissingGracePeriod)

	entry := f.reconcile()
	require.Equal(t, ref(alive), entry.Status.Owner, "a holder that is not in the cache cannot succeed")
}

func TestSuccessorHostedElsewhereIsLeftToItsShard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	remote := export("consumer-a", "wildwest", "a-uid")
	f.elsewhere(remote)
	f.entry(owner, remote)
	f.ownerMissingFor(MissingGracePeriod)

	entry := f.reconcile()
	require.Equal(t, ref(owner), entry.Status.Owner, "the successor's shard performs the succession")
	require.Len(t, entry.Status.Holders, 1)
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityOwnerMissing))
}

func TestGoneOwnerWithoutHoldersIsOrphaned(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	dead := export("consumer", "wildwest", "holder-uid")
	f.entry(owner, dead) // neither is in the cache
	f.ownerMissingFor(MissingGracePeriod)

	entry := f.reconcile()
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityOrphaned))
	require.Empty(t, entry.Status.Holders)
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityShared))
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing))

	// an orphaned entry is left alone until somebody reclaims it
	entry = f.reconcile()
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityOrphaned))
	require.Empty(t, f.requeued)
}

func TestRecreatedOwnerIsNotGone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "old-uid")
	holder := export("consumer", "wildwest", "holder-uid")
	f.host(holder)
	f.entry(owner, holder)
	f.ownerMissingFor(MissingGracePeriod)

	// the owner was recreated under its name and the registry already
	// records the new UID; its copy is back in the cache
	recreated := export("provider", "wildwest", "new-uid")
	f.elsewhere(recreated)
	f.registry.entries[identity].Status.Owner.UID = "new-uid"

	entry := f.reconcile()
	require.Equal(t, "new-uid", entry.Status.Owner.UID)
	require.Len(t, entry.Status.Holders, 1)
	require.Nil(t, conditions.Get(entry, adminv1alpha1.APIIdentityOwnerMissing))
	require.False(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityOrphaned))
}

func TestMaintainDropsHoldersGoneFromTheCache(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	owner := export("provider", "wildwest", "owner-uid")
	dead := export("provider", "cowboys", "dead-uid")
	alive := export("elsewhere", "wildwest", "alive-uid")
	young := export("newcomer", "wildwest", "young-uid")
	f.host(owner)
	f.elsewhere(alive)
	e := f.entry(owner, dead, alive, young)
	e.Status.Holders[2].Since = metav1.NewTime(time.Now()) // just recorded, copy not replicated yet

	entry := f.reconcile()
	require.Len(t, entry.Status.Holders, 2)
	require.Equal(t, ref(alive), entry.Status.Holders[0].ExportReference)
	require.Equal(t, ref(young), entry.Status.Holders[1].ExportReference, "a holder within its grace period is kept")
	require.True(t, conditions.IsTrue(entry, adminv1alpha1.APIIdentityShared))

	f.registry.entries[identity].Status.Holders[1].Since = metav1.NewTime(time.Now().Add(-MissingGracePeriod))
	entry = f.reconcile()
	require.Len(t, entry.Status.Holders, 1, "past the grace period a holder missing from the cache is dropped")
	require.Equal(t, ref(alive), entry.Status.Holders[0].ExportReference)
}
