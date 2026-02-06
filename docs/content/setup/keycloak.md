---
description: >
  Deploy Keycloak as an OIDC provider for kcp authentication.
---

# Keycloak

Keycloak is an open-source identity and access management solution aimed at modern applications and services. It provides features such as single sign-on (SSO), user federation, identity brokering, social login, and more. Keycloak can be integrated with kcp to provide authentication and authorization services for users accessing kcp resources.

This guide describes how to deploy Keycloak on Kubernetes using the Keycloak Operator for use with kcp.

## Prerequisites

Before deploying Keycloak, ensure you have the following:

- A running Kubernetes cluster
- `kubectl` configured to access your cluster
- [cert-manager](https://cert-manager.io/) installed (for TLS certificates)
- [CloudNativePG](https://cloudnative-pg.io/) operator installed (for PostgreSQL database)

## Deployment Steps

### 1. Create the Namespace

```sh
kubectl create namespace oidc
```

### 2. Install the Keycloak Operator

Install the Keycloak Operator in the `oidc` namespace. By default, the operator only watches the namespace it's deployed in.

```sh
# Install the Keycloak Operator CRDs and resources in the oidc namespace
kubectl -n oidc apply -f https://raw.githubusercontent.com/keycloak/keycloak-k8s-resources/26.0.5/kubernetes/keycloaks.k8s.keycloak.org-v1.yml
kubectl -n oidc apply -f https://raw.githubusercontent.com/keycloak/keycloak-k8s-resources/26.0.5/kubernetes/keycloakrealmimports.k8s.keycloak.org-v1.yml
kubectl -n oidc apply -f https://raw.githubusercontent.com/keycloak/keycloak-k8s-resources/26.0.5/kubernetes/kubernetes.yml

# Patch the ClusterRoleBinding to use the oidc namespace
kubectl patch clusterrolebinding keycloak-operator-clusterrole-binding \
  --type='json' \
  -p='[{"op": "replace", "path": "/subjects/0/namespace", "value":"oidc"}]'
```

Wait for the operator to be ready:

```sh
kubectl wait --for=condition=Available deployment/keycloak-operator -n oidc --timeout=120s
```

### 3. Deploy PostgreSQL Database

Keycloak requires a database backend. We use CloudNativePG to provision PostgreSQL.

Create the database secrets and cluster:

```yaml
# postgres-cluster.yaml
---
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: keycloak-auth
  namespace: oidc
spec:
  instances: 1
  bootstrap:
    initdb:
      database: keycloak
      owner: keycloak
      secret:
        name: keycloak-postgres
  enableSuperuserAccess: true
  superuserSecret:
    name: keycloak-superuser
  storage:
    size: 10Gi
---
apiVersion: v1
data:
  username: a2V5Y2xvYWs=  # keycloak
  password: <base64-encoded-password>
kind: Secret
metadata:
  namespace: oidc
  name: keycloak-postgres
type: kubernetes.io/basic-auth
---
apiVersion: v1
data:
  username: cG9zdGdyZXM=  # postgres
  password: <base64-encoded-password>
kind: Secret
metadata:
  namespace: oidc
  name: keycloak-superuser
type: kubernetes.io/basic-auth
```

Apply the configuration:

```sh
kubectl apply -f postgres-cluster.yaml
```

Optionally, create a separate Database resource:

```yaml
# postgres-database.yaml
apiVersion: postgresql.cnpg.io/v1
kind: Database
metadata:
  namespace: oidc
  name: db-keycloak
spec:
  name: keycloak
  owner: keycloak
  cluster:
    name: keycloak-auth
```

### 4. Create TLS Certificate

Use cert-manager to provision a TLS certificate for Keycloak:

```yaml
# certificate-dns.yaml
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: keycloak-tls-cert
  namespace: oidc
spec:
  secretName: keycloak-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
    group: cert-manager.io
  dnsNames:
  - auth.example.com  # Replace with your domain
  usages:
  - digital signature
  - key encipherment
```

Apply the certificate:

```sh
kubectl apply -f certificate-dns.yaml
```

Wait for the certificate to be issued:

```sh
kubectl get certificate -n oidc keycloak-tls-cert -w
```

### 5. Create Database Credentials Secret

Create a secret for Keycloak to access the database:

```sh
kubectl create secret generic keycloak-db-secret \
  --namespace oidc \
  --from-literal=username=keycloak \
  --from-literal=password=cGFzc3dvcmQ=
```

### 6. Deploy Keycloak

Create the Keycloak Custom Resource. This example disables the default Ingress and uses a separate LoadBalancer Service:

```yaml
# keycloak.yaml
apiVersion: k8s.keycloak.org/v2alpha1
kind: Keycloak
metadata:
  name: keycloak
  namespace: oidc
spec:
  instances: 1
  db:
    vendor: postgres
    host: keycloak-auth-rw.oidc.svc.cluster.local
    usernameSecret:
      name: keycloak-db-secret
      key: username
    passwordSecret:
      name: keycloak-db-secret
      key: password
  http:
    tlsSecret: keycloak-tls
  hostname:
    hostname: auth.example.com  # Replace with your domain
  proxy:
    headers: xforwarded
  ingress:
    enabled: false  # Disable default Ingress, we'll use LoadBalancer
```

Create a LoadBalancer Service to expose Keycloak:

```yaml
# keycloak-service-lb.yaml
apiVersion: v1
kind: Service
metadata:
  name: keycloak-lb
  namespace: oidc
spec:
  type: LoadBalancer
  selector:
    app: keycloak
    app.kubernetes.io/instance: keycloak
  ports:
  - name: https
    port: 443
    targetPort: 8443
    protocol: TCP
```

Apply both resources:

```sh
kubectl apply -f keycloak.yaml
kubectl apply -f keycloak-service-lb.yaml
```

Get the LoadBalancer external IP:

```sh
kubectl get svc keycloak-lb -n oidc -w
```

Apply the Keycloak deployment:

```sh
kubectl apply -f keycloak.yaml
```

### 7. Verify Deployment

Check the status of the Keycloak deployment:

```sh
kubectl get keycloaks/keycloak -n oidc -o go-template='{{range .status.conditions}}CONDITION: {{.type}}{{"\n"}}  STATUS: {{.status}}{{"\n"}}  MESSAGE: {{.message}}{{"\n"}}{{end}}'
```

When ready, you should see:

```
CONDITION: Ready
  STATUS: true
  MESSAGE:
CONDITION: HasErrors
  STATUS: false
  MESSAGE:
CONDITION: RollingUpdate
  STATUS: false
  MESSAGE:
```

## Accessing the Admin Console

The Keycloak Operator generates initial admin credentials stored in a Secret:

```sh
# Get the admin username
kubectl get secret keycloak-initial-admin -n oidc -o jsonpath='{.data.username}' | base64 --decode

# Get the admin password
kubectl get secret keycloak-initial-admin -n oidc -o jsonpath='{.data.password}' | base64 --decode
```

Access the admin console at `https://auth.example.com/admin`.

!!! warning "Security"
    Change the default admin credentials and enable MFA before using in production.

## Configuring Keycloak for kcp

After Keycloak is running, configure it for kcp authentication:

### 1. Create a Realm

1. Log in to the Keycloak Admin Console
2. Create a new realm (e.g., `kcp`)
3. Configure the realm settings as needed

### 2. Create a Client for kcp

1. Navigate to **Clients** in your realm
2. Click **Create client**
3. Configure the client:
   - **Client ID**: `kcp`
   - **Client authentication**: Off (for public clients like CLI tools)
   - **Valid redirect URIs**:
     - `http://localhost:8000`
     - `http://127.0.0.1:8000/`
   - **Web origins**: `+`

### 3. Configure Identity Providers (Optional)

To enable social login (e.g., GitHub):

1. Navigate to **Identity providers**
2. Add a new provider (e.g., GitHub)
3. Configure the provider with your OAuth app credentials

## Configuring kcp to Use Keycloak

Configure kcp to use Keycloak as the OIDC provider by setting the following flags:

```sh
kcp start \
  --oidc-issuer-url=https://auth.example.com/realms/kcp \
  --oidc-client-id=kcp \
  --oidc-username-claim=preferred_username \
  --oidc-groups-claim=groups
```

Or via Helm values:

```yaml
kcp:
  oidc:
    enabled: true
    issuerURL: https://auth.example.com/realms/kcp
    clientID: kcp
    usernameClaim: preferred_username
    groupsClaim: groups
```

## Configuring kubectl for OIDC

To authenticate with kcp using Keycloak, install the [kubelogin](https://github.com/int128/kubelogin) plugin:

```sh
kubectl krew install oidc-login
```

Configure kubectl credentials using the command line:

```sh
kubectl config set-credentials oidc \
  --exec-api-version=client.authentication.k8s.io/v1beta1 \
  --exec-command=kubectl \
  --exec-arg=oidc-login \
  --exec-arg=get-token \
  --exec-arg=--oidc-issuer-url=https://auth.example.com/realms/kcp \
  --exec-arg=--oidc-client-id=kcp \
  --exec-arg=--oidc-extra-scope=email
```

Or manually edit your kubeconfig:

```yaml
users:
- name: oidc
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: kubectl
      args:
      - oidc-login
      - get-token
      - --oidc-issuer-url=https://auth.example.com/realms/kcp
      - --oidc-client-id=kcp
      - --oidc-extra-scope=email
```

Then set your context to use the OIDC credentials:

```sh
kubectl config set-context --current --user=oidc
```

## Custom Ingress Configuration

If the default ingress does not fit your use case, disable it and create your own:

```yaml
apiVersion: k8s.keycloak.org/v2alpha1
kind: Keycloak
metadata:
  name: keycloak
  namespace: oidc
spec:
  # ... other configuration
  ingress:
    enabled: false
```

Then create your own Ingress resource pointing to the `keycloak-service` service.

## Troubleshooting

### Check Keycloak Pods

```sh
kubectl get pods -n oidc -l app=keycloak
kubectl logs -n oidc -l app=keycloak
```

### Check Database Connectivity

```sh
kubectl get pods -n oidc -l cnpg.io/cluster=keycloak-auth
```

### Port Forward for Local Access

For debugging, you can port-forward to the Keycloak service:

```sh
kubectl port-forward -n oidc service/keycloak-service 8443:8443
```

Then access Keycloak at `https://localhost:8443`.

## Reference Files

Example configuration files are available in the kcp repository:

- [contrib/production/oidc-keycloak/](https://github.com/kcp-dev/kcp/tree/main/contrib/production/oidc-keycloak)

## Additional Resources

- [Keycloak Documentation](https://www.keycloak.org/documentation)
- [Keycloak Operator Guide](https://www.keycloak.org/operator/basic-deployment)
- [CloudNativePG Documentation](https://cloudnative-pg.io/documentation/)
