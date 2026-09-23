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

package v1alpha1

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp,path=permissionclaimpolicies,singular=permissionclaimpolicy
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// PermissionClaimPolicy is an installation-wide policy, stored in the Admin
// workspace (`/services/admin`), that lets a family of APIExports claim each
// other's resources without knowing their identity hashes.
//
// It does two things at once:
//
//   - It reserves every API group it mentions (claimers and claimed groups
//     alike): only the subjects listed under spec.providers may create an
//     APIExport that exports resources in a reserved group. This is what makes
//     "an APIExport that exports group X" a trustworthy identity for X, and
//     what makes a binding of a reserved group in any workspace a genuine one.
//   - It grants, per claimer group, an explicit list of groups that an
//     APIExport exporting that claimer group may claim with an empty
//     identityHash. No wildcards: every group is named.
//
// At runtime such a claim resolves, in every consumer workspace, to whatever
// APIExport that workspace's APIBinding for the claimed resource points at.
// Nobody has to know or enumerate identity hashes, and rotation or multiple
// genuine producers of the same group are handled per workspace.
type PermissionClaimPolicy struct {
	metav1.TypeMeta `json:",inline"`
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec holds the desired state.
	//
	// +required
	// +kubebuilder:validation:Required
	Spec PermissionClaimPolicySpec `json:"spec"`
}

// PermissionClaimPolicySpec defines the desired state of a PermissionClaimPolicy.
type PermissionClaimPolicySpec struct {
	// providers are the subjects allowed to create or extend an APIExport that
	// exports resources in any API group reserved by this policy. A group is
	// reserved as soon as it appears anywhere in spec.claims, as a claimer or
	// as a claimed group.
	//
	// If providers is empty, no APIExport can be created for the reserved groups
	// at all, which effectively freezes the set of existing exports.
	//
	// +optional
	// +listType=atomic
	Providers []PermissionClaimPolicySubject `json:"providers,omitempty"`

	// claims lists, per claimer API group, the API groups an APIExport
	// exporting that claimer group may claim without an identityHash.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=claimer
	Claims []PermissionClaimRule `json:"claims"`
}

// PermissionClaimRule grants one claimer group an explicit list of claimable groups.
type PermissionClaimRule struct {
	// claimer is the API group an APIExport must export, in at least one of its
	// resources, to be granted the claims listed in groups. No wildcards.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	Claimer string `json:"claimer"`

	// groups are the API groups the claimer may claim without an identityHash.
	// Every entry is a full group name. No wildcards.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +listType=set
	Groups []string `json:"groups"`
}

// PermissionClaimPolicySubjectKind is the kind of a policy subject.
//
// +kubebuilder:validation:Enum=User;Group
type PermissionClaimPolicySubjectKind string

const (
	// PermissionClaimPolicySubjectUser matches a request whose user name equals the subject name.
	PermissionClaimPolicySubjectUser PermissionClaimPolicySubjectKind = "User"
	// PermissionClaimPolicySubjectGroup matches a request whose user is a member of the named group.
	PermissionClaimPolicySubjectGroup PermissionClaimPolicySubjectKind = "Group"
)

// PermissionClaimPolicySubject identifies a user or a group. Service accounts
// are matched through their user name (system:serviceaccount:<namespace>:<name>)
// or their groups (system:serviceaccounts, system:serviceaccounts:<namespace>).
type PermissionClaimPolicySubject struct {
	// kind is the kind of subject: User or Group.
	//
	// +required
	// +kubebuilder:validation:Required
	Kind PermissionClaimPolicySubjectKind `json:"kind"`

	// name is the user name or group name.
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// ReservedGroups returns every API group this policy reserves, i.e. every
// claimer group and every claimed group, deduplicated and in no particular order.
func (p *PermissionClaimPolicy) ReservedGroups() []string {
	seen := map[string]struct{}{}
	var groups []string
	add := func(g string) {
		if _, ok := seen[g]; ok {
			return
		}
		seen[g] = struct{}{}
		groups = append(groups, g)
	}
	for _, rule := range p.Spec.Claims {
		add(rule.Claimer)
		for _, g := range rule.Groups {
			add(g)
		}
	}
	return groups
}

// Reserves reports whether the policy reserves the given API group.
func (p *PermissionClaimPolicy) Reserves(group string) bool {
	for _, rule := range p.Spec.Claims {
		if rule.Claimer == group || slices.Contains(rule.Groups, group) {
			return true
		}
	}
	return false
}

// Allows reports whether an APIExport exporting the claimer group may claim
// the given group without an identity hash under this policy.
func (p *PermissionClaimPolicy) Allows(claimer, group string) bool {
	for _, rule := range p.Spec.Claims {
		if rule.Claimer == claimer && slices.Contains(rule.Groups, group) {
			return true
		}
	}
	return false
}

// PermissionClaimPolicyList is a list of PermissionClaimPolicy resources.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type PermissionClaimPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []PermissionClaimPolicy `json:"items"`
}
