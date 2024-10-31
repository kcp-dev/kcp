/*
Copyright The KCP Authors.

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/kcp-dev/kcp/sdk/apis/tenancy/v1alpha1"
	conditionsv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/third_party/conditions/apis/conditions/v1alpha1"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VClusterStatusApplyConfiguration represents a declarative configuration of the VClusterStatus type for use
// with apply.
type VClusterStatusApplyConfiguration struct {
	URL                    *string                        `json:"URL,omitempty"`
	SecretString           *string                        `json:"secretString,omitempty"`
	Phase                  *v1alpha1.MountPhaseType       `json:"phase,omitempty"`
	LastProxyHeartbeatTime *v1.Time                       `json:"lastProxyHeartbeatTime,omitempty"`
	Conditions             *conditionsv1alpha1.Conditions `json:"conditions,omitempty"`
}

// VClusterStatusApplyConfiguration constructs a declarative configuration of the VClusterStatus type for use with
// apply.
func VClusterStatus() *VClusterStatusApplyConfiguration {
	return &VClusterStatusApplyConfiguration{}
}

// WithURL sets the URL field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the URL field is set to the value of the last call.
func (b *VClusterStatusApplyConfiguration) WithURL(value string) *VClusterStatusApplyConfiguration {
	b.URL = &value
	return b
}

// WithSecretString sets the SecretString field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the SecretString field is set to the value of the last call.
func (b *VClusterStatusApplyConfiguration) WithSecretString(value string) *VClusterStatusApplyConfiguration {
	b.SecretString = &value
	return b
}

// WithPhase sets the Phase field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Phase field is set to the value of the last call.
func (b *VClusterStatusApplyConfiguration) WithPhase(value v1alpha1.MountPhaseType) *VClusterStatusApplyConfiguration {
	b.Phase = &value
	return b
}

// WithLastProxyHeartbeatTime sets the LastProxyHeartbeatTime field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the LastProxyHeartbeatTime field is set to the value of the last call.
func (b *VClusterStatusApplyConfiguration) WithLastProxyHeartbeatTime(value v1.Time) *VClusterStatusApplyConfiguration {
	b.LastProxyHeartbeatTime = &value
	return b
}

// WithConditions sets the Conditions field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the Conditions field is set to the value of the last call.
func (b *VClusterStatusApplyConfiguration) WithConditions(value conditionsv1alpha1.Conditions) *VClusterStatusApplyConfiguration {
	b.Conditions = &value
	return b
}
