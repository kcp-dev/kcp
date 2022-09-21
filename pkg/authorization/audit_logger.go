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
	"context"
	"fmt"

	kaudit "k8s.io/apiserver/pkg/audit"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

const (
	auditDecision = "decision"
	auditReason   = "reason"
)

// NewAuditLogger returns an authorizer that logs every decision of the delegate authorizer
// for the given audit prefix key.
// Note: the prefix key must not contain a trailing slash `/`.
func NewAuditLogger(auditPrefix string, delegate authorizer.Authorizer) authorizer.Authorizer {
	return authorizer.AuthorizerFunc(func(ctx context.Context, attr authorizer.Attributes) (authorizer.Decision, string, error) {
		dec, reason, err := delegate.Authorize(ctx, attr)

		auditReason := reason
		if err != nil {
			auditReason = fmt.Sprintf("reason: %q, error: %v", reason, err)
		}

		kaudit.AddAuditAnnotations(
			ctx,
			auditPrefix+"/"+auditDecision, DecisionString(dec),
			auditPrefix+"/"+auditReason, auditReason,
		)

		return dec, reason, err
	})
}
