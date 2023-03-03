---
linkTitle: "kcp-aware controllers"
weight: 1
description: >
  How to write a kcp-aware controller.
---

# Writing kcp-aware controllers

## Keys for objects in listers/indexers

When you need to get an object from a kcp-aware lister or an indexer, you can't just pass the object's name to the
`Get()` function, like you do with a typical controller targeting Kubernetes. Projects using kcp's copy of client-go
are using a modified key function.

Here are what keys look like for an object `foo` for both cluster-scoped and namespace-scoped varieties:

|Organization|Workspace|Logical Cluster|Namespace|Key|
|-|-|-|-|-|
|-|-|root|-|root|foo|
|-|-|root|default|default/root|foo|
|root|my-org|root:my-org|-|root:my-org|foo|
|root|my-org|root:my-org|default|default/root:my-org|foo|
|my-org|my-workspace|my-org:my-workspace|-|my-org:my-workspace|foo|
|my-org|my-workspace|my-org:my-workspace|default|default/my-org:my-workspace|foo|

## Encoding/decoding keys

Use the `github.com/kcp-dev/apimachinery/pkg/cache` package to encode and decode keys.
