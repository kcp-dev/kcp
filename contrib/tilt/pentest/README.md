# kcp pen-test scenario

A small "two tenants, one attacker" world for probing **privilege escalation**
and **workspace (tenant) isolation** in kcp. It gives you a low-privilege
kubeconfig you can hand to an AI/red-teamer with a concrete escalation goal, on
top of the local `make tilt-kind-up` dev cluster.

It is deliberately generic: the attacker is just a normal authenticated kcp user
with access to its own workspace and nothing else. Use it to test any isolation
or authorization property — cross-workspace reads, becoming `system:masters`,
abusing impersonation, warrants, scopes, service accounts, virtual workspaces,
etc.

## What the scenario sets up

| Workspace     | attacker's access            | contents                          |
|---------------|------------------------------|-----------------------------------|
| `:root:alpha` | `cluster-admin` (legit home) | —                                 |
| `:root:bravo` | **none**                     | `secret/crown-jewels` (the flag)  |

Identities (kubeconfigs written to the repo root):

| File                          | Identity                                  |
|-------------------------------|-------------------------------------------|
| `tilt-frontproxy.kubeconfig`  | `kcp-admin`, `system:masters` (the admin) |
| `pentest-attacker.kubeconfig` | `attacker`, group `pentest:tenants`       |

The attacker can do anything in `alpha` and, by RBAC, nothing in `bravo`. Any
path that lets it read `bravo`'s flag, enter another tenant, or gain
`system:masters` is a finding.

### Second flag: a provider tenant (optional, run after `setup.sh`)

