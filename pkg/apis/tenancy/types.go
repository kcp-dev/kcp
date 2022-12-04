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

package tenancy

import (
	"github.com/kcp-dev/logicalcluster/v2"
)

// Object is a local interface representation of the Kubernetes metav1.Object, to avoid dependencies on
// k8s.io/apimachinery.
type Object interface {
	GetAnnotations() map[string]string
}

// Cluster is a logical cluster name, not a workspace path.
//
// +kubebuilder:validation:Pattern:="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
type Cluster string

func (c Cluster) Path() logicalcluster.Name {
	return logicalcluster.New(string(c))
}

func (c Cluster) String() string {
	return string(c)
}

func From(obj Object) Cluster {
	return Cluster(logicalcluster.From(obj).String())
}
