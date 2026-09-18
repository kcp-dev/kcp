/*
Copyright 2021 The kcp Authors.

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

// Package shard runs on every shard and maintains the status of the shard's
// own authoritative Shard object in the local system:shard logical cluster.
// It mimics a Kubernetes node reporting its state: it keeps the Schedulable
// condition in sync with the unschedulable (cordon) annotation and reports the
// scheduling limits it enforces through the ResourceLimitsApplied condition,
// acknowledging what was written through the Admin workspace.
// The status replicates to the cache server and is mirrored onto the shard's
// representation in the root workspace, where admins can see the ack.
package shard

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	kcpcache "github.com/kcp-dev/apimachinery/v2/pkg/cache"
	"github.com/kcp-dev/logicalcluster/v3"
	corev1alpha1 "github.com/kcp-dev/sdk/apis/core/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"
	"github.com/kcp-dev/sdk/apis/third_party/conditions/util/conditions"
	kcpclientset "github.com/kcp-dev/sdk/client/clientset/versioned/cluster"
	corev1alpha1client "github.com/kcp-dev/sdk/client/clientset/versioned/typed/core/v1alpha1"
	corev1alpha1informers "github.com/kcp-dev/sdk/client/informers/externalversions/core/v1alpha1"

	configshard "github.com/kcp-dev/kcp/config/shard"
	"github.com/kcp-dev/kcp/pkg/logging"
	"github.com/kcp-dev/kcp/pkg/reconciler/committer"
)

const (
	ControllerName = "kcp-shard"
)

func NewController(
	shardName string,
	kcpClient kcpclientset.ClusterInterface,
	shardInformer corev1alpha1informers.ShardClusterInformer,
) (*Controller, error) {
	c := &Controller{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{
				Name: ControllerName,
			},
		),
		shardName: shardName,
		kcpClient: kcpClient,
		commit:    committer.NewCommitter[*Shard, Patcher, *ShardSpec, *ShardStatus](kcpClient.CoreV1alpha1().Shards()),
		getShard: func(clusterName logicalcluster.Name, name string) (*corev1alpha1.Shard, error) {
			return shardInformer.Cluster(clusterName).Lister().Get(name)
		},
	}

	_, _ = shardInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.enqueue(obj) },
		UpdateFunc: func(_, obj interface{}) { c.enqueue(obj) },
	})

	return c, nil
}

// Controller maintains the status of this shard's own authoritative Shard
// object in the local system:shard logical cluster: the Schedulable condition
// acknowledging cordon/uncordon signals, and the ResourceLimitsApplied
// condition acknowledging the scheduling limits.
type Controller struct {
	queue workqueue.TypedRateLimitingInterface[string]

	shardName string
	kcpClient kcpclientset.ClusterInterface

	getShard func(clusterName logicalcluster.Name, name string) (*corev1alpha1.Shard, error)
	commit   CommitFunc
}

type Shard = corev1alpha1.Shard
type ShardSpec = corev1alpha1.ShardSpec
type ShardStatus = corev1alpha1.ShardStatus
type Patcher = corev1alpha1client.ShardInterface
type Resource = committer.Resource[*ShardSpec, *ShardStatus]
type CommitFunc = func(ctx context.Context, original, updated *Resource) error

func (c *Controller) enqueue(obj interface{}) {
	key, err := kcpcache.MetaClusterNamespaceKeyFunc(obj)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		utilruntime.HandleError(err)
		return
	}
	// only this shard's own authoritative object is of interest; leave
	// representations and legacy objects in other logical clusters alone.
	if clusterName != configshard.SystemShardCluster || name != c.shardName {
		return
	}
	logger := logging.WithQueueKey(logging.WithReconciler(klog.Background(), ControllerName), key)
	logger.V(4).Info("queueing Shard")
	c.queue.Add(key)
}

func (c *Controller) Start(ctx context.Context, numThreads int) {
	defer utilruntime.HandleCrash()
	defer c.queue.ShutDown()

	logger := logging.WithReconciler(klog.FromContext(ctx), ControllerName)
	ctx = klog.NewContext(ctx, logger)
	logger.Info("Starting controller")
	defer logger.Info("Shutting down controller")

	for range numThreads {
		go wait.UntilWithContext(ctx, c.startWorker, time.Second)
	}

	<-ctx.Done()
}

func (c *Controller) startWorker(ctx context.Context) {
	for c.processNextWorkItem(ctx) {
	}
}

func (c *Controller) processNextWorkItem(ctx context.Context) bool {
	// Wait until there is a new item in the working queue
	k, quit := c.queue.Get()
	if quit {
		return false
	}
	key := k

	logger := logging.WithQueueKey(klog.FromContext(ctx), key)
	ctx = klog.NewContext(ctx, logger)
	logger.V(4).Info("processing key")

	// No matter what, tell the queue we're done with this key, to unblock
	// other workers.
	defer c.queue.Done(key)

	if err := c.process(ctx, key); err != nil {
		utilruntime.HandleError(fmt.Errorf("%q controller failed to sync %q, err: %w", ControllerName, key, err))
		c.queue.AddRateLimited(key)
		return true
	}
	c.queue.Forget(key)
	return true
}

func (c *Controller) process(ctx context.Context, key string) error {
	logger := klog.FromContext(ctx)
	clusterName, _, name, err := kcpcache.SplitMetaClusterNamespaceKey(key)
	if err != nil {
		logger.Error(err, "invalid key")
		return nil
	}

	obj, err := c.getShard(clusterName, name)
	if err != nil {
		if kerrors.IsNotFound(err) {
			return nil // object deleted before we handled it
		}
		return err
	}

	previous := obj
	obj = obj.DeepCopy()

	logger = logging.WithObject(logger, obj)
	ctx = klog.NewContext(ctx, logger)

	var errs []error
	if err := c.reconcile(ctx, obj); err != nil {
		errs = append(errs, err)
	}

	oldResource := &Resource{ObjectMeta: previous.ObjectMeta, Spec: &previous.Spec, Status: &previous.Status}
	newResource := &Resource{ObjectMeta: obj.ObjectMeta, Spec: &obj.Spec, Status: &obj.Status}
	if err := c.commit(ctx, oldResource, newResource); err != nil {
		errs = append(errs, err)
	}

	logger.V(6).Info("processed Shard")
	return utilerrors.NewAggregate(errs)
}

// reconcile acknowledges the operational configuration written through the
// Admin workspace: it keeps the Schedulable condition in sync with the cordon
// annotation and reports the scheduling limits this shard enforces.
func (c *Controller) reconcile(_ context.Context, shard *corev1alpha1.Shard) error {
	if _, cordoned := shard.Annotations[corev1alpha1.ShardUnschedulableAnnotationKey]; cordoned {
		conditions.MarkFalse(
			shard,
			corev1alpha1.ShardSchedulable,
			corev1alpha1.ShardReasonCordoned,
			conditionsv1alpha1.ConditionSeverityInfo,
			"shard is cordoned via the %s annotation, no new workspaces are scheduled onto it",
			corev1alpha1.ShardUnschedulableAnnotationKey,
		)
	} else {
		conditions.MarkTrue(shard, corev1alpha1.ShardSchedulable)
	}

	reconcileResourceLimits(shard)
	return nil
}

// noResourceLimitsMessage is the ResourceLimitsApplied message when the shard
// enforces no limits at all.
const noResourceLimitsMessage = "no limits configured"

// reconcileResourceLimits reports the scheduling limits this shard is actually
// enforcing through the ResourceLimitsApplied condition. Limits are written
// through the Admin workspace and reach the owning shard indirectly, so this
// condition - whose message spells out the values in force - is what confirms
// the round trip completed.
func reconcileResourceLimits(shard *corev1alpha1.Shard) {
	applied := formatResourceLimits(shard.Spec.ResourceLimits)

	if unusable := unusableResourceLimits(shard.Spec.ResourceLimits); len(unusable) > 0 {
		conditions.MarkFalse(
			shard,
			corev1alpha1.ShardResourceLimitsApplied,
			corev1alpha1.ShardReasonInvalidResourceLimits,
			conditionsv1alpha1.ConditionSeverityWarning,
			"%s; %s", applied, strings.Join(unusable, "; "),
		)
		return
	}

	// MarkTrue carries no message, and the message is the point here.
	condition := conditions.TrueCondition(corev1alpha1.ShardResourceLimitsApplied)
	condition.Message = applied
	conditions.Set(shard, condition)
}

// formatResourceLimits renders the limits in force as a compact, parsable
// "soft/hard: <resource>=<soft>/<hard>" list sorted by resource name, using "-"
// for a tier that is not configured, e.g. "soft/hard: workspaces=10/20". The
// rendered values are the ones the shard enforces, so comparing them against
// spec.resourceLimits confirms the shard applied what was requested.
func formatResourceLimits(limits *corev1alpha1.ShardResourceLimits) string {
	if limits == nil || (len(limits.Soft) == 0 && len(limits.Hard) == 0) {
		return noResourceLimitsMessage
	}

	names := map[corev1.ResourceName]struct{}{}
	for name := range limits.Soft {
		names[name] = struct{}{}
	}
	for name := range limits.Hard {
		names[name] = struct{}{}
	}

	pairs := make([]string, 0, len(names))
	for _, name := range slices.Sorted(maps.Keys(names)) {
		pairs = append(pairs, fmt.Sprintf("%s=%s/%s", name, quantityOrDash(limits.Soft, name), quantityOrDash(limits.Hard, name)))
	}
	return "soft/hard: " + strings.Join(pairs, ", ")
}

// quantityOrDash renders a configured limit, or "-" when the tier does not set
// one for this resource.
func quantityOrDash(list corev1.ResourceList, name corev1.ResourceName) string {
	quantity, ok := list[name]
	if !ok {
		return "-"
	}
	return quantity.String()
}

// unusableResourceLimits describes the configured limits that do not take
// effect as written. A negative limit is treated as disabled by the scheduler,
// and a soft limit at or above the hard limit can never deprioritize the shard
// before the hard limit refuses it outright. Absent and zero limits are
// disabled by design and are not reported here.
func unusableResourceLimits(limits *corev1alpha1.ShardResourceLimits) []string {
	if limits == nil {
		return nil
	}
	var unusable []string
	for _, tier := range []struct {
		name string
		list corev1.ResourceList
	}{{"soft", limits.Soft}, {"hard", limits.Hard}} {
		for _, resource := range slices.Sorted(maps.Keys(tier.list)) {
			if quantity := tier.list[resource]; quantity.Sign() < 0 {
				unusable = append(unusable, fmt.Sprintf("%s %s=%s is negative and is treated as disabled", tier.name, resource, quantity.String()))
			}
		}
	}
	for _, resource := range slices.Sorted(maps.Keys(limits.Soft)) {
		soft, hard := limits.Soft[resource], limits.Hard[resource]
		if soft.Sign() > 0 && hard.Sign() > 0 && soft.Cmp(hard) >= 0 {
			unusable = append(unusable, fmt.Sprintf("soft %s=%s is not below hard %s=%s, so the shard is never deprioritized before it refuses new workspaces", resource, soft.String(), resource, hard.String()))
		}
	}
	return unusable
}
