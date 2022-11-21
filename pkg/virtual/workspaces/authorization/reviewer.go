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
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
	kauthorizer "k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

// Review is a list of users and groups that can access a resource. It is also possible that the authorization
// check encountered some errors (e.g. if it couldn't resolve certain role bindings). Any errors encountered are
// stored in EvaluationError.
type Review struct {
	Users           []string
	Groups          []string
	EvaluationError error
}

// Allows returns true if there is an intersection between either the Review's groups and the user's groups,
// or the Review's users and the user's name.
func (r Review) Allows(user user.Info) bool {
	return sets.NewString(r.Groups...).HasAny(user.GetGroups()...) || sets.NewString(r.Users...).Has(user.GetName())
}

// Reviewer is a wrapper around rbac.SubjectLocator that parses the allowed subjects and splits them into users and
// groups.
type Reviewer struct {
	subjectLocater rbac.SubjectLocator
}

// NewReviewer returns a new Reviewer that uses subjectLocator.
func NewReviewer(subjectLocator rbac.SubjectLocator) *Reviewer {
	return &Reviewer{
		subjectLocater: subjectLocator,
	}
}

// Review returns a Review for attributes.
func (r *Reviewer) Review(ctx context.Context, attributes kauthorizer.Attributes) Review {
	if r.subjectLocater == nil {
		return Review{}
	}
	subjects, err := r.subjectLocater.AllowedSubjects(ctx, attributes)
	review := Review{
		EvaluationError: err,
	}
	review.Users, review.Groups = rbacSubjectsToUsersAndGroups(subjects)
	return review
}

func (r *Reviewer) Authorize(ctx context.Context, attributes kauthorizer.Attributes) (authorized kauthorizer.Decision, reason string, err error) {
	review := r.Review(ctx, attributes)
	if review.Allows(attributes.GetUser()) {
		return kauthorizer.DecisionAllow, "", nil
	}
	return kauthorizer.DecisionNoOpinion, "SubjectNotAllowed", nil
}

func rbacSubjectsToUsersAndGroups(subjects []rbacv1.Subject) (users []string, groups []string) {
	for _, subject := range subjects {
		switch {
		case subject.APIGroup == rbacv1.GroupName && subject.Kind == rbacv1.GroupKind:
			groups = append(groups, subject.Name)
		case subject.APIGroup == rbacv1.GroupName && subject.Kind == rbacv1.UserKind:
			users = append(users, subject.Name)
		}
	}

	return users, groups
}
