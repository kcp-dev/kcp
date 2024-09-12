---
description: >
    What APIs come standard, how to share APIs with others, how to consume shared APIs.
---

# APIs in kcp

## Overview

kcp supports several built-in Kubernetes APIs, provides extensibility using CustomResourceDefinitions, and adds a new
way to export custom APIs for sharing with other workspaces.

## CustomResourceDefinitions

kcp, like Kubernetes, allows developers to add new APIs using
[CustomResourceDefinitions](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
(CRDs). Unlike Kubernetes, kcp allows multiple copies of a CRD (e.g. `widgets.example.com`) to be installed in multiple
workspaces at the same time. Each copy is entirely independent and isolated from all other copies. This means the API
versions and schemas can be entirely different. kcp makes this possible because each workspace is its own isolated
"cluster."

There are currently some limitations to be aware of with CRDs in kcp:

- Conversion webhooks are not supported
- `service`-based validating/mutating webhooks are not supported; you must use `url`-based  `clientConfigs` instead.

CRDs are a fantastic way to add new APIs to a workspace, but if you want to share a CRD with other workspaces, you have
to install it in each workspace separately. You also need a controller that can reconcile CRs in all the workspaces
where your CRD is installed, which typically means 1 distinct controller per workspace. CRDs are not "cheap" in the API
server (each one consumes memory), and kcp offers an improved workflow that significantly reduces overhead.

## Pages

{% include "partials/section-overview.html" %}
