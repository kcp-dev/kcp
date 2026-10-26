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

// Package apiidentity derives the APIIdentity view from APIExports. An
// identity is held by every APIExport whose status carries its hash; the
// view names the owner and the other holders. Nothing is stored: the view
// is computed from the APIExports replicated to the cache server, which are
// ordinary per-shard data, so it is the same on every shard and needs no
// coordination between them.
package apiidentity

import (
	"sort"
	"strconv"
	"sync"

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"

	"github.com/kcp-dev/logicalcluster/v3"
	adminv1alpha1 "github.com/kcp-dev/sdk/apis/admin/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/sdk/apis/apis/v1alpha2"
	"github.com/kcp-dev/sdk/apis/core"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"

	"github.com/kcp-dev/kcp/pkg/server/bootstrap"
	"github.com/kcp-dev/kcp/pkg/tombstone"
)

// IsCoreExport reports whether the export is one of the core APIExports in
// the root workspace, whose identities every shard needs before it can serve.
func IsCoreExport(export *apisv1alpha2.APIExport) bool {
	return logicalcluster.From(export) == core.RootCluster && coreExportNames.Has(export.Name)
}

var coreExportNames = func() sets.Set[string] {
	names := sets.New[string]()
	for _, name := range bootstrap.KcpRootGroupExportNames {
		names.Insert(name)
	}
	for _, name := range bootstrap.KcpRootGroupResourceExportNames {
		names.Insert(name)
	}
	return names
}()

// Project computes the view of one identity from the exports holding it.
// The owner is the oldest export, by creation time, then cluster, then name,
// so every reader of the same exports agrees on it; the others are holders.
// It returns nil when no export holds the identity.
func Project(hash string, exports []*apisv1alpha2.APIExport, isCore func(*apisv1alpha2.APIExport) bool) *adminv1alpha1.APIIdentity {
	if len(exports) == 0 {
		return nil
	}
	sorted := append([]*apisv1alpha2.APIExport(nil), exports...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if !sorted[i].CreationTimestamp.Equal(&sorted[j].CreationTimestamp) {
			return sorted[i].CreationTimestamp.Before(&sorted[j].CreationTimestamp)
		}
		if ci, cj := logicalcluster.From(sorted[i]), logicalcluster.From(sorted[j]); ci != cj {
			return ci < cj
		}
		return sorted[i].Name < sorted[j].Name
	})

	owner := sorted[0]
	ownerRef := reference(owner)
	core := isCore != nil && isCore(owner)
	entry := &adminv1alpha1.APIIdentity{
		TypeMeta: metav1.TypeMeta{APIVersion: adminv1alpha1.SchemeGroupVersion.String(), Kind: "APIIdentity"},
		ObjectMeta: metav1.ObjectMeta{
			Name:              hash,
			CreationTimestamp: owner.CreationTimestamp,
			Labels:            adminv1alpha1.Labels(ownerRef, core),
		},
		Status: adminv1alpha1.APIIdentityStatus{Owner: ownerRef, Core: core},
	}
	for _, holder := range sorted[1:] {
		entry.Status.Holders = append(entry.Status.Holders, adminv1alpha1.Holder{ExportReference: reference(holder), Since: holder.CreationTimestamp})
	}
	if len(entry.Status.Holders) > 0 {
		conditions.MarkTrue(entry, adminv1alpha1.APIIdentityShared)
	}
	return entry
}

func reference(export *apisv1alpha2.APIExport) adminv1alpha1.ExportReference {
	return adminv1alpha1.ExportReference{Cluster: logicalcluster.From(export).String(), Name: export.Name, UID: string(export.UID)}
}

// Projector keeps the view of every identity current from APIExport events
// and serves it: by name, as a list, as a watch, and to event handlers. It
// is an event handler for an APIExport informer, normally the one against
// the cache server. Resource versions are its own, counting the changes of
// the view; a watch resuming from one it no longer remembers is told to
// relist, like a watch on an apiserver.
type Projector struct {
	isCore func(*apisv1alpha2.APIExport) bool

	lock     sync.RWMutex
	exports  map[string]map[string]*apisv1alpha2.APIExport // identity hash -> cluster|name -> export
	entries  map[string]*adminv1alpha1.APIIdentity         // identity hash -> current view
	rv       uint64
	history  []event
	watchers map[*watcher]struct{}
	handlers []cache.ResourceEventHandler
}

// historyLength bounds the events kept for watches resuming from a
// resource version.
const historyLength = 1024

type event struct {
	rv  uint64
	typ watch.EventType
	obj *adminv1alpha1.APIIdentity
}

// NewProjector creates a projector. isCore tells which exports are the core
// ones of the installation; IsCoreExport is the usual choice.
func NewProjector(isCore func(*apisv1alpha2.APIExport) bool) *Projector {
	return &Projector{
		isCore:   isCore,
		exports:  map[string]map[string]*apisv1alpha2.APIExport{},
		entries:  map[string]*adminv1alpha1.APIIdentity{},
		watchers: map[*watcher]struct{}{},
	}
}

