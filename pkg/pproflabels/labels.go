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
