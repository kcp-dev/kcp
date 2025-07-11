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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceAuthenticationConfiguration specifies additional authentication options
// for workspaces.
//
// +crd
// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:scope=Cluster,categories=kcp
type WorkspaceAuthenticationConfiguration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`

	Spec WorkspaceAuthenticationConfigurationSpec `json:"spec"`
}

type WorkspaceAuthenticationConfigurationSpec struct {
	JWT []JWTAuthenticator `json:"jwt"`
}

type JWTAuthenticator struct {
	Issuer               Issuer                `json:"issuer"`
	ClaimValidationRules []ClaimValidationRule `json:"claimValidationRules,omitempty"`
	ClaimMappings        ClaimMappings         `json:"claimMappings"`
	UserValidationRules  []UserValidationRule  `json:"userValidationRules,omitempty"`
}

// Issuer provides the configuration for an external provider's specific settings.
type Issuer struct {
	// url points to the issuer URL in a format https://url or https://url/path.
	// This must match the "iss" claim in the presented JWT, and the issuer returned from discovery.
	// Same value as the --oidc-issuer-url flag.
	// Discovery information is fetched from "{url}/.well-known/openid-configuration" unless overridden by discoveryURL.
	// Required to be unique across all JWT authenticators.
	// Note that egress selection configuration is not used for this network connection.
	// +required
	URL string `json:"url"`
	// discoveryURL, if specified, overrides the URL used to fetch discovery
	// information instead of using "{url}/.well-known/openid-configuration".
	// The exact value specified is used, so "/.well-known/openid-configuration"
	// must be included in discoveryURL if needed.
	//
	// The "issuer" field in the fetched discovery information must match the "issuer.url" field
	// in the AuthenticationConfiguration and will be used to validate the "iss" claim in the presented JWT.
	// This is for scenarios where the well-known and jwks endpoints are hosted at a different
	// location than the issuer (such as locally in the cluster).
	//
	// Example:
	// A discovery url that is exposed using kubernetes service 'oidc' in namespace 'oidc-namespace'
	// and discovery information is available at '/.well-known/openid-configuration'.
	// discoveryURL: "https://oidc.oidc-namespace/.well-known/openid-configuration"
	// certificateAuthority is used to verify the TLS connection and the hostname on the leaf certificate
	// must be set to 'oidc.oidc-namespace'.
	//
	// curl https://oidc.oidc-namespace/.well-known/openid-configuration (.discoveryURL field)
	// {
	//     issuer: "https://oidc.example.com" (.url field)
	// }
	//
	// discoveryURL must be different from url.
	// Required to be unique across all JWT authenticators.
	// Note that egress selection configuration is not used for this network connection.
	// +optional
	DiscoveryURL         string                  `json:"discoveryURL"`
	CertificateAuthority string                  `json:"certificateAuthority"`
	Audiences            []string                `json:"audiences,omitempty"`
	AudienceMatchPolicy  AudienceMatchPolicyType `json:"audienceMatchPolicy"`
}

// AudienceMatchPolicyType is a set of valid values for Issuer.AudienceMatchPolicy
type AudienceMatchPolicyType string

// Valid types for AudienceMatchPolicyType
const (
	AudienceMatchPolicyMatchAny AudienceMatchPolicyType = "MatchAny"
)

// ClaimValidationRule provides the configuration for a single claim validation rule.
type ClaimValidationRule struct {
	Claim         string `json:"claim"`
	RequiredValue string `json:"requiredValue"`

	Expression string `json:"expression"`
	Message    string `json:"message"`
}

// ClaimMappings provides the configuration for claim mapping
type ClaimMappings struct {
	Username PrefixedClaimOrExpression `json:"username"`
	Groups   PrefixedClaimOrExpression `json:"groups"`
	UID      ClaimOrExpression         `json:"uid"`
	Extra    []ExtraMapping            `json:"extra,omitempty"`
}

// PrefixedClaimOrExpression provides the configuration for a single prefixed claim or expression.
type PrefixedClaimOrExpression struct {
	Claim  string  `json:"claim"`
	Prefix *string `json:"prefix,omitempty"`

	Expression string `json:"expression"`
}

// ClaimOrExpression provides the configuration for a single claim or expression.
type ClaimOrExpression struct {
	Claim      string `json:"claim"`
	Expression string `json:"expression"`
}

// ExtraMapping provides the configuration for a single extra mapping.
type ExtraMapping struct {
	Key             string `json:"key"`
	ValueExpression string `json:"valueExpression"`
}

// UserValidationRule provides the configuration for a single user validation rule.
type UserValidationRule struct {
	Expression string `json:"expression"`
	Message    string `json:"message"`
}

// WorkspaceAuthenticationConfigurationList is a list of WorkspaceAuthenticationConfigurations.
//
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type WorkspaceAuthenticationConfigurationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []WorkspaceAuthenticationConfiguration `json:"items"`
}
