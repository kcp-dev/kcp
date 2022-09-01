/*
Copyright 2021 The KCP Authors.

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
	"fmt"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilwait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	rbacinformers "k8s.io/client-go/informers/rbac/v1"
	rbaclisters "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clusters"
	"k8s.io/klog/v2"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	tenancylisters "github.com/kcp-dev/kcp/pkg/client/listers/tenancy/v1alpha1"
	workspaceutil "github.com/kcp-dev/kcp/pkg/virtual/workspaces/util"
)

// Lister enforces ability to enumerate a resource based on role
type Lister interface {
	// List returns the list of ClusterWorkspace items that the user can access
	List(user user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error)
}

// subjectRecord is a cache record for the set of workspaces a subject can access
type subjectRecord struct {
	subject    string
	workspaces sets.String
}

// reviewRequest is the resource we want to review
type reviewRequest struct {
	workspace string
	// the resource version of the workspace that was observed to make this request
	workspaceResourceVersion string
	// the map of role uid to resource version that was observed to make this request
	roleUIDToResourceVersion map[types.UID]string
	// the map of role binding uid to resource version that was observed to make this request
	roleBindingUIDToResourceVersion map[types.UID]string
}

// reviewRecord is a cache record for the result of a resource access review
type reviewRecord struct {
	*reviewRequest
	users  []string
	groups []string
}

// reviewRecordKeyFn is a key func for reviewRecord objects
func reviewRecordKeyFn(obj interface{}) (string, error) {
	reviewRecord, ok := obj.(*reviewRecord)
	if !ok {
		return "", fmt.Errorf("expected reviewRecord")
	}
	return reviewRecord.workspace, nil
}

// subjectRecordKeyFn is a key func for subjectRecord objects
func subjectRecordKeyFn(obj interface{}) (string, error) {
	subjectRecord, ok := obj.(*subjectRecord)
	if !ok {
		return "", fmt.Errorf("expected subjectRecord")
	}
	return subjectRecord.subject, nil
}

type skipSynchronizer interface {
	// SkipSynchronize returns true if if its safe to skip synchronization of the cache based on provided token from previous observation
	SkipSynchronize(prevState string, versionedObjects ...LastSyncResourceVersioner) (skip bool, currentState string)
}

// LastSyncResourceVersioner is any object that can divulge a LastSyncResourceVersion
type LastSyncResourceVersioner interface {
	LastSyncResourceVersion() string
}

type unionLastSyncResourceVersioner []LastSyncResourceVersioner

func (u unionLastSyncResourceVersioner) LastSyncResourceVersion() string {
	resourceVersions := []string{}
	for _, versioner := range u {
		resourceVersions = append(resourceVersions, versioner.LastSyncResourceVersion())
	}
	return strings.Join(resourceVersions, "")
}

type statelessSkipSynchronizer struct{}

func (rs *statelessSkipSynchronizer) SkipSynchronize(prevState string, versionedObjects ...LastSyncResourceVersioner) (skip bool, currentState string) {
	resourceVersions := []string{}
	for i := range versionedObjects {
		resourceVersions = append(resourceVersions, versionedObjects[i].LastSyncResourceVersion())
	}
	currentState = strings.Join(resourceVersions, ",")
	skip = currentState == prevState

	return skip, currentState
}

type neverSkipSynchronizer struct{}

func (s *neverSkipSynchronizer) SkipSynchronize(prevState string, versionedObjects ...LastSyncResourceVersioner) (bool, string) {
	return false, ""
}

type SyncedClusterRoleLister interface {
	rbaclisters.ClusterRoleLister
	LastSyncResourceVersioner
}

type SyncedClusterRoleBindingLister interface {
	rbaclisters.ClusterRoleBindingLister
	LastSyncResourceVersioner
}

type syncedClusterRoleLister struct {
	rbaclisters.ClusterRoleLister
	versioner LastSyncResourceVersioner
}

func (l syncedClusterRoleLister) LastSyncResourceVersion() string {
	return l.versioner.LastSyncResourceVersion()
}

type syncedClusterRoleBindingLister struct {
	rbaclisters.ClusterRoleBindingLister
	versioner LastSyncResourceVersioner
}

func (l syncedClusterRoleBindingLister) LastSyncResourceVersion() string {
	return l.versioner.LastSyncResourceVersion()
}

// AuthorizationCache maintains a cache on the set of workspaces a user or group can access.
type AuthorizationCache struct {
	// allKnownWorkspaces we track all the known workspaces, so we can detect deletes.
	// TODO remove this in favor of a list/watch mechanism for workspaces
	allKnownWorkspaces        sets.String
	workspaceLister           tenancylisters.ClusterWorkspaceLister
	lastSyncResourceVersioner LastSyncResourceVersioner

	clusterRoleLister             SyncedClusterRoleLister
	clusterRoleBindingLister      SyncedClusterRoleBindingLister
	roleLastSyncResourceVersioner LastSyncResourceVersioner

	reviewRecordStore       cache.Store
	userSubjectRecordStore  cache.Store
	groupSubjectRecordStore cache.Store

	clusterBindingResourceVersions sets.String
	clusterRoleResourceVersions    sets.String

	skip      skipSynchronizer
	lastState string

	reviewTemplate authorizer.AttributesRecord
	reviewer       *Reviewer

	syncHandler func(request *reviewRequest, userSubjectRecordStore cache.Store, groupSubjectRecordStore cache.Store, reviewRecordStore cache.Store) error

	rwMutex sync.RWMutex

	watcherLock sync.Mutex
	watchers    []CacheWatcher
}

// NewAuthorizationCache creates a new AuthorizationCache
func NewAuthorizationCache(
	workspaceLister tenancylisters.ClusterWorkspaceLister,
	workspaceLastSyncResourceVersioner LastSyncResourceVersioner,
	reviewer *Reviewer,
	reviewTemplate authorizer.AttributesRecord,
	informers rbacinformers.Interface,
) *AuthorizationCache {
	scrLister := syncedClusterRoleLister{
		informers.ClusterRoles().Lister(),
		informers.ClusterRoles().Informer(),
	}
	scrbLister := syncedClusterRoleBindingLister{
		informers.ClusterRoleBindings().Lister(),
		informers.ClusterRoleBindings().Informer(),
	}
	ac := AuthorizationCache{
		allKnownWorkspaces: sets.String{},
		workspaceLister:    workspaceLister,

		clusterRoleResourceVersions:    sets.NewString(),
		clusterBindingResourceVersions: sets.NewString(),

		clusterRoleLister:             scrLister,
		clusterRoleBindingLister:      scrbLister,
		roleLastSyncResourceVersioner: unionLastSyncResourceVersioner{scrLister, scrbLister},

		reviewRecordStore:       cache.NewStore(reviewRecordKeyFn),
		userSubjectRecordStore:  cache.NewStore(subjectRecordKeyFn),
		groupSubjectRecordStore: cache.NewStore(subjectRecordKeyFn),

		reviewer:       reviewer,
		reviewTemplate: reviewTemplate,
		skip:           &neverSkipSynchronizer{},

		watchers: []CacheWatcher{},
	}
	ac.lastSyncResourceVersioner = workspaceLastSyncResourceVersioner
	ac.syncHandler = ac.syncRequest
	ac.rwMutex = sync.RWMutex{}
	return &ac
}

// Run begins watching and synchronizing the cache
func (ac *AuthorizationCache) Run(period time.Duration, stopCh <-chan struct{}) {
	ac.skip = &statelessSkipSynchronizer{}
	go utilwait.Until(func() { ac.synchronize() }, period, stopCh)
}

func (ac *AuthorizationCache) AddWatcher(watcher CacheWatcher) {
	ac.watcherLock.Lock()
	defer ac.watcherLock.Unlock()

	ac.watchers = append(ac.watchers, watcher)
}

func (ac *AuthorizationCache) RemoveWatcher(watcher CacheWatcher) {
	ac.watcherLock.Lock()
	defer ac.watcherLock.Unlock()

	lastIndex := len(ac.watchers) - 1
	for i := 0; i < len(ac.watchers); i++ {
		if ac.watchers[i] == watcher {
			if i < lastIndex {
				// if we're not the last element, shift
				copy(ac.watchers[i:], ac.watchers[i+1:])
			}
			ac.watchers = ac.watchers[:lastIndex]
			break
		}
	}
}

func (ac *AuthorizationCache) GetClusterRoleLister() SyncedClusterRoleLister {
	return ac.clusterRoleLister
}

// synchronizeWorkspaces synchronizes access over each workspace and returns a set of workspace names that were looked at in last sync
func (ac *AuthorizationCache) synchronizeWorkspaces(userSubjectRecordStore cache.Store, groupSubjectRecordStore cache.Store, reviewRecordStore cache.Store) sets.String {
	workspaceSet := sets.NewString()
	workspaces, err := ac.workspaceLister.List(labels.Everything())
	if err != nil {
		// should never happen
		panic(err)
	}
	for i := range workspaces {
		workspace := workspaces[i]
		workspaceKey, err := cache.MetaNamespaceKeyFunc(workspace)
		if err != nil {
			klog.Warning(err)
		}
		workspaceSet.Insert(workspaceKey)
		reviewRequest := &reviewRequest{
			workspace:                workspaceKey,
			workspaceResourceVersion: workspace.ResourceVersion,
		}
		if err := ac.syncHandler(reviewRequest, userSubjectRecordStore, groupSubjectRecordStore, reviewRecordStore); err != nil {
			utilruntime.HandleError(fmt.Errorf("error synchronizing: %w", err))
		}
	}
	return workspaceSet
}

// purgeDeletedWorkspaces will remove all workspaces enumerated in a reviewRecordStore that are not in the workspace set
func (ac *AuthorizationCache) purgeDeletedWorkspaces(oldWorkspaces, newWorkspaces sets.String, userSubjectRecordStore cache.Store, groupSubjectRecordStore cache.Store, reviewRecordStore cache.Store) {
	reviewRecordItems := reviewRecordStore.List()
	for i := range reviewRecordItems {
		reviewRecord := reviewRecordItems[i].(*reviewRecord)
		if !newWorkspaces.Has(reviewRecord.workspace) {
			deleteWorkspaceFromSubjects(userSubjectRecordStore, reviewRecord.users, reviewRecord.workspace)
			deleteWorkspaceFromSubjects(groupSubjectRecordStore, reviewRecord.groups, reviewRecord.workspace)
			_ = reviewRecordStore.Delete(reviewRecord)
		}
	}

	for workspace := range oldWorkspaces.Difference(newWorkspaces) {
		ac.notifyWatchers(workspace, nil, sets.String{}, sets.String{})
	}
}

// invalidateCache returns true if there was a change in the cluster workspace that holds cluster role and role bindings
func (ac *AuthorizationCache) invalidateCache() bool {
	invalidateCache := false

	clusterRoleList, err := ac.clusterRoleLister.List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(err)
		return invalidateCache
	}

	temporaryVersions := sets.NewString()
	for _, clusterRole := range clusterRoleList {
		temporaryVersions.Insert(clusterRole.ResourceVersion)
	}
	if (len(ac.clusterRoleResourceVersions) != len(temporaryVersions)) || !ac.clusterRoleResourceVersions.HasAll(temporaryVersions.List()...) {
		invalidateCache = true
		ac.clusterRoleResourceVersions = temporaryVersions
	}

	clusterRoleBindingList, err := ac.clusterRoleBindingLister.List(labels.Everything())
	if err != nil {
		utilruntime.HandleError(err)
		return invalidateCache
	}

	temporaryVersions.Delete(temporaryVersions.List()...)
	for _, clusterRoleBinding := range clusterRoleBindingList {
		temporaryVersions.Insert(clusterRoleBinding.ResourceVersion)
	}
	if (len(ac.clusterBindingResourceVersions) != len(temporaryVersions)) || !ac.clusterBindingResourceVersions.HasAll(temporaryVersions.List()...) {
		invalidateCache = true
		ac.clusterBindingResourceVersions = temporaryVersions
	}
	return invalidateCache
}

// synchronize runs a a full synchronization over the cache data.  it must be run in a single-writer model, it's not thread-safe by design.
func (ac *AuthorizationCache) synchronize() {
	ac.rwMutex.Lock()
	defer ac.rwMutex.Unlock()

	// if none of our internal reflectors changed, then we can skip reviewing the cache
	skip, currentState := ac.skip.SkipSynchronize(ac.lastState, ac.lastSyncResourceVersioner, ac.roleLastSyncResourceVersioner)
	if skip {
		return
	}

	// by default, we update our current caches and do an incremental change
	userSubjectRecordStore := ac.userSubjectRecordStore
	groupSubjectRecordStore := ac.groupSubjectRecordStore
	reviewRecordStore := ac.reviewRecordStore

	// if there was a global change that forced complete invalidation, we rebuild our cache and do a fast swap at end
	invalidateCache := ac.invalidateCache()
	if invalidateCache {
		userSubjectRecordStore = cache.NewStore(subjectRecordKeyFn)
		groupSubjectRecordStore = cache.NewStore(subjectRecordKeyFn)
		reviewRecordStore = cache.NewStore(reviewRecordKeyFn)
	}

	// iterate over caches and synchronize our three caches
	newKnownWorkspaces := ac.synchronizeWorkspaces(userSubjectRecordStore, groupSubjectRecordStore, reviewRecordStore)
	ac.purgeDeletedWorkspaces(ac.allKnownWorkspaces, newKnownWorkspaces, userSubjectRecordStore, groupSubjectRecordStore, reviewRecordStore)

	// if we did a full rebuild, now we swap the fully rebuilt cache
	if invalidateCache {
		ac.userSubjectRecordStore = userSubjectRecordStore
		ac.groupSubjectRecordStore = groupSubjectRecordStore
		ac.reviewRecordStore = reviewRecordStore
	}
	ac.allKnownWorkspaces = newKnownWorkspaces

	// we were able to update our cache since this last observation period
	ac.lastState = currentState
}

// syncRequest takes a reviewRequest and determines if it should update the caches supplied, it is not thread-safe
func (ac *AuthorizationCache) syncRequest(request *reviewRequest, userSubjectRecordStore cache.Store, groupSubjectRecordStore cache.Store, reviewRecordStore cache.Store) error {
	lastKnownValue, err := lastKnown(reviewRecordStore, request.workspace)
	if err != nil {
		return err
	}

	if skipReview(request, lastKnownValue) {
		return nil
	}

	workspace := request.workspace

	// Create a copy of reviewTemplate
	reviewAttributes := ac.reviewTemplate

	// And set the resource name on it
	_, workspaceName := clusters.SplitClusterAwareKey(workspace)
	reviewAttributes.Name = workspaceName

	review := ac.reviewer.Review(reviewAttributes)

	usersToRemove := sets.NewString()
	groupsToRemove := sets.NewString()
	if lastKnownValue != nil {
		usersToRemove.Insert(lastKnownValue.users...)
		usersToRemove.Delete(review.Users...)
		groupsToRemove.Insert(lastKnownValue.groups...)
		groupsToRemove.Delete(review.Groups...)
	}

	deleteWorkspaceFromSubjects(userSubjectRecordStore, usersToRemove.List(), workspace)
	deleteWorkspaceFromSubjects(groupSubjectRecordStore, groupsToRemove.List(), workspace)
	addSubjectsToWorkspace(userSubjectRecordStore, review.Users, workspace)
	addSubjectsToWorkspace(groupSubjectRecordStore, review.Groups, workspace)
	cacheReviewRecord(request, lastKnownValue, review, reviewRecordStore)
	ac.notifyWatchers(workspace, lastKnownValue, sets.NewString(review.Users...), sets.NewString(review.Groups...))

	if review.EvaluationError != nil {
		klog.V(5).ErrorS(review.EvaluationError, "Evaluation Error in the workspace authorization cache")
	}
	return nil
}

// List returns the set of workspace names for all workspaces that match the given selector
func (ac *AuthorizationCache) ListAllWorkspaces(selector labels.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	ac.rwMutex.RLock()
	defer ac.rwMutex.RUnlock()

	keys := sets.String{}
	// All the workspace objects are accessible to the "system:masters" group
	obj, exists, _ := ac.groupSubjectRecordStore.GetByKey(user.SystemPrivilegedGroup)
	if exists {
		subjectRecord := obj.(*subjectRecord)
		keys.Insert(subjectRecord.workspaces.List()...)
	}

	workspaceList := &tenancyv1alpha1.ClusterWorkspaceList{}
	for _, key := range keys.List() {
		workspace, err := ac.workspaceLister.Get(key)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// only match selected labels
		if !selector.Matches(labels.Set(workspace.Labels)) {
			continue
		}
		workspaceList.Items = append(workspaceList.Items, *workspace)
	}
	return workspaceList, nil
}

// List returns the set of workspace names the user has access to view
func (ac *AuthorizationCache) List(userInfo user.Info, labelSelector labels.Selector, fieldSelector fields.Selector) (*tenancyv1alpha1.ClusterWorkspaceList, error) {
	ac.rwMutex.RLock()
	defer ac.rwMutex.RUnlock()

	keys := sets.String{}
	user := userInfo.GetName()
	groups := userInfo.GetGroups()

	obj, exists, _ := ac.userSubjectRecordStore.GetByKey(user)
	if exists {
		subjectRecord := obj.(*subjectRecord)
		keys.Insert(subjectRecord.workspaces.List()...)
	}

	for _, group := range groups {
		obj, exists, _ := ac.groupSubjectRecordStore.GetByKey(group)
		if exists {
			subjectRecord := obj.(*subjectRecord)
			keys.Insert(subjectRecord.workspaces.List()...)
		}
	}

	workspaceList := &tenancyv1alpha1.ClusterWorkspaceList{}
	for _, key := range keys.List() {
		workspace, err := ac.workspaceLister.Get(key)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}

		// only match selected labels and fields

		predicate := workspaceutil.MatchWorkspace(labelSelector, fieldSelector)
		if matches, err := predicate.Matches(workspace); err != nil || !matches {
			continue
		}

		workspaceList.Items = append(workspaceList.Items, *workspace)
	}
	return workspaceList, nil
}

func (ac *AuthorizationCache) ReadyForAccess() bool {
	ac.rwMutex.RLock()
	defer ac.rwMutex.RUnlock()

	return len(ac.lastState) > 0
}

// skipReview returns true if the request was satisfied by the lastKnown
func skipReview(request *reviewRequest, lastKnownValue *reviewRecord) bool {

	// if your request is nil, you have no reason to make a review
	if request == nil {
		return true
	}

	// if you know nothing from a prior review, you better make a request
	if lastKnownValue == nil {
		return false
	}
	// if you are asking about a specific workspace, and you think you knew about a different one, you better check again
	if request.workspace != lastKnownValue.workspace {
		return false
	}

	// if you are making your request relative to a specific resource version, only make it if its different
	if len(request.workspaceResourceVersion) > 0 && request.workspaceResourceVersion != lastKnownValue.workspaceResourceVersion {
		return false
	}

	// if you see a new role binding, or a newer version, we need to do a review
	for k, v := range request.roleBindingUIDToResourceVersion {
		oldValue, exists := lastKnownValue.roleBindingUIDToResourceVersion[k]
		if !exists || v != oldValue {
			return false
		}
	}

	// if you see a new role, or a newer version, we need to do a review
	for k, v := range request.roleUIDToResourceVersion {
		oldValue, exists := lastKnownValue.roleUIDToResourceVersion[k]
		if !exists || v != oldValue {
			return false
		}
	}
	return true
}

// deleteWorkspaceFromSubjects removes the workspace from each subject
// if no other workspaces are active to that subject, it will also delete the subject from the cache entirely
func deleteWorkspaceFromSubjects(subjectRecordStore cache.Store, subjects []string, workspace string) {
	for _, subject := range subjects {
		obj, exists, _ := subjectRecordStore.GetByKey(subject)
		if exists {
			subjectRecord := obj.(*subjectRecord)
			delete(subjectRecord.workspaces, workspace)
			if len(subjectRecord.workspaces) == 0 {
				_ = subjectRecordStore.Delete(subjectRecord)
			}
		}
	}
}

// addSubjectsToWorkspace adds the specified workspace to each subject
func addSubjectsToWorkspace(subjectRecordStore cache.Store, subjects []string, workspace string) {
	for _, subject := range subjects {
		var item *subjectRecord
		obj, exists, _ := subjectRecordStore.GetByKey(subject)
		if exists {
			item = obj.(*subjectRecord)
		} else {
			item = &subjectRecord{subject: subject, workspaces: sets.NewString()}
			_ = subjectRecordStore.Add(item)
		}
		item.workspaces.Insert(workspace)
	}
}

func (ac *AuthorizationCache) notifyWatchers(workspaceKey string, exists *reviewRecord, users, groups sets.String) {
	_, workspaceName := clusters.SplitClusterAwareKey(workspaceKey)
	ac.watcherLock.Lock()
	defer ac.watcherLock.Unlock()
	for _, watcher := range ac.watchers {
		watcher.GroupMembershipChanged(workspaceName, users, groups)
	}
}

// cacheReviewRecord updates the cache based on the request processed
func cacheReviewRecord(request *reviewRequest, lastKnownValue *reviewRecord, review Review, reviewRecordStore cache.Store) {
	reviewRecord := &reviewRecord{
		reviewRequest: &reviewRequest{workspace: request.workspace, roleUIDToResourceVersion: map[types.UID]string{}, roleBindingUIDToResourceVersion: map[types.UID]string{}},
		groups:        review.Groups,
		users:         review.Users,
	}
	// keep what we last believe we knew by default
	if lastKnownValue != nil {
		reviewRecord.workspaceResourceVersion = lastKnownValue.workspaceResourceVersion
		for k, v := range lastKnownValue.roleUIDToResourceVersion {
			reviewRecord.roleUIDToResourceVersion[k] = v
		}
		for k, v := range lastKnownValue.roleBindingUIDToResourceVersion {
			reviewRecord.roleBindingUIDToResourceVersion[k] = v
		}
	}

	// update the review record relative to what drove this request
	if len(request.workspaceResourceVersion) > 0 {
		reviewRecord.workspaceResourceVersion = request.workspaceResourceVersion
	}
	for k, v := range request.roleUIDToResourceVersion {
		reviewRecord.roleUIDToResourceVersion[k] = v
	}
	for k, v := range request.roleBindingUIDToResourceVersion {
		reviewRecord.roleBindingUIDToResourceVersion[k] = v
	}
	// update the cache record
	_ = reviewRecordStore.Add(reviewRecord)
}

func lastKnown(reviewRecordStore cache.Store, workspace string) (*reviewRecord, error) {
	obj, exists, err := reviewRecordStore.GetByKey(workspace)
	if err != nil {
		return nil, err
	}
	if exists {
		return obj.(*reviewRecord), nil
	}
	return nil, nil
}