`provider.sh` adds a third tenant that **publishes an API** and binds it into
both existing tenants — exercising kcp's `APIExport` / `APIBinding` surface. The
export also **claims `core/secrets`** (a realistic provider-overreach: "let me
manage TLS material for your widgets"), which `alpha` and `bravo` accept:

| Workspace        | attacker's access                  | contents                              |
|------------------|------------------------------------|---------------------------------------|
| `:root:provider` | **none**                           | `APIExport/widgets` (claims secrets), `secret/provider-jewels` (2nd flag) |
| `:root:alpha`    | `cluster-admin` (legit home)       | binds `widgets`, accepted secrets claim |
| `:root:bravo`    | none                               | binds `widgets`, accepted secrets claim |

The attacker is a legitimate **consumer** of the provider's `widgets` API in
`alpha`. Consuming an API must not let it act **as** the provider. The second
flag therefore has two capture paths, both findings:

1. read `secret/provider-jewels` in `:root:provider` (the provider's own flag), or
2. reach the provider's `widgets` **APIExport virtual workspace** and read
   secrets in another tenant — the same `crown-jewels` from the first scenario.
   The provider legitimately can (consumers accepted the claim); a mere consumer
   must not.

Either path — via the export/binding relationship, the export's identity or
virtual workspace, the bound API's storage, or any impersonation trick — is a
finding.

## Files

| File                              | Purpose                                                              |
|-----------------------------------|----------------------------------------------------------------------|
| `attacker-kubeconfig.yaml`        | kcp-operator `Kubeconfig` CR that mints the attacker's client cert   |
| `setup.sh`                         | Builds the workspaces, RBAC, flag, and attacker kubeconfig           |
| `attacker-creds.sh`               | Extracts the attacker's raw client cert/key for curl-level probing   |
| `provider.sh`                     | Adds the provider tenant + second flag (run after `setup.sh`)        |
| `provider-apiresourceschema.yaml` | `APIResourceSchema` for the provider's `Widget` API                  |
| `provider-apiexport.yaml`         | `APIExport` that publishes `widgets` for alpha/bravo to bind         |

## Prerequisites

- A running dev cluster from `make tilt-kind-up` (see `../README.md`), including
  the `/etc/hosts` entry so `kcp.localhost` resolves to `127.0.0.1`.
- On `PATH`: `kubectl`, `kubectl-ws` (from `bin/`), `curl`, `base64`.

## Usage

```bash
# 1. Bring up kcp (from the repo root) and wait for it to go green.
make tilt-kind-up

# 2. Build the scenario.
./contrib/tilt/pentest/setup.sh

# 3. (optional) Add the provider tenant + second flag.
./contrib/tilt/pentest/provider.sh

# 4. (optional) Pull the attacker's raw client cert/key for curl-level probing.
./contrib/tilt/pentest/attacker-creds.sh
```

`setup.sh` finishes with a sanity check confirming admin can read the flag, the
attacker can reach its home (`alpha`), and the attacker is denied in `bravo`
with no tricks. `provider.sh` likewise confirms the attacker can *consume* the
`widgets` API in `alpha` but is denied in `:root:provider`.

## The AI challenge

Hand the model **only** `pentest-attacker.kubeconfig`. Pick the prompt for the
scenario you built.

**First flag** (`setup.sh`) — tenant isolation:

> You hold `attacker.kubeconfig`, a low-privilege kcp tenant (group
> `pentest:tenants`). The kcp API is at `https://kcp.localhost:8443`. Read the
> `flag` from `secret/crown-jewels` in workspace `:root:bravo`, cross into
> another tenant's workspace, or gain `system:masters` on the shard. You only
> control your own client and HTTP requests.

**Second flag** (`provider.sh`) — APIExport / provider isolation:

> You hold `attacker.kubeconfig`, a low-privilege kcp tenant, and you are
> `cluster-admin` in your home workspace `:root:alpha`. You legitimately consume
> the provider's `widgets` API there, and your tenant accepted that export's
> permission claim on `secrets`. The kcp API is at `https://kcp.localhost:8443`.
> Consuming an API must not let you act **as** the provider. Either read the
> `flag` from `secret/provider-jewels` in `:root:provider`, or reach the
> provider's `widgets` APIExport virtual workspace and read a secret in another
> tenant (e.g. `crown-jewels` in `:root:bravo`). Abuse the APIExport/APIBinding
> relationship, the export's identity or virtual workspace, the bound API's
> storage, or any impersonation path. You only control your own client and HTTP
> requests.

For probing beyond what `kubectl` exposes (custom headers, raw paths, verb
tunnelling, watch streams), `attacker-creds.sh` writes the attacker's client
cert/key to PEM files and prints ready-to-edit `curl` examples. You can also
`eval "$(./contrib/tilt/pentest/attacker-creds.sh -e)"` to export
`$KCP_PROXY` / `$ATTACKER_CERT` / `$ATTACKER_KEY` into your shell.

DO NOT USE ANY OF THE ADMIN KUBECONFIGS OR THE FRONT-PROXY KUBECONFIG. The attacker is only allowed to use its own identity. If you do, you will be testing the wrong thing

To access shards, you need to use SNI:
curl -sk https://theseus.kcp.localhost:8443/healthz   # -> ok
curl -sk https://root.kcp.localhost:8443/healthz      # -> ok


## How identities are minted

The kcp-operator `Kubeconfig` CR turns `spec.username` into the client-cert CN
(the kcp username) and `spec.groups` into the cert Organization values (the kcp
groups), signs it with the front proxy's client CA, and writes a ready-to-use
kubeconfig into a secret. The attacker CR sets **no** privileged groups, so the
user can authenticate but — absent an escalation — can reach only what RBAC
grants it.

To add more tenants or identities, copy `attacker-kubeconfig.yaml`, change the
name / `username` / `groups`, apply it to the host (kind) cluster, and extract
the secret the same way `setup.sh` does. To grant a user access to a workspace,
create a `ClusterRoleBinding` *inside* that workspace to `system:kcp:workspace:access`
(read) or `cluster-admin` (admin).

## Customizing the scenario

`setup.sh` is intentionally short — edit it to fit your test:

- **More workspaces / tenants:** add `kubectl ws create <name>` calls.
- **Different starting privilege:** change the `ClusterRoleBinding` in step 3
  (e.g. bind `system:kcp:workspace:access` instead of `cluster-admin`, or none).
- **Different target:** change what gets planted in step 4 (a different secret,
  an APIExport, RBAC objects, a LogicalCluster, ...).
- **Sharded targets:** workspaces may land on the `root` or `theseus` shard;
  use a cross-shard target to test shard-boundary isolation.

## Cleanup

```bash
# Remove the attacker identity from the host (kind) cluster:
kubectl --context kind-kcp-tilt delete -f contrib/tilt/pentest/attacker-kubeconfig.yaml

# Remove the generated artifacts:
rm -rf pentest-attacker.kubeconfig pentest-attacker-creds

# The workspaces live inside kcp; drop them with the admin kubeconfig:
KUBECONFIG=tilt-frontproxy.kubeconfig kubectl ws use :root
KUBECONFIG=tilt-frontproxy.kubeconfig kubectl delete workspace alpha bravo provider
```

(`provider` only exists if you ran `provider.sh`.)

Or `kind delete cluster --name kcp-tilt` to tear the whole environment down.

## Notes

- The default Tilt install does **not** deploy dex/OIDC, so this scenario uses
  client certs. (If you wire up `contrib/kcp-dex`, you can mint OIDC-based
  attacker identities instead — the scenario is otherwise identical.)
- These files are a local testing aid and are untracked by default; `git add`
  them only if you want to commit the scenario.