var _ cache.ResourceEventHandler = &Projector{}

func (p *Projector) OnAdd(obj any, _ bool) {
	if export, ok := obj.(*apisv1alpha2.APIExport); ok {
		p.apply(nil, export)
	}
}

func (p *Projector) OnUpdate(oldObj, newObj any) {
	oldExport, _ := oldObj.(*apisv1alpha2.APIExport)
	if newExport, ok := newObj.(*apisv1alpha2.APIExport); ok {
		p.apply(oldExport, newExport)
	}
}

func (p *Projector) OnDelete(obj any) {
	if export := tombstone.Obj[*apisv1alpha2.APIExport](obj); export != nil {
		p.apply(export, nil)
	}
}

func exportKey(export *apisv1alpha2.APIExport) string {
	return logicalcluster.From(export).String() + "|" + export.Name
}

// apply records that old became new (either may be nil) and re-projects the
// identities affected.
func (p *Projector) apply(oldExport, newExport *apisv1alpha2.APIExport) {
	p.lock.Lock()
	defer p.lock.Unlock()

	affected := sets.New[string]()
	if oldExport != nil && oldExport.Status.IdentityHash != "" {
		hash := oldExport.Status.IdentityHash
		delete(p.exports[hash], exportKey(oldExport))
		if len(p.exports[hash]) == 0 {
			delete(p.exports, hash)
		}
		affected.Insert(hash)
	}
	if newExport != nil && newExport.Status.IdentityHash != "" {
		hash := newExport.Status.IdentityHash
		if p.exports[hash] == nil {
			p.exports[hash] = map[string]*apisv1alpha2.APIExport{}
		}
		p.exports[hash][exportKey(newExport)] = newExport
		affected.Insert(hash)
	}
	for _, hash := range sets.List(affected) {
		p.reproject(hash)
	}
}

// reproject recomputes one identity's view and emits the change, if any.
// Called with the lock held.
func (p *Projector) reproject(hash string) {
	exports := make([]*apisv1alpha2.APIExport, 0, len(p.exports[hash]))
	for _, export := range p.exports[hash] {
		exports = append(exports, export)
	}
	next := Project(hash, exports, p.isCore)
	prev := p.entries[hash]

	switch {
	case prev == nil && next == nil:
		return
	case next == nil:
		delete(p.entries, hash)
		p.emit(watch.Deleted, prev)
	case prev == nil:
		p.entries[hash] = next
		p.emit(watch.Added, next)
	default:
		carryOverConditionTimes(prev, next)
		next.ResourceVersion = prev.ResourceVersion
		if equality.Semantic.DeepEqual(prev, next) {
			return
		}
		p.entries[hash] = next
		p.emit(watch.Modified, next)
	}
}

// carryOverConditionTimes keeps the transition time of a condition that did
// not change status, so that a recomputation is not a change.
func carryOverConditionTimes(prev, next *adminv1alpha1.APIIdentity) {
	for i := range next.Status.Conditions {
		if old := conditions.Get(prev, next.Status.Conditions[i].Type); old != nil && old.Status == next.Status.Conditions[i].Status {
			next.Status.Conditions[i].LastTransitionTime = old.LastTransitionTime
		}
	}
}

// emit stamps the next resource version on the object and delivers the
// event to the watchers and handlers. Called with the lock held.
func (p *Projector) emit(typ watch.EventType, obj *adminv1alpha1.APIIdentity) {
	p.rv++
	obj.ResourceVersion = strconv.FormatUint(p.rv, 10)
	ev := event{rv: p.rv, typ: typ, obj: obj}

	p.history = append(p.history, ev)
	if len(p.history) > historyLength {
		p.history = p.history[len(p.history)-historyLength:]
	}
	for w := range p.watchers {
		if !w.deliver(ev) {
			delete(p.watchers, w)
		}
	}
	for _, handler := range p.handlers {
		switch typ {
		case watch.Added:
			handler.OnAdd(obj.DeepCopy(), false)
		case watch.Modified:
			handler.OnUpdate(nil, obj.DeepCopy())
		case watch.Deleted:
			handler.OnDelete(obj.DeepCopy())
		}
	}
}

// ResourceVersion is the version of the view: the number of changes so far.
func (p *Projector) ResourceVersion() string {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return strconv.FormatUint(p.rv, 10)
}

// Get returns the view of the named identity.
func (p *Projector) Get(name string) (*adminv1alpha1.APIIdentity, bool) {
	p.lock.RLock()
	defer p.lock.RUnlock()
	entry, ok := p.entries[name]
	if !ok {
		return nil, false
	}
	return entry.DeepCopy(), true
}

