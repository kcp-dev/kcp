---
description: >
  A brain dump of thoughts behind KCP's architecture.
---

# Architecture – A Brain Dump

!!! note
    This document is a brain dump of thoughts behind KCP's architecture.
    It's a work in progress and may contain incomplete or unpolished ideas. It was
    recorded through ChatGPT (not generated), and hence might have a conversational
    tone (GPT's summarizing responses have been removed) and might contain mistakes.

## KCP Overview

KCP is an extension or a fork of the KubeAPI server, it's adding a concept
called a logical cluster, or merely it's called a workspace, a workspace concept
to one instance of KCP. When I talk about one instance, it's actually one shard,
and there can be multiple shards in a system. And the paper will be about the
architecture to make that possible while giving the user

## Every workspace looks like a Kubernetes cluster from the outside.

Every workspace looks like a Kubernetes cluster from the outside, at least when
looking at the main APIs which are independent of container management. For
example, every workspace has its own discovery information, its own OpenAPI
spec, and of course, its own API groups implementation. Every workspace has
certain kinds of APIs which are used to from Kubernetes, APIs which are like
namespaces, config maps, secrets, and many more. And it has the semantics you
would expect, like namespace deletion is implemented, garbage collection is
implemented, airbag permission management is implemented the same way as in
Kubernetes. The big difference to Kubernetes is that one instance of KCP, we
call that one shard in this context, can host an arbitrary number of workspaces,
each being logically independent.

If you look on one shard, one instance of KCP and a number of workspaces hosted
by that shard. Between workspaces, there can be interactions. Interactions in
the sense of one workspace can export APIs and another workspace can bind to
those APIs. And the objects to define those
two concepts in KCP are named like that, API export and API binding. With a
small number of exceptions, everything in KCP is implemented using those API
export, API binding concepts. Even the workspace concept itself is based on an
API export. The workspaces of KCP have a structure, like they live in KCP as a
system, as a platform. They are ordered in a hierarchy, so they are placed in a
hierarchy in a tree-like structure, similar to directories in a Linux or Windows
file system. Every directory is here a workspace. Exports and bindings connect
those. There's one very special workspace called the root workspace. The root
workspace hosts the API exports of the main KCP APIs. For example, the tendency
KCP or API group, with the workspace object, the workspace kind as a primary
type, is exported from the root workspace. So the root workspace plays a crucial
role in bootstrapping a KCP instance.

The workspace hierarchy is not established through exports and bindings. The
workspace hierarchy is established by defining a child workspace within a parent
workspace by creating a workspace object. So in this sense, this is again very
similar to a file system hierarchy in Linux.

I want to dive a little bit into the Workspace concept, what is behind it, how
it's implemented. As I described, Workspace objects within parent Workspaces
define the hierarchy. Within the hierarchy, you get a path, a path like a file
system path. In KCP, we use a colon as separator. A normal example of a path is
starting with root, colon, team name, colon, and, for example, application name.
So it's root, team A, application Z, as an example. The path is constructed and
also reconstructed just by this nesting of Workspaces. It's not inherent in how
the data is stored, stored in the storage layer in etcd or in the kind-based SQL
database. Behind the scenes, every Workspace path is mapped to a logical
cluster. A logical cluster is identified by some hash value. So there's a hash,
some random character string. And this is unique. So it's like UID. It's unique
throughout the KCP system. And that key is used within the etcd or the
kind-based key structure. So it's part of the keys in the storage. This is used
to separate values, so objects, of Workspaces which live in different logical
clusters. When you talk to KCP, you as a client from outside, so similarly as
you would talk to a kube cluster, but now you talk to a Workspace, you can talk
to it through the path, or you can talk to it through the logical cluster,
identify as this random UID string.

The mapping from a workspace path to the Logical Cluster ID is done through
reading the leaf object of the path, so the last workspace object, the last
component of the path. Inside of that, the UID of the Logical Cluster is stored.
So if you know all workspace objects in KCP, you can resolve every path which
exists which points to a Logical Cluster by going through the workspace objects
one by one up to the leaf, reading the Logical Cluster ID, and then accessing.

I want to talk about the consistency guarantees in KCP. A workspace is a main
unit of a consistent API space, like a Kubernetes cluster. So in a workspace,
you have similar guarantees as in a Kubernetes cluster, which means per object
kind, you have resource versions, which order changes to objects. Cross
workspaces, you don't necessarily have that. If you look on objects in two
workspaces, in two logical clusters technically, and you look on objects and
resource versions, there does not have to be a linear order between them. It can
be, but it doesn't have to.

The fact that objects in one KCP shard are stored in the same storage, the same
etcd or kine storage, this can be exploited by listing objects across
workspaces, across logical clusters which are stored on the same shard. There is
a request type we call a wildcard request. And that request returns objects
across logical clusters of a shard. And inside of that list, all objects, the
resource versions of those objects, they follow the same linear ordering
guarantees as in one Kubernetes cluster, which means you can wildcard list
objects for all logical clusters on a shard, and you can use that information to
back an informer, so a reflector-based informer with list and watch support. And
we can use the same informer infrastructure of Kubernetes to have informers for
one shard. Now we will go into details of a multi-shard architecture soon, but
in that case, this guarantee will not be there. This guarantee only holds for
one shard.

I want to talk about API exports a little bit in the sense of how they are
stored and what an API export actually defines. An API export is a little bit
like a CRD or multiple CRDs, so it defines resources with things like resource
name, of course, the kind name, and the list type name. If you have multiple
workspaces, in theory, each could export the same kind in the same API group. So
there's a problem of how to distinguish those. Different exports, same kind of
group. What KCP is introducing, basically adding to a CRD to an export, is a
concept called an identity of an export. The identity is a secret that the owner
of the API export knows. When you know that secret, you can export the
same object and workspaces which use a bind to that API will then be actually
bound to that export with that identity. So if you give away the secret, you
leak the secret to an attacker, in theory, that attacker could export the same
kind with the same identity and steal information. What we do to make use of the
identity, we take the hash. So it's a short 256 hash of the identity, and that
string is part of a binding. If you want to bind to an API export, you point the
binding to the export by path, or in different ways in the future, and you will
use the identity hash to make sure you really bind to the right export. The
identity hash is also used within etcd. So to make sure that the API export
owner can access objects of all tenants or users of that export, to make sure
that this owner can only see the objects belonging to the same export, again,
identified by the identity hash string. To make sure this is safe and nothing
leaks, the identity hash is part of the key in the storage. So while on the
wire, when you do requests against the workspace, you will just use the resource
name as-is, but the binding carries the identity hash, and the system will make
sure that the data, the objects are stored in the key which has identity hash as
part of it. That way, if we come back to the wildcard request, that way we can
make sure that the owner of the export can see all objects in all logic clusters
on the shard of their export with their identity hash. And the wildcard request
will include the identity hash as part of the request resource name. That way,
we can segregate wildcard requests of different exports of the same resource and
group name. That way, we get a safe system.

## API Exports Definition and Storage

An API export in KCP is similar to a CRD and defines resources by specifying
details like the resource name, kind name, and list type name. In scenarios
where multiple workspaces could export the same kind in the same API group, KCP
introduces a way to distinguish between different exports of the same kind and
group.

### Identity of an Export

KCP adds a concept called the identity of an export. This identity is
essentially a secret known only to the owner of the API export. This secret,
when known, allows for the exporting of the same object. Workspaces that bind to
this API are then actually bound to that export with the specified identity. The
security implication here is that if this secret is leaked, an attacker could
potentially export the same kind with the same identity and intercept
information. Utilizing the Identity Hash:

To secure this system, KCP uses an SHA-256 hash of the identity as a critical
part of a binding. When a workspace wants to bind to an API export, it points
the binding to the export by path (or other methods in the future) and uses the
identity hash to ensure binding to the correct export. Within the etcd storage
system, this identity hash is used to ensure that the API export owner can only
access objects that belong to the same export, identified by this hash. Security
and Data Segregation in Storage:

The identity hash becomes part of the key in the storage system. While client
requests to a workspace use the resource name asynchronously, the binding
carries the identity hash, and KCP ensures that data objects are stored in keys
incorporating this hash. For wildcard requests, this system allows the export
owner to see all objects across all logical clusters on a shard that pertain to
their export, using their identity hash. The wildcard request includes the
identity hash as part of the request resource name, allowing segregation of
wildcard requests for different exports of the same resource and group name.
This design presents a robust and secure system for managing API exports in KCP,
ensuring that only authorized entities can access the relevant data and
preventing unauthorized access or data leaks. This approach to API export
management in KCP is both intricate and vital for the system's overall security
and functionality.

## Wildcard Requests

A note about wildcard requests. Wildcard requests are the low-level tool or
enabler to make multi-workspace multi-logic cluster access possible. In
practice, the use of the wildcard request type is a privileged operation which
is only possible to do if you have the system master's group. An API export
owner will not have that group. To enable the API export owner to access the
objects of their export in all logic clusters on the shard. There is a proxy in
front of the wildcard requests. We call that proxy a virtual API server, a
virtual workspace API server. The API export object has a URL field in the
status where the export owner can access the objects on a shard across logic
clusters. This API service looks like the wildcard request we talked about
earlier, but it is protected. For example, on that endpoint, the API export
owner does not have to pass the identity hash. This is automatically added
behind the scenes when proxying the request from the API export owner to the
actual KCP instance via the wildcard request we talked about earlier. Virtual
workspace API servers are a crucial tool. Here, we see them the first time for
the first use of them in the system, but there are many more. We have built that
in KCP, but in theory, virtual workspace API servers can be built by third
parties and add further functionality which goes beyond a simple API service of
one block.

## Sharding

Now it is time to extend the mental model of KCP, which we described until now,
to extend it to multiple shards. Imagine you have multiple instances of KCP
running. Let's say we have two, A and B. Let's call the first one, let's call it
the root shard. So the A shard is the root shard. The root shard hosts the root
workspace. By the way, small note, the root workspace is the only workspace
which has a logical cluster, the identifier of its logical cluster, which
matches the workspace name. So the logical cluster UID of the root workspace is
root. And the root shard is the one hosting the root logical cluster. The root
logical cluster is a singleton in the KCP platform. On that root shard, there
can be many more workspaces, many more logical clusters. The workspaces, or
merely the logical clusters behind the workspace path, they are hosted on the
shards of the KCP system. If they are multiple, for every logical cluster, when
you want to access it, you have to know on which shard that logical cluster is
stored. Every logical cluster object, or let's go into some detail here, a
logical cluster, as we described, is identified by a logical cluster UID. That
UID is used in the resource path in the storage. To make a logical cluster
existent, there is a logical cluster object which lives in that logical cluster.
It lives in its own. It's a bit like the dot object, the dot file in a Linux
file system. It tells the system, when watching all logical cluster objects
across the shard, so across one shard, if you list all logical cluster objects,
you will know all the logical clusters on that shard. Side note, in theory, a
system master user can create objects like config maps in a logical cluster
without a logical cluster object. A user which is not privileged, including
admin users, cannot do that. There is a mission in place which makes sure that
objects like config maps can only be stored in logical clusters which exist.
Existence again is realized by creating the logical cluster object. The logical
cluster object is always called cluster. There's just one name, one singleton
per workspace, per logical cluster.

When sending a request to a multi-shard KCP, that request must be routed to the
right shard. As we have seen, the existence of the Logical Cluster object called
Cluster tells the system that the given Logical Cluster identified by the UID
lives on that shard. In other words, a front proxy, as we call it in KCP, a
component sitting in front of the KCP system, if it watches all Logical Cluster
objects on all shards, it knows how to route requests. Combining that with the
resolution of WorkspacePath, as we have seen before, this front proxy watches
Workspace objects and Logical Cluster objects. When resolving a request to a
WorkspacePath, it will first go Workspace object by Workspace object to resolve
the leave of the WorkspacePath, to map it to the UID of the underlying Logical
Cluster. In the last step, it will use the informer of the Logical Cluster
objects, and it will store a mapping from a Logical Cluster UID to the respective
shard, each with a stored one. So the front proxy will resolve, in the last
step, the Logical Cluster UID to the shard and send the request there. The shard
itself will check the existence of the Logical Cluster object called Cluster to
make sure the request it receives from the front proxy is valid. And it will
process the request and will eventually get the object the user requested, send
it back to the front proxy and the front proxy sends it back to the user.

I want to talk about controllers which run cross-workspace, which implement
semantics, which require multiple workspaces. As an example, take the API
binding controller. The controller sees an API binding pointing to an API export
on a different shard.

To implement this API binding logic, the controller has to access the API
export, and the API export can live on different shards, as we said. In that
case, it has to access a different shard to implement the functionality of the
API. This is not a desired behavior, because if the target shard is unavailable
for some time for reasons, all the other shards won't be able to bind APIs
anymore. To solve that, KCP introduced a concept called a cache server. A cache
server stores objects which are needed to implement cross-shard functionality of
APIs. For example, for the API binding process, the API exports are needed. What
happens is, there is a second controller next to the API binding controller, and
we call that an API export replication controller, which synchronizes all API
exports from a local shard to a shared cache server. The whole system will have
a number one or a number of cache servers, and each shard will replicate data to
the cache server or to the cache servers, if they are multiple. In this example,
all API exports not only live on the local shard where the workspace is living
on, but they are replicated, so the API binding controller, in our example, will
have an informer both against the local objects and against the cached objects.
When it wants to bind, it will look first for the local API exports, because of
course this export can also live potentially on the local shard, but it will
also watch on the cache server, and it will merge those two informers into one.
There might be more objects necessary. A typical example here is, again, the API
export, the binding process. For binding, you have to authorize. To authorize,
you need the ABAC objects, which are living next to the API export. The API
export application controller will check all ABAC objects, verify whether they
are necessary to authorize a binding, and in that case, it will label the ABAC
objects with a certain label for application, and application happens. This is a
general pattern, which we will use for multi or cross workspace semantics of
APIs.

The concept of a cache server introduces constraints. The cache server in KCP is
a regular Kube API server with workspace support. So it is bound to the scaling
targets of Kube itself, the Kube API server itself. So imagine you have a giant
multi-talent KCP installation. The cache server has to hold all the exports.
This means that the cache server has to be able to store all exports in the
system in roughly eight gigabytes of storage memory, in the case of etcd,
including the airbag objects as well. While for API exports, this number doesn't
seem to be a problem. You can imagine other kind of APIs, whereas cardinality
might be much bigger, and APIs which also should be cross-workspace. In that
case, you have to think about more scalable cache server topologies. In such a
setup, very likely you will have many tenants. A tenant is probably a good
partitioning, but it offers a way to partition the caching infrastructure. So
everything which is within one tenant could be contained by default at least,
which means you need only one cache server per tenant. Only those things like
APIs which are not contained, which are explicitly shared among users, would
have to be replicated into the whole hierarchy of cache servers. I mentioned the
word hierarchy here because this might be a caching hierarchy which we want to
use. Think of this as an API app store-like thing, where companies' tenants can
offer services to other tenants. In such a platform, they would opt in into
sharing their API export to the world. In such a setup, the cache server
scalability would only be a limit for the APIs which are shared in that app
store-like way. This doesn't seem to be a limit which limits the applicability
of KCP, because there will never be so many exports that eight gigabyte is not
enough.

## Multi-Shard Architecture and Controllers In a multi-shard setup

You need multiple instances of a controller or alternatively make a controller
aware of multiple instances of KCP. A controller would in the second case have
multiple wildcard watches as described before not against the shards themselves
but against the virtual workspace API server for API exports. And they would
watch multiple of them at the same time and give behavior semantics to the API
objects on all of those shards. And you can imagine that you want some kind of
partitioning of course if the number of shards grows. But it's pretty clear that
some kind of awareness of KCP is necessary to run multi-workspace,
multi-cluster controllers. In particular for the API exports, we talked about
having the URL of the virtual workspace API server in the status. In reality,
this list of URLs or this is a list of URLs. It's not just one. There are
multiple URLs to multiple virtual workspace API servers, one per shard. At least
one per shard which has at least one workspace which binds against that export.
That way a controller which is KCP-enabled would have to watch the API export
status and spawn another instance either of an informer or even the whole
controller per URL which pops up in the status of the export.

## Multi-shard setups, and the workspace hierarchy

When creating a workspace object within another parent workspace, a
WorkspaceScheduler will choose a shard and create a Logical Cluster object named
Cluster on that shard under a random Logical Cluster identity hash. The choice
of the shard to be used for scheduling follows different logics. In a single
shard setup, this will always be the same shard. In a multi-shard setup, this
could be random. Or it could be that it will choose a shard in the same region
by default. Or there could be shards which belong to certain tenants. There
could also be ways where a user could specify a different region for a
workspace. The parent workspace, for example, would live in US-West-1 and the
sub-workspace would live in EU-West-1 because it will host information necessary
in Europe. So the result of all of those scheduling alternatives would be that
the hierarchy is basically scattered across shards. Multiple shards from
multiple regions potentially would be needed to access a certain deeply nested
workspace. The naive implementation today of the front proxy is just watching
all shards for workspace objects and for Logical Cluster objects. Clearly, this
is not necessary that we implement it that way. We could have a highly available
and distributed storage as an alternative to have an eventually consistent
picture of all workspaces and Logical Clusters to implement the right routing of
requests. In such a world, resolving a path would not necessarily couple shards
from different regions or different cloud providers or anything like that. But
it would be highly available even when the region goes down, which is used to
store an intermediate workspace in the workspace path. Only the last component
actually matters. If you can resolve the workspace path to the Logical Cluster
UID and the shard name, the request can be routed, although parts of the
workspace path may be unlocked.

## Workspace Access Authorization

To access a workspace, the KCP instance which receives the request, potentially
sent by the front proxy, will do an authentication of the user, whether this
user is able to access the workspace as a whole, and in particular, the object
which is accessed, whether it is allowed to access that using normal local
airbag semantics. The second part is pure Kube. The first part is done through
authorization via airbag objects in the logical cluster, not outside, inside,
using a virtual fake resource, or more correctly, a certain verb against the
logic cluster object. The user is authorized to access that workspace, and
access means basically system-authenticated access, which might be just
discovery in the minimal case. But there is this first step of workspace as a
whole authorization, and it's done by checking that the user has verb access
permission against logical cluster object, which as we described before is
called cluster. To delegate access to that workspace, to a workspace, you will
create a cluster role for the logical cluster object with a verb access, and you
will bind the user or some groups the user has with a cluster role binding to
that cluster role. Again, very important, all this happens within the workspace
which is authorized. There is no airbag logic at the parent level. The reason is
having logic at the parent level would mean would have to cache the airbag
objects which are involved in the cache server. This is just heavy and can be
avoided by this architecture.

## Security and scheduling of workspaces

When scheduling a workspace, a shard is chosen to host the logic cluster. We
talked about variations of that logic before. The scheduler, which does that
work, runs on the shard, which hosts the parent workspace. For it to be able to
schedule, it means two things. First, what I just described, choosing the shard,
choosing a UID. And second, we have to create the logic cluster object on that
chosen shard. Remember, existence of a logic cluster means the creation of the
logic cluster object. For this to work, the shard which hosts the parent
workspace needs access to the target shard of the child. Access which allows to
create a logic cluster object, although the logic cluster it's created in is not
existing yet. So this process of scheduling today needs privileged access, a
privileged user which can create logic clusters, skipping the check whether the
logic cluster actually exists.

## Bootstrapping a KCP platform and, in particular, a KCP shard in the light of multiple shards

Bootstrapping a KCP system, and in particular, a KCP shard. Bootstrapping a
single shard KCP means to create the root workspace, create the API shards for
the main API groups, and that's basically it. A multi-shard setup requires to
bootstrap the root shard, which is basically equivalent to what I just
described, and then bootstrapping further shards in addition. When we talked
about API exports and API bindings, we talked about the identity of an export.
The identity, or more completely, the hash of the identity string, is an
important part of a resource, especially the resources which are defined in the
root shards, the root workspace API exports. And those are particularly risky,
so we really want this security feature of identities and identity hashes for
those, because they are central for KCP, for the security of KCP, of the whole
platform. By bootstrapping a KCP shard, it will need the identity hash of those
exports. And to do that, the KCP shard needs a bootstrapping root shard user,
which is able to read the identity hashes of the API exports. Which means, when
bootstrapping a new shard for the first time the shard has started, it will need
access to the root shard. After that, every start of the KCP instance of that
shard, the KCP process, that root user is actually not needed anymore if no new
exports are added or something like that. Every shard caches its identities, its
identity hashes, in a local contract map. So that contract maps allows to
restart every KCP shard, even when the root shard is down. What makes this
bootstrapping tricky is that to make, to start up the KCP shard and the core
controllers, for example, the API binding controller, it has to start certain
informers. And to start an informer, you have to know the identity hash of the
exports of the resources you want to watch. So in the bootstrapping phase,
before a shard is ready, it will need this root shard information, which is
cached in the contract map, or if the contract map is not there, or incomplete,
there's access to the root shard. When those informers are up, the core
controllers of the KCP shard are started and the KCP shard is ready to serve
requests.

## Definitions of Shards

Shards are defined by creating Shard objects in the root workspace. When a Shard
starts up, it will attempt to create its object there and tries to keep status
of that object up-to-date with the controller. In the future, this object might
also give load information, which might control scheduling of new workspaces.
Today, this is very simplistic and not really crucial for the architecture of
KCP.

## Logical cluster path annotation

A logical cluster has one unique longest path which points to it. In the
simplest case where a logical cluster has no parent, that path is equal to the
logical cluster ID. But if there is a parent, the parent longest and unique path
plus colon plus the workspace name of the child is the longest path for that
child. Some controllers need that information. To make that available, there is
a kcp.io path annotation on the logical cluster object. If you have that object,
you can know the path as well, not only the logical cluster itself, the logical
cluster UID. There are APIs which allow bypass references, like API binding, for
example. To make that possible, the API export, which is cached in the cache
server, will also hold that path annotation. But this is a custom feature of the
API export object, the API export API. There must be a controller which adds
that annotation to the API export, which then ends up on the cache server to
resolve an API binding. The API binding will maintain an index of the informer
of API exports by that path annotation, so it can quickly find, bypass the
corresponding export if it exists. There might be other APIs which use the same
trick to allow bypass references across workspaces.

## Multiple Routes in the Workspace Hierarchy

We described before that the only workspace which has a logical cluster name
that resembles the longest workspace path as the root workspace. We will keep
this invariant, but we will extend the hierarchy by having more than one root,
like a tree, not a tree, a root of trees. The main tree is starting at root, as
we described before. Imagine you have a logical cluster which carries a path
annotation which is referencing logically a parent which does not exist. We
allow that. As an example, we take a user hierarchy. Imagine there are user
workspaces. Every user gets a workspace. What we want to have is user colon
username as a workspace. But we will not have a workspace or a logical cluster
called user or users, plural. We can do that by creating logical clusters with a
path annotation, users colon username, and no workspace which points to them. No
workspace in the parent, because the parent doesn't exist. The front proxy will
take the path annotation, and it will resolve the path from users colon username
just by following the annotation. The annotation is enough to create a new root
in the system. That way, we have rootless user home workspaces. In a previous
iteration of KCP, we had a hierarchy of user home workspaces in the main
hierarchy, which means we had to apply a multilevel hierarchy with a first
letter or some hash of usernames and then multiple layers to guarantee that
millions of users can store the workspace objects in the same parent. As you
know in Kubernetes, there's a logical or technical limit of the number of
objects of cluster wide cluster scope objects, which is probably in the 10,000
or something like that. All this complexity of a multilayer hierarchy for home
workspaces goes away by having rootless workspaces. They can live anywhere in
the KCP system on every shard, and the front proxy implements them just by the
path annotation. To implement them, you need a privileged user which can create
logical cluster objects in non-existing logical clusters, so very similar
to the scheduler we talked about before. The user home workspace here is just an
example. You could have rootless logical clusters for tenants, `tenant:colon:tenant-name`, 
for example, or anything else you want to have as a start of a new hierarchy.

# System Masters versus Cluster Admin Users

In a single-shard setup, when you launch KCP via KCP Start, for convenience, an
admin user is created. An admin user has star access, wildcard access, to all
resources and all verbs. In Kubernetes, there is a System Masters group in
addition. System Masters is more than Cluster Admin. System Masters means that
authorization and admission is skipped. Skipped means safety checks are not
executed for System Master requests. As an example, the scheduler needs a System
Masters user in order to create notary cluster objects in non-existing notary
clusters. It is important to understand that every user in front of the front
proxy, so a real user including cluster admins, should not be System Masters
because System Masters can destroy the workspace hierarchy, bring it into an
inconsistent state. Hence, System Masters users should only be used for very
specific high-risk operations on a shard. The KCP process will create a shard
admin for that purpose. It's a shard local System Masters user. The admin user
which is created is not like that. The KCP Start command in a single shard setup
will create a token-based admin user. That token is only valid during runtime.
It's not completely correct. That token is also stored locally, but the idea is
that that user is just for convenience and a single shard setup. If you want to
have a multi-shard setup, you have to create an admin user outside the
bootstrapping process. There's a flag for the KCP Start command to skip the
admin user creation. You can use, for example, a client certificate which adds
cluster admin permissions to a user and makes that client certificate accepted
by all shards by passing the right client cert flags to the process. To
summarize, the bootstrapping of KCP in a multi-shard setup is a multistep
process. The KCP Start command alone is without any special parameters. It's
really meant for a single shard and for that reason, pretty simplistic setup.
This is intentional. The admin user must be created out of scope in a step on
its own.

## Mobility of Workspaces

There are reasons why workspaces or more correctly their logical clusters should
be moved to another shard, for example to escape from some noisy neighbour, or
because a shard is supposed to be replaced and drained. This is future work. But
let's collect some thoughts:

1. Moving a logical cluster means to copy the key-values from storage to another
   storage. This is obviously not an instant process but might take minutes, at
   least with etcd (maybe a kine based storage could be faster).
2. While moving, controllers need some transactional migration to the new shard.
3. Moving could be with downtime, or it can be continuous. There are ideas to use
   some kind of progressive move-stone (like tomb-stones) logic, but this hasn't
   been implemented.
4. Controllers would see deletion as deletions and do what they have to do.
   Even if we make the logical cluster immutable during deletion, there might be
   cross-workspace controllers leading to surprising effects of these deletion
   events that actually are no deletions. It might be that extending the
   wildcard watch protocol with MOVE events would help here. After all this
   protocol is under kcp control as wildcard request semantics is not part of
   Kubernetes conformance anyway.