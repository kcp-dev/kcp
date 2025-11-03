/*
Copyright 2025 The KCP Authors.

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

package authentication

import (
	"k8s.io/apiserver/pkg/apis/apiserver"

	tenancyv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

// The functions in this file convert the kcp AuthConfig to Kubernetes AuthConfig, which cannot be
// used directly in our CRDs because they do not have json tags.

func convertAuthenticationConfiguration(in *tenancyv1alpha1.WorkspaceAuthenticationConfiguration) *apiserver.AuthenticationConfiguration {
	out := &apiserver.AuthenticationConfiguration{
		JWT: []apiserver.JWTAuthenticator{},
	}

	for _, authn := range in.Spec.JWT {
		out.JWT = append(out.JWT, convertJWTAuthenticator(authn))
	}

	return out
}

func convertJWTAuthenticator(in tenancyv1alpha1.JWTAuthenticator) apiserver.JWTAuthenticator {
	out := apiserver.JWTAuthenticator{
		Issuer:               convertIssuer(in.Issuer),
		ClaimMappings:        convertClaimMappings(in.ClaimMappings),
		ClaimValidationRules: []apiserver.ClaimValidationRule{},
		UserValidationRules:  []apiserver.UserValidationRule{},
	}

	for _, rule := range in.ClaimValidationRules {
		out.ClaimValidationRules = append(out.ClaimValidationRules, convertClaimValidationRule(rule))
	}

	for _, rule := range in.UserValidationRules {
		out.UserValidationRules = append(out.UserValidationRules, convertUserValidationRule(rule))
	}

	return out
}

func convertIssuer(in tenancyv1alpha1.Issuer) apiserver.Issuer {
	return apiserver.Issuer{
		URL:                  in.URL,
		DiscoveryURL:         in.DiscoveryURL,
		CertificateAuthority: in.CertificateAuthority,
		Audiences:            in.Audiences,
		AudienceMatchPolicy:  apiserver.AudienceMatchPolicyType(in.AudienceMatchPolicy),
	}
}

func convertClaimMappings(in tenancyv1alpha1.ClaimMappings) apiserver.ClaimMappings {
	out := apiserver.ClaimMappings{
		Username: convertPrefixedClaimOrExpression(in.Username),
		Groups:   convertPrefixedClaimOrExpression(in.Groups),
		UID:      convertClaimOrExpression(in.UID),
		Extra:    []apiserver.ExtraMapping{},
	}

	for _, mapping := range in.Extra {
		out.Extra = append(out.Extra, convertExtraMapping(mapping))
	}

	return out
}

func convertPrefixedClaimOrExpression(in tenancyv1alpha1.PrefixedClaimOrExpression) apiserver.PrefixedClaimOrExpression {
	return apiserver.PrefixedClaimOrExpression{
		Claim:      in.Claim,
		Prefix:     in.Prefix,
		Expression: in.Expression,
	}
}

func convertClaimOrExpression(in tenancyv1alpha1.ClaimOrExpression) apiserver.ClaimOrExpression {
	return apiserver.ClaimOrExpression{
		Claim:      in.Claim,
		Expression: in.Expression,
	}
}

func convertClaimValidationRule(in tenancyv1alpha1.ClaimValidationRule) apiserver.ClaimValidationRule {
	return apiserver.ClaimValidationRule{
		Claim:         in.Claim,
		RequiredValue: in.RequiredValue,
		Message:       in.Message,
		Expression:    in.Expression,
	}
}

func convertUserValidationRule(in tenancyv1alpha1.UserValidationRule) apiserver.UserValidationRule {
	return apiserver.UserValidationRule{
		Message:    in.Message,
		Expression: in.Expression,
	}
}

func convertExtraMapping(in tenancyv1alpha1.ExtraMapping) apiserver.ExtraMapping {
	return apiserver.ExtraMapping{
		Key:             in.Key,
		ValueExpression: in.ValueExpression,
	}
}