// List returns the views matching the selector, by name.
func (p *Projector) List(selector labels.Selector) []*adminv1alpha1.APIIdentity {
	if selector == nil {
		selector = labels.Everything()
	}
	p.lock.RLock()
	defer p.lock.RUnlock()
	var result []*adminv1alpha1.APIIdentity
	for _, entry := range p.entries {
		if selector.Matches(labels.Set(entry.Labels)) {
			result = append(result, entry.DeepCopy())
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// AddEventHandler registers a handler for the changes of the view. It gets
// the current entries first, as initial adds. Handlers run with the
// projector's lock held and must not call back into it.
func (p *Projector) AddEventHandler(handler cache.ResourceEventHandler) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.handlers = append(p.handlers, handler)
	for _, entry := range p.entries {
		handler.OnAdd(entry.DeepCopy(), true)
	}
}

// WatchOptions are the watch semantics a client asks for.
type WatchOptions struct {
	// ResourceVersion is where to start: "" for now, "0" for the current
	// entries followed by changes, otherwise the changes after that version.
	ResourceVersion string
	// SendInitialEvents asks for the current entries first, closed by a
	// bookmark when AllowWatchBookmarks is set, as a watch-list client does.
	SendInitialEvents   bool
	AllowWatchBookmarks bool
}

// Watch streams the changes of the entries matching the selector.
func (p *Projector) Watch(selector labels.Selector, opts WatchOptions) (watch.Interface, error) {
	if selector == nil {
		selector = labels.Everything()
	}
	w := &watcher{projector: p, selector: selector, ch: make(chan watch.Event, watchBuffer)}

	p.lock.Lock()
	defer p.lock.Unlock()

	switch {
	case opts.SendInitialEvents || opts.ResourceVersion == "0":
		for _, entry := range p.sortedEntries() {
			w.deliver(event{rv: p.rv, typ: watch.Added, obj: entry})
		}
		if opts.SendInitialEvents && opts.AllowWatchBookmarks {
			w.deliver(event{rv: p.rv, typ: watch.Bookmark, obj: &adminv1alpha1.APIIdentity{
				TypeMeta:   metav1.TypeMeta{APIVersion: adminv1alpha1.SchemeGroupVersion.String(), Kind: "APIIdentity"},
				ObjectMeta: metav1.ObjectMeta{ResourceVersion: strconv.FormatUint(p.rv, 10), Annotations: map[string]string{metav1.InitialEventsAnnotationKey: "true"}},
			}})
		}
	case opts.ResourceVersion == "":
		// from now on
	default:
		from, err := strconv.ParseUint(opts.ResourceVersion, 10, 64)
		if err != nil {
			return nil, apierrors.NewBadRequest("invalid resourceVersion " + opts.ResourceVersion)
		}
		if from > p.rv || (from < p.rv && (len(p.history) == 0 || p.history[0].rv > from+1)) {
			// too old, or unknown: the client relists
			w.ch <- watch.Event{Type: watch.Error, Object: &apierrors.NewResourceExpired("too old resource version: " + opts.ResourceVersion).ErrStatus}
			close(w.ch)
			w.closed = true
			return w, nil
		}
		for _, ev := range p.history {
			if ev.rv > from {
				w.deliver(ev)
			}
		}
	}
	p.watchers[w] = struct{}{}
	return w, nil
}

func (p *Projector) sortedEntries() []*adminv1alpha1.APIIdentity {
	entries := make([]*adminv1alpha1.APIIdentity, 0, len(p.entries))
	for _, entry := range p.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (p *Projector) removeWatcher(w *watcher) {
	p.lock.Lock()
	defer p.lock.Unlock()
	if _, ok := p.watchers[w]; ok {
		delete(p.watchers, w)
		w.close()
	}
}

// watchBuffer is how many events a watcher may fall behind before it is
// closed, which makes the client relist.
const watchBuffer = 1000

type watcher struct {
	projector *Projector
	selector  labels.Selector
	ch        chan watch.Event
	closed    bool // guarded by the projector's lock
}

// deliver queues the event for the watcher. It reports false when the
// watcher could not keep up and was closed. Called with the lock held.
func (w *watcher) deliver(ev event) bool {
	if w.closed {
		return false
	}
	if ev.typ != watch.Bookmark && !w.selector.Matches(labels.Set(ev.obj.Labels)) {
		return true
	}
	select {
	case w.ch <- watch.Event{Type: ev.typ, Object: ev.obj.DeepCopy()}:
		return true
	default:
		w.close()
		return false
	}
}

// close ends the stream. Called with the lock held.
func (w *watcher) close() {
	if !w.closed {
		w.closed = true
		close(w.ch)
	}
}

func (w *watcher) Stop() {
	w.projector.removeWatcher(w)
}

func (w *watcher) ResultChan() <-chan watch.Event {
	return w.ch
}
