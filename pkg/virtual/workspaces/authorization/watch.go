/*
Copyright 2022 The KCP Authors.

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
	"errors"
	"sync"

	"github.com/kcp-dev/logicalcluster/v2"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/apiserver/pkg/authentication/user"
	kstorage "k8s.io/apiserver/pkg/storage"
	"k8s.io/klog/v2"

	"github.com/kcp-dev/kcp/pkg/apis/tenancy/projection"
	workspaceapi "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	workspaceapibeta1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1beta1"
	workspacecache "github.com/kcp-dev/kcp/pkg/virtual/workspaces/cache"
)

type CacheWatcher interface {
	// GroupMembershipChanged is called serially for all changes for all watchers.  This method MUST NOT BLOCK.
	// The serial nature makes reasoning about the code easy, but if you block in this method you will doom all watchers.
	GroupMembershipChanged(workspaceName string, users, groups sets.String)
}

type WatchableCache interface {
	// RemoveWatcher removes a watcher
	RemoveWatcher(CacheWatcher)
	// List returns the set of workspace names the user has access to view
	List(userInfo user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*workspaceapi.ClusterWorkspaceList, error)
}

// userWorkspaceWatcher converts notifications received from the WorkspaceAuthCache to
// watch events sent through a watch.Interface.
type userWorkspaceWatcher struct {
	user user.Info

	// cacheIncoming is a buffered channel used for notification to watcher.  If the buffer fills up,
	// then the watcher will be removed and the connection will be broken.
	cacheIncoming chan watch.Event
	// cacheError is a cached channel that is put to serially.  In theory, only one item will
	// ever be placed on it.
	cacheError chan error

	// outgoing is the unbuffered `ResultChan` use for the watch.  Backups of this channel will block
	// the default `emit` call.  That's why cacheError is a buffered channel.
	outgoing chan watch.Event
	// userStop lets a user stop his watch.
	userStop chan struct{}

	// stopLock keeps parallel stops from doing crazy things
	stopLock sync.Mutex

	// Injectable for testing. Send the event down the outgoing channel.
	emit func(watch.Event)

	clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache
	authCache             WatchableCache

	initialClusterWorkspaces []workspaceapi.ClusterWorkspace
	// knownWorkspaces maps name to resourceVersion
	knownWorkspaces map[string]string

	lclusterName logicalcluster.Name
}

var (
	// watchChannelHWM tracks how backed up the most backed up channel got.  This mirrors etcd watch behavior and allows tuning
	// of channel depth.
	watchChannelHWM kstorage.HighWaterMark
)

func NewUserWorkspaceWatcher(user user.Info, lclusterName logicalcluster.Name, clusterWorkspaceCache *workspacecache.ClusterWorkspaceCache, authCache WatchableCache, includeAllExistingWorkspaces bool, predicate kstorage.SelectionPredicate) *userWorkspaceWatcher {
	workspaces, _ := authCache.List(user, labels.Everything(), fields.Everything())
	knownWorkspaces := map[string]string{}
	for _, workspace := range workspaces.Items {
		knownWorkspaces[workspace.Name] = workspace.ResourceVersion
	}

	// this is optional.  If they don't request it, don't include it.
	initialWorkspaces := []workspaceapi.ClusterWorkspace{}
	if includeAllExistingWorkspaces {
		initialWorkspaces = append(initialWorkspaces, workspaces.Items...)
	}

	w := &userWorkspaceWatcher{
		user: user,

		cacheIncoming: make(chan watch.Event, 1000),
		cacheError:    make(chan error, 1),
		outgoing:      make(chan watch.Event),
		userStop:      make(chan struct{}),

		clusterWorkspaceCache:    clusterWorkspaceCache,
		authCache:                authCache,
		initialClusterWorkspaces: initialWorkspaces,
		knownWorkspaces:          knownWorkspaces,

		lclusterName: lclusterName,
	}
	w.emit = func(e watch.Event) {
		// if dealing with workspace events, ensure that we only emit events for workspaces
		// that match the field or label selector specified by a consumer
		if workspace, ok := e.Object.(*workspaceapibeta1.Workspace); ok {
			if matches, err := predicate.Matches(workspace); err != nil || !matches {
				return
			}
		}

		select {
		case w.outgoing <- e:
		case <-w.userStop:
		}
	}
	return w
}

func (w *userWorkspaceWatcher) GroupMembershipChanged(workspaceName string, users, groups sets.String) {
	hasAccess := users.Has(w.user.GetName()) || groups.HasAny(w.user.GetGroups()...)
	_, known := w.knownWorkspaces[workspaceName]

	var workspace workspaceapibeta1.Workspace
	projection.ProjectClusterWorkspaceToWorkspace(&workspaceapi.ClusterWorkspace{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				logicalcluster.AnnotationKey: w.lclusterName.String(),
			},
			Name: workspaceName,
		},
	}, &workspace)

	switch {
	// this means that we were removed from the workspace
	case !hasAccess && known:
		delete(w.knownWorkspaces, workspaceName)

		select {
		case w.cacheIncoming <- watch.Event{
			Type:   watch.Deleted,
			Object: &workspace,
		}:
		default:
			// remove the watcher so that we wont' be notified again and block
			w.authCache.RemoveWatcher(w)
			w.cacheError <- errors.New("delete notification timeout")
		}

	case hasAccess:
		clusterWorkspace, err := w.clusterWorkspaceCache.Get(w.lclusterName, workspaceName)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		var workspace workspaceapibeta1.Workspace
		projection.ProjectClusterWorkspaceToWorkspace(clusterWorkspace, &workspace)
		if err != nil {
			utilruntime.HandleError(err)
			return
		}
		event := watch.Event{
			Type:   watch.Added,
			Object: &workspace,
		}

		// if we already have this in our list, then we're getting notified because the object changed
		if lastResourceVersion, known := w.knownWorkspaces[workspaceName]; known {
			event.Type = watch.Modified

			// if we've already notified for this particular resourceVersion, there's no work to do
			if lastResourceVersion == clusterWorkspace.ResourceVersion {
				return
			}
		}
		w.knownWorkspaces[workspaceName] = clusterWorkspace.ResourceVersion

		select {
		case w.cacheIncoming <- event:
		default:
			// remove the watcher so that we won't be notified again and block
			w.authCache.RemoveWatcher(w)
			w.cacheError <- errors.New("add notification timeout")
		}

	}

}

// Watch pulls stuff from etcd, converts, and pushes out the outgoing channel. Meant to be
// called as a goroutine.
func (w *userWorkspaceWatcher) Watch() {
	defer close(w.outgoing)
	defer func() {
		// when the watch ends, always remove the watcher from the cache to avoid leaking.
		w.authCache.RemoveWatcher(w)
	}()
	defer utilruntime.HandleCrash()

	// start by emitting all the `initialWorkspaces`
	for i := range w.initialClusterWorkspaces {
		// keep this check here to sure we don't keep this open in the case of failures
		select {
		case err := <-w.cacheError:
			w.emit(makeErrorEvent(err))
			return
		default:
		}
		var workspace workspaceapibeta1.Workspace
		projection.ProjectClusterWorkspaceToWorkspace(&w.initialClusterWorkspaces[i], &workspace)
		w.emit(watch.Event{
			Type:   watch.Added,
			Object: &workspace,
		})
	}

	for {
		select {
		case err := <-w.cacheError:
			w.emit(makeErrorEvent(err))
			return

		case <-w.userStop:
			return

		case event := <-w.cacheIncoming:
			if curLen := int64(len(w.cacheIncoming)); watchChannelHWM.Update(curLen) {
				// Monitor if this gets backed up, and how much.
				klog.V(2).Infof("watch: %v objects queued in workspace cache watching channel.", curLen)
			}

			w.emit(event)
		}
	}
}

func makeErrorEvent(err error) watch.Event {
	return watch.Event{
		Type: watch.Error,
		Object: &metav1.Status{
			Status:  metav1.StatusFailure,
			Message: err.Error(),
		},
	}
}

// ResultChan implements watch.Interface.
func (w *userWorkspaceWatcher) ResultChan() <-chan watch.Event {
	return w.outgoing
}

// Stop implements watch.Interface.
func (w *userWorkspaceWatcher) Stop() {
	// lock access so we don't race past the channel select
	w.stopLock.Lock()
	defer w.stopLock.Unlock()

	// Prevent double channel closes.
	select {
	case <-w.userStop:
		return
	default:
	}
	close(w.userStop)
}
