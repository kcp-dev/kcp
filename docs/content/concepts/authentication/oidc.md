---
description: >
  How to setup OIDC authentication in kcp.
---

# OIDC Setup

OpenID Connect (OIDC) is a simple identity layer on top of the OAuth 2.0 protocol, which allows clients to verify the identity of users based on the authentication performed by an external authorization server. In this guide, we will set up OIDC authentication in kcp using Dex as the identity provider.
For more details on Kubernetes specific configuration, please refer to this [page](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#openid-connect-tokens).


## Configure kcp OIDC Authentication Using OIDC Flags

kcp server will configure the OIDC authentication using the same Kubernetes control plane settings, to start kcp with OIDC authentication enabled, you can simply pass OIDC flags during the start of the kcp server:

```bash
kcp start \
--oidc-issuer-url=<url of the issuer> \
--oidc-client-id=<client-id>\
--oidc-groups-claim=<jwt-claim-name> \
--oidc-ca-file=<ca-file-path>
```

- `--oidc-issuer-url` URL of the provider that allows the API server to discover public signing keys.

- `--oidc-client-id` A client id that all tokens must be issued for.

- `--oidc-groups-claim` JWT claim to use as the user's group.

- `--oidc-ca-file` The path to the certificate for the CA that signed your identity provider's web certificate.

You can also set:

- `--oidc-username-claim` JWT claim to use as the user name.

- `--oidc-required-claim` A key=value pair that describes a required claim in the ID Token.

- `--oidc-signing-algs` The signing algorithms accepted.

- `--oidc-username-prefix` Prefix prepended to username claims to prevent clashes with existing names.

- `--oidc-groups-prefix` Prefix prepended to group claims to prevent clashes with existing names

## Configure kcp OIDC Authentication Using Structured Authentication Configuration

Alternatively, you can use the beta feature of authentication configuration from a file and set up the kcp server with it.
Please note that if you specify `--authentication-config` along with any of the `--oidc-*` command line arguments, this will be treated as a misconfiguration.

```bash
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
# list of authenticators to authenticate Kubernetes users using JWT compliant tokens.
# the maximum number of allowed authenticators is 64.
jwt:
- issuer:
    # url must be unique across all authenticators.
    # url must not conflict with issuer configured in --service-account-issuer.
    url: https://example.com # Same as --oidc-issuer-url.
    # discoveryURL, if specified, overrides the URL used to fetch discovery
    # information instead of using "{url}/.well-known/openid-configuration".
    # The exact value specified is used, so "/.well-known/openid-configuration"
    # must be included in discoveryURL if needed.
    #
    # The "issuer" field in the fetched discovery information must match the "issuer.url" field
    # in the AuthenticationConfiguration and will be used to validate the "iss" claim in the presented JWT.
    # This is for scenarios where the well-known and jwks endpoints are hosted at a different
    # location than the issuer (such as locally in the cluster).
    # discoveryURL must be different from url if specified and must be unique across all authenticators.
    discoveryURL: https://discovery.example.com/.well-known/openid-configuration
    # PEM encoded CA certificates used to validate the connection when fetching
    # discovery information. If not set, the system verifier will be used.
    # Same value as the content of the file referenced by the --oidc-ca-file flag.
    certificateAuthority: <PEM encoded CA certificates>
    # audiences is the set of acceptable audiences the JWT must be issued to.
    # At least one of the entries must match the "aud" claim in presented JWTs.
    audiences:
    - my-app # Same as --oidc-client-id.
    - my-other-app
    # this is required to be set to "MatchAny" when multiple audiences are specified.
    audienceMatchPolicy: MatchAny
  # rules applied to validate token claims to authenticate users.
  claimValidationRules:
    # Same as --oidc-required-claim key=value.
  - claim: hd
    requiredValue: example.com
    # Instead of claim and requiredValue, you can use expression to validate the claim.
    # expression is a CEL expression that evaluates to a boolean.
    # all the expressions must evaluate to true for validation to succeed.
  - expression: 'claims.hd == "example.com"'
    # Message customizes the error message seen in the API server logs when the validation fails.
    message: the hd claim must be set to example.com
  - expression: 'claims.exp - claims.nbf <= 86400'
    message: total token lifetime must not exceed 24 hours
  claimMappings:
    # username represents an option for the username attribute.
    # This is the only required attribute.
    username:
      # Same as --oidc-username-claim. Mutually exclusive with username.expression.
      claim: "sub"
      # Same as --oidc-username-prefix. Mutually exclusive with username.expression.
      # if username.claim is set, username.prefix is required.
      # Explicitly set it to "" if no prefix is desired.
      prefix: ""
      # Mutually exclusive with username.claim and username.prefix.
      # expression is a CEL expression that evaluates to a string.
      #
      # 1.  If username.expression uses 'claims.email', then 'claims.email_verified' must be used in
      #     username.expression or extra[*].valueExpression or claimValidationRules[*].expression.
      #     An example claim validation rule expression that matches the validation automatically
      #     applied when username.claim is set to 'email' is 'claims.?email_verified.orValue(true) == true'.
      #     By explicitly comparing the value to true, we let type-checking see the result will be a boolean, and
      #     to make sure a non-boolean email_verified claim will be caught at runtime.
      # 2.  If the username asserted based on username.expression is the empty string, the authentication
      #     request will fail.
      expression: 'claims.username + ":external-user"'
    # groups represents an option for the groups attribute.
    groups:
      # Same as --oidc-groups-claim. Mutually exclusive with groups.expression.
      claim: "sub"
      # Same as --oidc-groups-prefix. Mutually exclusive with groups.expression.
      # if groups.claim is set, groups.prefix is required.
      # Explicitly set it to "" if no prefix is desired.
      prefix: ""
      # Mutually exclusive with groups.claim and groups.prefix.
      # expression is a CEL expression that evaluates to a string or a list of strings.
      expression: 'claims.roles.split(",")'
    # uid represents an option for the uid attribute.
    uid:
      # Mutually exclusive with uid.expression.
      claim: 'sub'
      # Mutually exclusive with uid.claim
      # expression is a CEL expression that evaluates to a string.
      expression: 'claims.sub'
    # extra attributes to be added to the UserInfo object. Keys must be domain-prefix path and must be unique.
    extra:
      # key is a string to use as the extra attribute key.
      # key must be a domain-prefix path (e.g. example.org/foo). All characters before the first "/" must be a valid
      # subdomain as defined by RFC 1123. All characters trailing the first "/" must
      # be valid HTTP Path characters as defined by RFC 3986.
      # k8s.io, kubernetes.io and their subdomains are reserved for Kubernetes use and cannot be used.
      # key must be lowercase and unique across all extra attributes.
    - key: 'example.com/tenant'
      # valueExpression is a CEL expression that evaluates to a string or a list of strings.
      valueExpression: 'claims.tenant'
  # validation rules applied to the final user object.
  userValidationRules:
    # expression is a CEL expression that evaluates to a boolean.
    # all the expressions must evaluate to true for the user to be valid.
  - expression: "!user.username.startsWith('system:')"
    # Message customizes the error message seen in the API server logs when the validation fails.
    message: 'username cannot used reserved system: prefix'
  - expression: "user.groups.all(group, !group.startsWith('system:'))"
    message: 'groups cannot used reserved system: prefix'
```

To set up the AuthenticationConfiguration, similarly to the previous example with OIDC flags(`--oidc-issuer-url`, `--oidc-client-id`, `--oidc-groups-claim`, `--oidc-ca-file`), you can set it in the file:

```bash
apiVersion: apiserver.config.k8s.io/v1beta1
kind: AuthenticationConfiguration
jwt:
- issuer:
    url: <url of the issuer>
    certificateAuthority: |
      <ca-file-content>
    audiences:
      - <client-id>
    audienceMatchPolicy: MatchAny
  claimMappings:
    groups:
      claim: <jwt-claim-name>
      prefix: ""
  claimValidationRules: []
  userValidationRules: []
```

To start the kcp server with the specified OIDC authentication configuration, pass the file path to the `--authentication-config` flag.

```bash
kcp start --authentication-config <auth-config-file-path>
```
