---
description: >
  Guidance on how to design, deploy, and consume WorkspaceTypes safely.
---

# WorkspaceType Best Practices

`WorkspaceType` is a powerful extension point in kcp, but it is also very
easily misused. The core reason is that a `WorkspaceType` is
**not a 1:1 model of a workspace** — a single type is referenced by many
workspaces, lives across replication boundaries, and sits on the critical
path of workspace creation whenever it carries blocking initializers.

This page captures recommendations that complement the mechanics described
in [Workspace Types](./workspace-types.md),
[Workspace Initialization](./workspace-initialization.md), and
[Workspace Termination](./workspace-termination.md). Read those first — this
page assumes you already know *how* the pieces work and focuses on *when*
and *whether* to use them.

## When to Use a WorkspaceType

Think of a `WorkspaceType` as a **static schema for a workspace**, not as a
runtime provisioning mechanism. It is a good fit for:

- Declaring the shape of a class of workspaces (allowed parents and
  children, default child type, inherited behavior through `extend.with`).
- One-time structural setup that genuinely must be in place before the
  workspace becomes usable to end users.
- Cross-cutting policy that has to be enforced at creation time and cannot
  be retrofitted later.

It is **not** a good fit for:

- Provisioning application-level resources on behalf of a tenant (quotas,
  default namespaces, day-2 config objects). Model these with regular
  controllers that reconcile provider-owned config CRs instead.
- Anything that might need to change per-workspace. `WorkspaceType` objects
  are referenced, not copied — mutating one mutates the contract for every
  workspace that points at it.
- Work that can just as easily be done asynchronously after the workspace
  is ready. If the workspace does not *functionally* require the resource
  to exist before it is handed to the user, it should not block creation.

Rule of thumb: if you are tempted to put business logic behind an
initializer, the workspace probably does not need the initializer at all.
Provision asynchronously and let the consumer observe readiness through a
status field on their own resource.

## Initialization Best Practices

Initializers turn workspace creation into a distributed rendezvous. Every
controller that owns an initializer is, for as long as it holds the
initializer, a hard dependency of `Workspace.status.phase = Ready`.

### Prefer non-blocking patterns

Most "initialization" work does not actually need to block the workspace
from becoming ready. Prefer this order of choices:

1. **No initializer at all.** Have your controller watch for new
   `LogicalCluster` objects (or workspace-scoped resources it cares about)
   and reconcile asynchronously. The workspace becomes `Ready` immediately
   and the user observes provisioning progress on your own CR's status.
2. **Initializer used only for truly blocking invariants.** Something the
   workspace is *broken* without — for example, a `ClusterRoleBinding` that
   grants the workspace owner any access at all, or assigning a cost center
   to correctly assign costs generated from the workspace.
3. **Avoid chaining multiple blocking initializers from different teams on
   the same type.** Each one becomes a single point of failure for every
   workspace of that type. If any controller is down or slow, every new
   workspace of that type stalls.

### When you do need an initializer

- Keep the initializer controller's reconcile loop **fast and idempotent**.
  It sits on the critical path of workspace creation; a p99 of several
  seconds multiplies across bursts.
- Make the controller **highly available**. It is infrastructure for
  every workspace of this type, not a tenant-scoped component.
