# APIExport Identity Rotation

!!! warning
    APIExport identity rotation is an **alpha** feature. The API and the
    operational behavior described here may change in incompatible ways in
    future releases.

An `APIExport`'s identity acts like a private key: whoever holds the identity
secret can serve the export's APIs and access resources claimed with it. If
the secret leaks — or organizational policy demands periodic credential
rotation — the identity must be replaceable. Rotation is more than swapping
the secret, because the identity's **hash** is load-bearing in two places:

1. **Storage**: bound resource instances are stored in etcd under a prefix
   that contains the identity hash
   (`/registry/<group>/<resource>/<identityHash>/<cluster>/...`). Changing
   the identity without moving data would orphan every existing object.
2. **Permission claims**: `APIBinding` acceptance of permission claims is
   keyed by `(group, resource, identityHash)`, both in claim labels on
   claimed objects and in the APIExport virtual workspace views.

The `APIExportIdentityRotation` API (group `migration.kcp.io`) performs a
coordinated rotation that handles both.

## Requesting a rotation

Create a rotation referencing the `APIExport` and a new identity secret that
has been pre-created **in the export's workspace** (that is where kcp
resolves export identities):

```yaml
apiVersion: migration.kcp.io/v1alpha1
kind: APIExportIdentityRotation
metadata:
  name: rotate-widgets-2026-09
spec:
  export:
    path: root:acme:widgets   # optional: defaults to the rotation's workspace
    name: widgets.example.kcp.io
  newIdentity:
    secretRef:
      namespace: kcp-system
      name: widgets-identity-2026-09
  aliasRetirement:
    policy: After
    after: 168h
```

The `migration.kcp.io` APIExport is owned by the platform and bound from
the `root` workspace, so binding it (and therefore requesting rotations) can
be restricted by the platform operator. Because `spec.export.path` may point
at another workspace (even on another shard), rotations are typically
requested from a platform-operated workspace: the workspaces owning the
rotated exports never need the rotation capability bound at all.

`spec.export` and `spec.newIdentity` are immutable. `spec.aliasRetirement`
selects when the old identity hash stops being honored as an alias:

- `Manual` (default): the alias stays until the retirement policy is changed.
- `After`: the alias retires `after` duration once all bindings migrated.
- `Immediate`: the alias retires as soon as all bindings migrated.

The policy may only be changed toward *earlier* retirement
(`Manual` → `After` → `Immediate`), and an `After` duration may only shrink.

## What happens during a rotation

The rotation controller drives `status.phase` through
`Pending → Migrating → AliasActive → Completed`:

1. **Pending**: the new secret is validated, the export is switched to the
   new identity, and the old hash is recorded as an **alias** in the export's
   `status.identityAliasHashes`. While an alias is active, permission claims
   and claim labels referencing the old hash are transparently normalized to
   the new (canonical) hash, so consumers that pin the old hash keep working.
2. **Migrating**: on every shard, the identity migrator drains each affected
   `APIBinding`:
      - the consumer workspace is fenced with the `core.kcp.io/inactive`
        annotation (requests are rejected and connections cut, exactly like
        during workspace migration),
      - all instances are copied byte-for-byte from the old identity's etcd
        prefix onto the new one and the copy is verified,
      - the binding's serving identity is flipped and the bound CRD is
        recreated so serving storage is rebuilt against the new prefix,
      - the old prefix is deleted and the fence lifted.
      Object UIDs, resourceVersions semantics, status, and ownerReferences
      survive unchanged; consumers observe a short unavailability window
      while their workspace is fenced.
3. **AliasActive**: all bindings tracked by the rotation have migrated. The
   alias remains honored per `spec.aliasRetirement`.
4. **Completed**: the alias is retired; the old identity is fully invalid.

Progress is reported in `status.migratedBindings` / `status.totalBindings`,
and per-binding in each `APIBinding`'s `IdentityMigrationCompleted`
condition and `status.boundResources[].identityHashes` bookkeeping (which
lists every hash still holding data for that resource until the drain is
verified, making the migrator crash-resumable).

## Safety invariants

Admission enforces:

- at most one active rotation per export,
- a cooldown (1h) between completed rotations of the same export, bounding
  how often consumers can be fenced,
- spec immutability except the retirement policy, which only moves earlier.

## Alpha limitations

- `status.migratedBindings`/`totalBindings` only counts bindings on the
  shard hosting the rotation (each shard's migrator acts independently and
  correctly, but cross-shard progress is not aggregated yet).
- The bound CRD serving rebuild is per shard and shared by all bindings of a
  schema; bindings of the same export on one shard migrate sequentially, and
  a not-yet-migrated binding may observe a brief `NotFound` window while the
  serving storage is rebuilt.
- Rotating an identity that other exports claim (`identityHash` in their
  permission claims) relies on alias normalization; once the alias is
  retired, claims still pinning the old hash stop matching until updated.
- The admission invariants (one active rotation per export, rotation
  cooldown) are enforced among the rotations of one workspace. Rotations for
  the same export requested from different workspaces are not deduplicated;
  keep the rotation capability scoped to a single ops workspace per export.
