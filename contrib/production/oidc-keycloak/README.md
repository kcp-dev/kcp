# Keycloak OIDC Provider for kcp

This directory contains example Kubernetes manifests for deploying Keycloak as an OIDC provider for kcp.

## Prerequisites

- Kubernetes cluster with kubectl access
- [cert-manager](https://cert-manager.io/) installed
- [CloudNativePG](https://cloudnative-pg.io/) operator installed
- [Keycloak Operator](https://www.keycloak.org/operator/installation) installed

## Files

| File | Description |
|------|-------------|
| `postgres-cluster.yaml` | CloudNativePG PostgreSQL cluster for Keycloak backend |
| `postgres-database.yaml` | Database resource for the PostgreSQL cluster |
| `keycloak-db-secret.yaml` | Secret for Keycloak to connect to PostgreSQL |
| `certificate-dns.yaml` | cert-manager Certificate for TLS |
| `keycloak.yaml` | Keycloak CRD deployment |
| `values.yaml.template` | Template showing all configuration options |

## Deployment

1. Create the namespace:
   ```sh
   kubectl create namespace oidc
   ```

2. Deploy PostgreSQL:
   ```sh
   kubectl apply -f postgres-cluster.yaml
   kubectl apply -f postgres-database.yaml
   ```

3. Wait for PostgreSQL to be ready:
   ```sh
   kubectl wait --for=condition=Ready cluster/keycloak-auth -n oidc --timeout=300s
   ```

4. Create the database secret for Keycloak:
   ```sh
   kubectl apply -f keycloak-db-secret.yaml
   ```

5. Create the TLS certificate:
   ```sh
   kubectl apply -f certificate-dns.yaml
   ```

6. Wait for the certificate to be issued:
   ```sh
   kubectl wait --for=condition=Ready certificate/keycloak-tls-cert -n oidc --timeout=300s
   ```

7. Deploy Keycloak:
   ```sh
   kubectl apply -f keycloak.yaml
   ```

8. Wait for Keycloak to be ready:
   ```sh
   kubectl get keycloaks/keycloak -n oidc -w
   ```

## Post-Deployment Configuration

After Keycloak is running:

1. Get the initial admin credentials:
   ```sh
   kubectl get secret keycloak-initial-admin -n oidc -o jsonpath='{.data.username}' | base64 -d
   kubectl get secret keycloak-initial-admin -n oidc -o jsonpath='{.data.password}' | base64 -d
   ```

2. Access the admin console at your configured hostname (e.g., `https://auth.keycloak.example.com/admin`)

3. Create a realm for kcp (e.g., `kcp`)

4. Create a client for kcp with:
   - Client ID: `kcp`
   - Public client: Yes
   - Valid redirect URIs: `http://localhost:8000`, `http://127.0.0.1:8000/`

## Security Notes

- Change all default passwords before production use
- Enable MFA for the admin account
- Review and restrict redirect URIs as needed

## Documentation

For more details, see the [Keycloak setup guide](../../../docs/content/setup/keycloak.md).