- Use the
  [`initializingworkspaces` virtual workspace](./workspace-initialization.md#the-initializingworkspaces-virtual-workspace)
  and patch-based updates — do not try to `Update` `LogicalCluster` objects.
- Treat the set of initializers on a type as **append-only from the user's
  perspective**. Adding a new blocking initializer to an existing type
  changes the readiness contract for workspaces already in flight.

### Anti-patterns

- Using initializers to run long-running bootstrap processes.
- Using initializers to call out to external systems whose latency or
  availability you do not control.
- Using initializers when a post-creation reconciler would do.
- Emitting user-visible errors from inside an initializer — the user sees
  their workspace stuck in `Initializing` with no actionable signal.

## Termination Best Practices

Terminators are the counterpart to initializers and carry the same risk
profile in reverse: every terminator is a hard dependency of the workspace
actually going away. A stuck terminator is a stuck workspace deletion.

- Prefer regular Kubernetes **finalizers on workspace-scoped resources**
  for cleanup that is internal to the workspace. Terminators should only
  run logic that must happen *outside* the workspace (for example, freeing
  an external account, releasing a provider-side record).
- Terminators run with **no guaranteed ordering** relative to each other
  or to in-workspace finalizers. Do not rely on another controller having
  already cleaned up a particular resource.
- Make the cleanup logic **idempotent and side-effect-safe on retry** —
  terminators can and will run multiple times.
- Have a clear plan for **what happens if the external system is
  unreachable**. A terminator that blocks forever on a 500 from a
  downstream API leaks workspace objects indefinitely.
- Emit a status condition the operator can see. "Stuck in terminating" is
  the single most common workspace-lifecycle support request; make it easy
  to triage.

## 1:N Reuse and Schema Evolution

A `WorkspaceType` is a **shared contract**. A single `WorkspaceType` object
is typically referenced by every workspace of that type, across shards and
across replication boundaries. That has two consequences that are easy to
miss.

### Mutations have wide blast radius

Changes to a `WorkspaceType` are not versioned per-workspace. Adding an
initializer, tightening `limitAllowedChildren`, or extending another type
immediately changes behavior for:

- every in-flight workspace that has not yet finished initializing, and
- every future workspace of that type.

Existing workspaces that are already `Ready` are not re-initialized, so the
result is a fleet where old and new workspaces behave differently despite
sharing a type name. Treat the set of initializers and constraints on a
published `WorkspaceType` as part of its public API.

### Replication amplifies the effect

When a `WorkspaceType` is replicated, the same object is consumed by many
consumers that do not coordinate with you. Breaking changes are effectively
unshippable without a coordinated rollout:

- Introduce new behavior under a **new type** (for example,
  `org-v2`) rather than mutating `org` in place, whenever the change alters
  the readiness contract.
- Use `extend.with` to share behavior between the old and new type during
  the transition, so you are not duplicating the whole definition.
- Give consumers time to migrate before retiring the old type.

### Do not encode per-workspace configuration in the type

Because the type is shared, anything that varies per workspace (quotas,
cost center, feature flags, account IDs) must live on a **provider-owned
CR** inside or next to the workspace — not on the `WorkspaceType` itself.
The type says "this is an organization workspace"; a separate
`Organization` CR says "...and specifically, it's for team X with quota Y".

## Provider Bootstrapping Without Initializers

Providers can only reconciler workspaces they see through their virtual
workspaces, that means the workspaces require the APIBinding.

Solving this issue by giving each provider as a blocking initializer on
the `WorkspaceType` makes **every** provider a critical component for
workspace creation. If **one** provider runs in an issue the workspace
creation is blocked until that provider recovers.

Recommended patterns, in order of preference:

1. **Platform-level `APIBinding` propagation.** Have the platform (not each
   individual provider) ensure that newly created workspaces of a given
   type receive the set of `APIBindings` they are expected to have. Once
   the binding exists, the provider's own controller can observe the
   workspace through the normal virtual-workspace mechanism and reconcile
   asynchronously — no initializer required.
2. **An init-agent pattern.** A small, generic agent runs against the
   `initializingworkspaces` virtual workspace of a type and applies a
   declaratively configured set of objects (typically `APIBindings`,
   `ClusterRoleBindings`, and similar structural resources) into the
   workspace before removing the initializer. The
   [`kcp-dev/init-agent`](https://github.com/kcp-dev/init-agent) project
   implements this: you describe *what* must exist in a new workspace,
   and the agent handles the initializer lifecycle. This consolidates
   blocking work into one well-understood controller instead of
   spreading it across every provider.

    !!! warning
        `init-agent` is a **no-code convenience** on top of the standard
        initializer mechanism — not a scalability story. It does not
        remove any of the architectural concerns described earlier on
        this page: the initializer still sits on the critical path of
        workspace creation, it is still a hard dependency of
        `phase=Ready`, it still fans out 1:N across every workspace of
        the type, and the agent itself is still an HA component you are
        now responsible for operating. Using `init-agent` means you have
        chosen to pay those costs without writing a controller — not
        that you have avoided them. Before reaching for it, convince
        yourself that an initializer is genuinely required; if it is
        not, option 1 (platform-level `APIBinding` propagation with
        asynchronous reconciliation) remains the preferred pattern.
3. **Provider watches, user acts.** The consumer explicitly creates an
   `APIBinding` for the providers they want to use, and only then does
   the provider's controller start reconciling in that workspace. This
   is the **desired provider model** and should be the default: each
   provider is opt-in, consumers choose what they bind, and the platform
   stays a platform instead of becoming a monolith of pre-wired
   services.

    !!! warning
        Avoid the "every workspace binds everything" anti-pattern.
        Auto-binding every provider into every new workspace — whether
        via a blanket platform rule, a catch-all initializer, or an
        init-agent config that enumerates every known provider — is not
        a platform architecture. It re-centralizes ownership, couples
        every provider's availability to workspace creation, and makes
        the blast radius of any single provider's bug the entire fleet.
        Platform-level `APIBinding` propagation (option 1) should only
        cover the small set of bindings that are genuinely structural
        for the workspace type; everything else belongs to the consumer.

Whichever pattern you pick, the shared goal is the same: keep
provider-specific, failure-prone logic *off* the workspace-creation
critical path, and let providers do their work through normal
reconciliation once the workspace is visible to them.

## Checklist

Before shipping a new `WorkspaceType`, ask:

- [ ] Does this type describe structure, or is it secretly a provisioning
      mechanism? If the latter, redesign as a provider-owned CR.
- [ ] Does every initializer on this type represent something the
      workspace is genuinely unusable without? If not, drop it.
- [ ] Is every initializer owned by an HA controller with a fast, safe
      reconcile loop?
- [ ] Do terminators only handle external cleanup, with in-workspace
      cleanup left to Kubernetes finalizers?
- [ ] Is the set of initializers, terminators, and constraints treated as
      part of the type's public API, with a plan for breaking changes via
      a new type rather than in-place mutation?
- [ ] Is per-workspace configuration modeled on a separate CR, not on the
      `WorkspaceType` itself?
- [ ] For bootstrapping, have you considered platform-level `APIBinding`
      propagation or the init-agent pattern before reaching for a custom
      blocking initializer?
