# APIExport Storage Re-keying

!!! warning
    APIExport storage re-keying is an **alpha** feature. The API and the
    operational behavior described here may change in incompatible ways in
    future releases.

An `APIExport`'s **identity** is an opaque, unique identifier assigned once by
kcp. It is public: it appears in `status.identityHash`, in every consumer's
`APIBinding` status, in permission claims that reference the export's
resources, and in the `resource:identity` suffix of wildcard requests. It
never changes.

By default the identity is also the **storage key** of the export's bound
instances: every object a consumer creates for one of the export's resources
is stored in etcd under

```
/registry/<group>/<resource>/<storage key>/<cluster>/...
```

The two are tracked separately on each consumer's `APIBinding`, per bound
resource: `status.boundResources[].schema.identityHash` is the identity,
`status.boundResources[].storage.key` is the prefix in use. They are equal
in the steady state. They differ while an export serves a resource out of
another export's prefix (adopted storage, see
[Taking over a resource from another export](exporting-apis.md#taking-over-a-resource-from-another-export)),
and during a **storage re-keying**, which moves the bound instances of an export's
resources from the prefix they currently sit on to another one without
touching the identity.

Because the identity is untouched, nothing that references it has to be
updated: permission claims keep matching, claim labels on claimed objects keep
their values, and wildcard clients keep using the same URL while the data
moves underneath.

## Enabling the feature

Storage re-keying is behind the `APIExportStorageRekey` feature gate, which
is off by default. Enable it on every shard:

```sh
kcp start --feature-gates=APIExportStorageRekey=true
```

With the gate disabled the re-keying and migrator controllers do not run,
and creating an `APIExportStorageRekey` is rejected by admission.

## Requesting a re-keying

Create a re-keying referencing the `APIExport`:

```yaml
apiVersion: migration.kcp.io/v1alpha1
kind: APIExportStorageRekey
metadata:
  name: rekey-widgets
spec:
  export:
    path: root:acme:widgets   # optional: defaults to the re-keying's workspace
    name: widgets.example.kcp.io
  # resources: limit the re-keying to some of the export's resources.
  # Omitted: every CRD-stored resource of the export.
  resources:
  - group: example.kcp.io
    resource: widgets
  # targetKey: the storage key to drain onto. Omitted: the export's own
  # identity, which is the steady state. Any other value is reserved for
  # platform administrators.
  targetKey: ""
```

The `migration.kcp.io` APIExport is owned by the platform and bound from
the `root` workspace, so binding it (and therefore requesting re-keyings) can
be restricted by the platform operator. Because `spec.export.path` may point
at another workspace (even on another shard), re-keyings are typically
requested from a platform-operated workspace: the workspaces owning the
re-keyed exports never need the capability bound at all.

The spec is immutable.

## What happens during a re-keying

The re-keying controller drives `status.phase` through
`Pending → Draining → Completed`:

1. **Pending**: the request is validated, the target key is resolved
   (`status.targetKey`), and the export is marked with the
   `migration.kcp.io/active-rekey` annotation. The export is replicated to
   every shard with it, so every shard's `APIBinding` reconciler records the
   target as a pending storage key on the affected bindings, and every
   shard's storage migrator learns it has to drain and report.
2. **Draining**: on every shard, the storage migrator drains each affected
   `APIBinding`:
      - the consumer workspace is fenced with the `core.kcp.io/inactive`
        annotation (requests are rejected and connections cut, exactly like
        during workspace migration),
      - all instances are copied byte-for-byte from the current prefix onto
        the target one and the copy is verified,
      - the binding's `storage.key` is flipped to the target and the bound
        CRD is recreated so serving storage is rebuilt against the new
        prefix (the apiextensions cleanup finalizer is stripped from the
        stale CRD: for a bound CRD it never deletes instances, and the
        instances are exactly the data that was just moved),
      - the old prefix is deleted, every instance is rewritten in place once
        under the new prefix (so wildcard watchers, which see all prefixes
        of a resource as one keyspace, observe the move as an update rather
        than an add followed by a delete), and the fence is lifted once no
        other binding in the same workspace is still draining (the fence
        is shared by all bindings of a workspace).
      Object UIDs, status, and ownerReferences survive unchanged; consumers
      observe a short unavailability window while their workspace is fenced.
3. **Completed**: every shard has reported all of its bindings migrated; the
   marker is removed from the export.

Progress is reported per shard in `status.shards`: each shard's storage
migrator counts the bindings of the re-keyed export it hosts and maintains its
own entry via server-side apply. Shards without any binding of the export
report a `0/0` entry, so the controller can tell "not reported yet" from
"nothing to drain". Each entry records the storage key it was evaluated
against (`storageKey`); entries evaluated against any other key are ignored.
`status.migratedBindings` / `status.totalBindings` are the sums over all valid
shard entries, and the `Drained` condition only becomes true once every shard
known to the cache server has reported a fully migrated entry.

Per-binding progress lives in each `APIBinding`'s `StorageRekeyCompleted`
condition and `status.boundResources[].storage.pendingKeys` bookkeeping, which
lists every prefix that may still hold data for the resource until the drain
is verified, making the migrator crash-resumable.

## Why no identity rotation

Earlier designs rotated the identity itself. Because the identity was also
the storage key, any rotation was a data migration, and because every
permission claim and wildcard client embedded it, any rotation broke
references until they were updated. Decoupling the storage key from the
identity removes the reason to rotate: the identity carries no secret and
grants nothing by itself, so there is nothing to leak and nothing to
replace. Only data ever moves, and only when a storage handover between
exports has to end.

## Safety invariants

Admission enforces:

- at most one active re-keying per export,
- a cooldown (1h, counted from when the previous re-keying completed its
  drain) between re-keyings of the same export, bounding how often consumers
  can be fenced; deleting the completed re-keying lifts the cooldown,
- spec immutability,
- no deletion of a re-keying while it is `Draining`: the export's marker is
  cleared through the re-keying object.

## Alpha limitations

- Shard entries in `status.shards` are trusted as reported and carry no
  staleness detection: a shard that stops reporting mid-drain (e.g. it went
  down) blocks the re-keying in `Draining` until it returns or is removed
  from the fleet. `lastUpdateTime` on each entry makes this observable.
- Every storage key a schema is served from on a shard gets a bound CRD of
  its own in `system:bound-crds` (the schema UID for the export's identity,
  a per-key variant otherwise), so bindings of different exports, or of one
  export mid re-keying, may sit on different storage keys on one shard. A
  binding switches CRDs when its storage key flips, and may observe a brief
  `NotFound` window while the serving storage for the new key is
  established. The CRD of a drained-from key is deleted as soon as no
  binding on the shard serves from it, which ends watches still streaming
  from the old prefix; the target prefix is then rewritten once more so
  that such watches, resuming on the new storage, see every object again.
- The admission invariants (one active re-keying per export, cooldown) are
  enforced among the re-keyings of one workspace. Re-keyings for the same
  export requested from different workspaces are not deduplicated; keep the
  capability scoped to a single ops workspace per export.
