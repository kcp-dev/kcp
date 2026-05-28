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

// Package pproflabels attaches pprof labels to goroutines so that profiles and
// goroutine dumps remain attributable to the controller and logical cluster
// that own them. Labels propagate to goroutines started during the labeled
// call, so wrapping the outermost per-cluster goroutine is enough.
package pproflabels

import (
	"context"
	"runtime/pprof"

	"github.com/kcp-dev/logicalcluster/v3"
)

// Label keys. Kept stable so tooling (pprof filters, leak tests) can match on them.
const (
	LabelController     = "controller"
	LabelLogicalCluster = "logicalcluster"
)

// Cluster runs fn with pprof labels identifying the controller and logical
// cluster. Wrap per-cluster goroutine starts with this so leaked goroutines
// stay attributable in /debug/pprof/goroutine?debug=2 dumps and in goleak
// output. See https://github.com/kcp-dev/kcp/issues/4071.
func Cluster(ctx context.Context, controller string, cluster logicalcluster.Name, fn func(context.Context)) {
	pprof.Do(ctx, pprof.Labels(LabelController, controller, LabelLogicalCluster, cluster.String()), fn)
}

// PushCluster applies the (controller, cluster) labels to the current goroutine
// and returns a labeled context plus a restore function. Use at the top of a
// reconcile body when wrapping in a closure is impractical:
//
//	ctx, done := pproflabels.PushCluster(ctx, ControllerName, clusterName)
//	defer done()
//
// Scope of attribution this gives you:
//   - CPU profile (/debug/pprof/profile): samples taken while the goroutine
//     holds these labels are tagged with (controller, cluster). Use
//     `pprof -tagfocus=controller=...` to slice CPU by reconciler.
//   - Goroutine profile (/debug/pprof/goroutine?debug=1): any goroutine still
//     mid-call when the dump is taken shows labels. Spawning a child
//     goroutine between push and done() also propagates labels for that
//     child's lifetime — useful only if the reconcile body itself starts a
//     long-lived goroutine, which the per-cluster reconcilers usually don't.
//
// What this does NOT give you:
//   - Heap profile attribution. Go's mprof samples do not record goroutine
//     labels (only the CPU and goroutine profiles do). Heap analysis stays
//     call-site-based via `pprof -base T0.pprof T2.pprof`.
//
// Restoration assumes the goroutine's labels at call entry match the labels
// already in ctx. kcp reconciler workers start clean and propagate ctx, so
// that holds; if an upstream caller has set labels via SetGoroutineLabels
// without putting them in ctx, this restore clobbers them.
func PushCluster(ctx context.Context, controller string, cluster logicalcluster.Name) (context.Context, func()) {
	prev := ctx
	labeled := pprof.WithLabels(ctx, pprof.Labels(LabelController, controller, LabelLogicalCluster, cluster.String()))
	pprof.SetGoroutineLabels(labeled)
	return labeled, func() { pprof.SetGoroutineLabels(prev) }
}
