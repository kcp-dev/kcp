# Frequently Asked Questions

## What is the elevator pitch, why would I want to use kcp?

kcp is a highly-multi-tenant Kubernetes control-plane, built for SaaS service-providers who want to offer a unified API-driven platform to many customers and dramatically reduce a) cost of operation of the platform and b) cost of onboarding of new services. It is made to scale to the order of magnitude of 10s of thousands of customers, offer a Kubernetes-like experience including execution of workloads, but with dramatically lower overhead per new customer.

## What is a ....

Check out our [concepts](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md) document and feel free to open an issue if something is not covered.


## If kcp is a Kubernetes API server without pod-like APIs, how do resources like Deployments get scheduled?

kcp has a concept called [syncer](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#syncer) which is installed on each [SyncTarget](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#workload-cluster). The [syncer](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#syncer) negotiates, with kcp, a set of APIs to make accessible in the workspace. This may include things like [Deployments](https://kubernetes.io/docs/concepts/workloads/controllers/deployment/) or other resources you may explicitly configure the syncer to synchronize to kcp. Once these APIs are made available in your [Workspace](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#workspace) you may then create resources of that type. From there, the [Location and Placement](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#location) APIs help determine which [Location](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#location) your deployable resource lands on.

## Will KCP be able to pass the K8S conformance tests in [CNCF Conformance Suites](https://www.cncf.io/certification/software-conformance/)?

No, the Kubernets conformance suites require that all Kubernetes APIs are supported and kcp does not support all APIs out of the box (for instance, Pods).

## Are these ideas being presented to the Kubernetes community?

Yes! All development is public and we have started discussions about what is mature enough to present to various interested parties. It is worth noting that not all of the goals of kcp are necessarily part of the goals of Kubernetes so while some items may be accepted upstream we expect that at least some of the concepts in kcp will live outside the Kubernetes repository.

## How does upgrading Kubernetes work in this model?

kcp depends on a [fork](https://github.com/kcp-dev/kubernetes) of Kubernetes. Updating Kubernetes for the kcp project itself requires a rebase. We are actively following the releases of Kubernetes and rebasing regularly. Updating Kubernetes for clusters attached to kcp is exactly like it is done today, though you may choose to follow different patterns of availability for applications based on kcp's ability to cordon and drain clusters or relocate applications.

## Can kcp workloads run on MiniKube clusters?

Yes.

## How will storage work at the workspace level for administration?

We are in the early stages of [brainstorming storage use cases](https://docs.google.com/document/d/13VpnyBQHpaishrastdO3kGApKLzYBeb9QXdKn3o2vHs/edit#heading=h.tg51mxx1tg19). Please join in the conversation if you have opinions or use cases in this areas.

## With multiple `Workspaces` on a single cluster, that implies `Pods` from multiple tenants are on the same host VM. Does this mean privileged `Pods` are forbidden to avoid cross contamination with host ports and host paths?

We aren't quite there yet. Security controls are especially important at the multi-tenant level and we'd love to hear your use cases in this area.

## Do the service provider operators/controllers live outside of kcp?

Controller patterns are something we are actively working on defining better.  In general, operators and controllers from a service providing team would run on their provided compute (or shared compute) and point back to kcp in order to have a view of the resources being created that they need to take action upon. This view would be via a [Virtual Workspace](https://github.com/kcp-dev/kcp/blob/main/docs/virtual-workspaces.md).

## Can I get the logs of a pod deployed via kcp? Can I attach to a pod or exec to a pod?

Yes. We are tracking [read-through of resources](https://github.com/kcp-dev/kcp/issues/25) and [debugging](https://github.com/kcp-dev/kcp/issues/521) as use cases we need to support. You can view a demo of the current work in our [April 26 Community Call Recording](https://www.youtube.com/watch?v=joR39RR2Gwo).

## Are workspaces hierarchical?

[Workspaces](https://github.com/kcp-dev/kcp/blob/main/docs/workspaces.md) can contain other workspaces and workspaces are typed. Please see the [Workspace documentation](https://github.com/kcp-dev/kcp/blob/main/docs/workspaces.md) for more details.

## Are custom admission controllers considered? How would that work across clusters if api server and the actual service is located elsewhere?

Yes. [Validating and mutating webhooks](https://github.com/kcp-dev/kcp/pull/818) via an external URL are supported.

## kcp hides nodes - does it mean that Pod does not have a nodeName field set?

They do, in the [workload clusters](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#workload-cluster) where the pods live and run. The control plane doesn't have pods (at least not by default, today).

## Let’s take something boring like FIPS compliance. Would a workspace be guaranteed to run accordingly to the regulatory standards? Ie a workspace admin defined some FIPS stuffs and kcp ensures that the resulting pods do run appropriate in the FIPS shard?

In kcp an application should be able to describe the constraints it needs in its runtime environment. This may be technical requirements like GPU or storage, it may be regulatory like data locality or FIPS, or it may be some other cool thing we haven't thought of yet. kcp expects the integration with [Location and Placement](https://github.com/kcp-dev/kcp/blob/main/docs/concepts.md#location) APIs to handle finding the right placement that fulfills those requirements.

## Could you define a 'shard' in the context of kcp?

Shards in kcp represent a single apiserver and etcd/db instance.  This is how kcp would like to split workspaces across many kcp instances since etcd will have storage limits.

## Where can I get the kubectl workspace plugin?

You're in the right place. Clone this repo and run `make install WHAT=./cli/cmd/kubectl-kcp`.

