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

package auth

import (
	"path"

	rbacv1 "k8s.io/api/rbac/v1"
	kauthorizer "k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"

	tenancyv1alpha1 "github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
)

// Review is a list of users and groups that can access a resource
type Review struct {
	Users           []string
	Groups          []string
	EvaluationError error
}

type ReviewerProvider interface {
	Create(checkedVerb, checkedResource string, subresources ...string) Reviewer
}

type authorizerReviewerProvider struct {
	policyChecker rbac.SubjectLocator
}

func (arp *authorizerReviewerProvider) Create(checkedVerb, checkedResource string, subresources ...string) Reviewer {
	return &authorizerReviewer{
		checkedVerb:     checkedVerb,
		checkedResource: checkedResource,
		policyChecker:   arp.policyChecker,
		subresources:    subresources,
	}
}

// Reviewer performs access reviews for a workspace by name
type Reviewer interface {
	Review(name string) (Review, error)
}

type authorizerReviewer struct {
	checkedVerb, checkedResource string
	subresources                 []string
	policyChecker                rbac.SubjectLocator
}

func NewAuthorizerReviewerProvider(policyChecker rbac.SubjectLocator) ReviewerProvider {
	return &authorizerReviewerProvider{
		policyChecker: policyChecker,
	}
}

func (r *authorizerReviewer) Review(workspaceName string) (Review, error) {
	attributes := kauthorizer.AttributesRecord{
		Verb:            r.checkedVerb,
		Namespace:       "",
		APIGroup:        tenancyv1alpha1.SchemeGroupVersion.Group,
		APIVersion:      tenancyv1alpha1.SchemeGroupVersion.Version,
		Resource:        r.checkedResource,
		Subresource:     path.Join(r.subresources...),
		Name:            workspaceName,
		ResourceRequest: true,
	}

	subjects, err := r.policyChecker.AllowedSubjects(attributes)
	review := Review{}
	review.Users, review.Groups = RBACSubjectsToUsersAndGroups(subjects)
	if err != nil {
		review.EvaluationError = err
	}
	return review, nil
}

func RBACSubjectsToUsersAndGroups(subjects []rbacv1.Subject) (users []string, groups []string) {
	for _, subject := range subjects {

		switch {
		case subject.APIGroup == rbacv1.GroupName && subject.Kind == rbacv1.GroupKind:
			groups = append(groups, subject.Name)
		case subject.APIGroup == rbacv1.GroupName && subject.Kind == rbacv1.UserKind:
			users = append(users, subject.Name)
		case subject.APIGroup == "" && subject.Kind == rbacv1.ServiceAccountKind:
			// service account are not considered here
		default:
			// service account are not considered here
		}
	}

	return users, groups
}
