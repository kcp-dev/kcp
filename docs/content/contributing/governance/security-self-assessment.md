---
title: Security Self-Assessment
---

# Security Self-Assessment kcp

This document is intended to aid the project's maintainers, contributors, and users understand the project's security status and help identify points of improvement.

## Table of Contents

* [Metadata](#metadata)
  * [Security links](#security-links)
* [Overview](#overview)
  * [Actors](#actors)
  * [Actions](#actions)
  * [Background](#background)
  * [Goals](#goals)
  * [Non-goals](#non-goals)
* [Self-assessment use](#self-assessment-use)
* [Security functions and features](#security-functions-and-features)
* [Project compliance](#project-compliance)
* [Secure development practices](#secure-development-practices)
* [Security issue resolution](#security-issue-resolution)
* [Appendix](#appendix)

## Metadata

| | |
| ---------------- | -------- |
| Assessment Stage | Complete |
| Software |  [kcp](https://kcp.io)  |
| Security Provider | No |
| Languages | Go |
| SBOM | kcp does not currently generate SBOMs on release |
| Security links | See below |

### Security links

| Doc                       | url                                             |
| ------------------------- | ----------------------------------------------- |
| Reporting security issues | [here](https://github.com/kcp-dev/kcp/security) |
| Security process          | [here](https://github.com/kcp-dev/kcp/blob/main/SECURITY.md) |

## Overview

kcp is an open-source, Kubernetes-like control plane designed for multi-cluster and multi-tenant environments. It provides a control plane that is not tied to a specific set of hardware, allowing users and services to manage APIs across a group of "logical clusters". kcp is specifically NOT a control plane for managing container workloads but higher level abstractions, which in turn might be run on separate Kubernetes clusters.

### Background

The kcp project began as an experiment to add the concept of "logical clusters" to the Kubernetes API server, effectively decoupling the powerful control plane from its traditional role in container orchestration. The project is built upon upstream Kubernetes code but is not a fork, with a strong commitment to maintaining 100% compatibility with the Kubernetes API machinery and ecosystem tooling. This allows kcp to provide a generic, horizontally scalable control plane for use cases beyond containers.

### Actors

The kcp architecture is composed of several key primitives that work together to provide a scalable, multi-tenant control plane.

#### kcp Server Components

* **Workspaces**: The primary user-facing unit of tenancy and isolation. From a user's perspective, a Workspace is a fully-isolated, Kubernetes-like cluster with its own unique API endpoint, CustomResourceDefinitions (CRDs), and RBAC policies.

* **Logical Clusters**: The underlying implementation construct for a Workspace. A logical cluster is a logical partition within the kcp data store (etcd), ensuring that objects from different workspaces are stored in disjoint key prefixes, which is the primary mechanism for enforcing isolation. The goal is to make creating a logical cluster as cheap and fast as creating a Kubernetes namespace.

* **Virtual Workspaces**: Endpoints that provide a Kubernetes-like API interface, but are not backed by a logical cluster for storage. They provide a computed "view" of certain resources across logical clusters. The exact semantics depend on the virtual workspace implementation, different virtual workspace endpoints provide different views according to their role. Access to virtual workspace endpoint is guarded by RBAC.

* **Shards**: A running instance of the kcp server process. Each shard hosts a set of logical clusters, and a full kcp installation can be composed of many shards to achieve horizontal scalability.

#### kcp kubectl Plugin (CLI)

kcp provides a set of plugins for `kubectl`, the Kubernetes command line client. These plugins interact with kcp through its Kubernetes-like APIs and offer command line tooling for interacting with the unique features of kcp (see above for some of them). All of the interactions are executed via Kubernetes client libraries and use the client credentials passed to `kubectl` (e.g. via the `KUBECONFIG` environment variable).

##### kcp Components

* **kcp**: The `kcp` binary provides the means to run a kcp shard (see above). It can either be launched completely standalone and then embeds a "mini" front-proxy, virtual-workspaces and a cache-server to run a fully functional kcp instance, or be run just to serve API endpoints and run controller loops.

* **cache-server**: The cache server provides a shared Kubernetes-like API layer that all shards connect to and cache objects relevant to other shards (e.g. `APIExports`) so that shards do not need to directly interact with each other.

* **kcp-front-proxy**: An intelligent API gateway that sits in front of all shards. It is aware of all logical clusters and the shards they reside on, and it routes incoming user requests to the correct shard.

#### API Management

kcp provides resources dedicated to managing available APIs in a Workspace.

* **APIExport**: Allows a service provider in one workspace to publish an API for consumption by other workspaces.
 
* **APIBinding**: Allows a service consumer in one workspace to bind to an APIExport from another workspace, making the published API available in the local workspace.

 The ability to bind APIs across workspaces (a security boundary) is guarded by RBAC checks.

### Actions

* **Workspace Management**: Users can create, list, and navigate between workspaces, each providing an isolated API endpoint.

* **API Sharing**: Service providers can securely offer APIs across workspace boundaries using a declarative APIExport and APIBinding model.

* **API Request Routing**: The front-proxy routes all standard client traffic to the appropriate shard based on the workspace path in the request URL.

* **Administrative Wildcard Requests**: A privileged "wildcard" endpoint allows global administrators to list resources across all logical clusters on a specific shard, bypassing standard workspace isolation for operational purposes.

### Goals

* Provide a scalable, multi-tenant control plane that can serve hundreds or thousands of isolated tenants.
* Enable service providers to offer Kubernetes-like APIs that can be consumed by consumers across multiple isolated tenants.
* Maintain a high degree of compatibility with the standard Kubernetes API and toolchain (e.g., kubectl, client-go, multicluster-runtime).


#### Security Goals

* **Strict Workspace Isolation:** Ensure that tenants in one Workspace cannot see, access, or affect resources in another Workspace unless explicitly authorized. This is the core security boundary of the system.
* **Scoped Permissions:** The permissions granted to a Syncer on both the kcp server and the physical cluster must be limited to the minimum required for its function (Principle of Least Privilege). - TODO remove Syncer
* **Standard Kubernetes Auth:** Leverage and extend Kubernetes authentication and authorization (RBAC) mechanisms for access control to the kcp server and within Workspaces.

### Non-goals

* kcp does not manage the data plane or physical infrastructure of downstream clusters (e.g., nodes, CNI, storage). It is purely a control plane.
* kcp is not a cluster provisioning tool; it doesn't define what kind of APIs are offered (which could include a cluster provisioning API, but that is not part of the core kcp efforts).
* kcp's logical clusters cannot be used as dedicated control planes for Kubernetes clusters.

## Self-assessment Use

This self-assessment is not intended to provide a security audit of kcp or function as an independent assessment or attestation of kcp's security health.

This document provides kcp users with an initial understanding of kcp's security, where to find existing security documentation, kcp's plans for security, and a general overview of kcp's security practices, both for the development of kcp and its operational security.

## Security Functions and Features

In general, kcp is a project; therefore, we do not list all the features here, only the "non-functional" security features.

### Critical

#### Workspace Isolation

The central security feature of kcp is the logical isolation provided by Workspaces. All API requests are scoped to a Workspace, and the system's authentication and authorization layers ensures that a user's credentials are only valid for the Workspaces they have been granted access to. This prevents cross-tenant data leakage and interference.

#### Authentication and Authorization

kcp uses the standard Kubernetes API server authentication and authorization mechanisms. It extends Kubernetes RBAC to operate within the context of Workspaces, allowing for fine-grained control over who can do what within each isolated environment.

#### Virtual Workspace Isolation

Given the broad permissions that virtual workspace components need to create various computed views across all available resources on a kcp shard, they often have to run with administrative permissions. It is therefore critical that virtual workspace implementations properly isolate requests from each other and use proper impersonation and/or enhanced authN/authZ checks for validating requests.

### Security Relevant

#### API Scoping (APIExport/APIBinding)

kcp allows administrators to control which APIs are available within a Workspace. An APIExport object makes an API available for consumption, and an APIBinding object makes it accessible within a specific Workspace. This mechanism can be used to limit the attack surface available to tenants.

#### APIExport Identity

To prevent data leakage when multiple tenants consume APIs with the same name (e.g., widgets.example.com) from different providers, each APIExport is associated with a unique cryptographic identity. A hash of this identity is used as part of the etcd storage path for all resources created via that export, ensuring complete data segregation.

#### Permission Claims

The APIExport/APIBinding model includes an explicit consent mechanism called PermissionClaims. An API provider must declare any access it needs to resources (like ConfigMaps or Secrets) in a consumer's workspace. The consumer must then explicitly review and accept these claims in their APIBinding for the access to be granted.

#### Maximal Permission Policy

An APIExport can define a maximalPermissionPolicy using standard RBAC rules. This policy acts as an upper bound on the permissions that any user from a consuming workspace can have on the exported API's resources, allowing the provider to enforce security constraints.

## Project Compliance

The project has no written record of complying with a well known security standard like FIPS.

## Secure Development Practices

### Development Pipeline

kcp's development pipeline uses [prow](https://docs.prow.k8s.io/docs/overview/) and thus ensures the software is tested for being robust, reliable, and secure. It involves several stages of reviews by project maintainers and automated testing flows.

#### Contributor Requirements

Contributors to kcp are required to [sign their commits](https://docs.kcp.io/kcp/latest/contributing/getting-started/#developer-certificate-of-origin-dco), adhering to the Developer Certificate of Origin (DCO). Contributors use the Signed-off-by line in commit messages to signal their adherence to these requirements.

Contributors can start by forking the repository on GitHub, reading the installation document for build and test instructions, and playing with the project.

#### Container Images

The container images used in kcp are built by [automatic pipelines](https://github.com/kcp-dev/infra/blob/main/prow/jobs/kcp-dev/kcp/kcp-postsubmits.yaml) and not individual contributors.

#### Reviewers

A project maintainer or code reviewer (called approver) reviews a commit before it is merged. This practice helps catch potential security issues early in the development process.

Maintainers or reviewers cannot merge their own code without a second review.

#### Automated Checks

kcp includes automated checks as part of its continuous integration (CI) process. The project has checks for dangerous workflow patterns and scans for known vulnerabilities in its dependencies.

#### Integration Tests

kcp's upstream continuous integration (CI) tests will automatically run integration tests against proposed changes. Users are not required to run these tests locally, but they may.

### Communication Channels

  kcp Communication Channels

#### Internal Communication

* Slack: The kcp-dev Slack channel in the [Kubernetes slack](https://communityinviter.com/apps/kubernetes/community)
 or [issues](https://github.com/kcp-dev/kcp/issues)
* Security topics: [kcp-dev-private](https://groups.google.com/g/kcp-dev-private) Google Group

#### Inbound Communication

Users or prospective users communicate with the kcp team through Slack or GitHub issues and pull requests. GitHub is a platform that hosts the kcp project's codebase and provides features for tracking changes, managing versions, and collaborating on code. Users can submit pull requests to report issues, propose changes, or contribute to the project.

* GitHub: [issues](https://github.com/kcp-dev/kcp/issues)
* Slack: [#kcp-users](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK)

#### Outbound Communication

* Mailing list: [kcp-users](https://groups.google.com/g/kcp-users)
* Website and blog: [kcp.io](https://kcp.io/)

##### Community Meetings

kcp [community meetings](https://docs.google.com/document/d/1PrEhbmq1WfxFv1fTikDBZzXEIJkUWVHdqDFxaY1Ply4/) are held on Google Meet every other week on Thursday at 17:00 CET. Meeting events can also be found on [community.cncf.io](https://community.cncf.io/kcp/).

Community members are encouraged to join and add to the discussion via the community meeting notes document (linked above).

## Security Issue Resolution

For a complete list of closed security issues, please refer to [this link](https://github.com/kcp-dev/kcp/security/advisories).

### Responsible Disclosures Process

In case of suspected security issues, incidents, or vulnerabilities, both external and internal to the project, kcp has a responsible disclosure process in place. The process is designed to handle security vulnerabilities quickly and sometimes privately. The primary goal of this process is to reduce the time users are vulnerable to publicly known exploits.

#### Vulnerability Response Process

Maintainers provide a [Security Response Team](https://github.com/kcp-dev/kcp/blob/main/MAINTAINERS.md#security-response-team) that organizes the entire response, including internal communication and external disclosure.

#### Reporting Security Vulnerabilities

Security vulnerability reports are handled via GitHub's security issue reporting feature, available [here](https://github.com/kcp-dev/kcp/security). The Security Response Team triage and respond to security issues reported privately through the tool.

Please see the complete [security release process](https://github.com/kcp-dev/kcp/blob/main/SECURITY.md) for further details.

#### Private Disclosure Processes

If a security vulnerability or any security-related issues are found, they should not be filed as a public or a GitHub issue. Instead, the report should be reported via the security issue reporting feature.

#### Public Disclosure Processes

If a publicly disclosed security vulnerability is known, it should be reported immediately via the security issue reporting feature to inform the Security Response Team. This will initiate the patch, release, and communication process.

### Patch, Release, and Public Communication

For each vulnerability, a member of the Security Response Team will send disclosure messages to the rest of the community. The leading team member is chosen based on availability at the time of a security report.

### Patching/Update Availability

Once the vulnerability has been confirmed and the relevant parties have been notified, the next step is to make a patch or update available. This involves releasing a new version of the software that addresses the vulnerability. The patch or update is then made available to all users, who can update their systems to the latest version to protect against the vulnerability. Communication is sent out via email, Slack and the GitHub security issue disclosure feature.

## Incident Response

There is a template for incident response for reference [here](https://github.com/cncf/tag-security/blob/main/community/resources/project-resources/templates/incident-response.md)

## Appendix

### Known Issues over Time

* [GHSA-c7xh-gjv4-4jgv](https://github.com/kcp-dev/kcp/security/advisories/GHSA-c7xh-gjv4-4jgv):  Impersonation allows access to global administrative groups 
* [GHSA-w2rr-38wv-8rrp](https://github.com/kcp-dev/kcp/security/advisories/GHSA-w2rr-38wv-8rrp):  Unauthorized creation and deletion of objects in arbitrary workspaces through APIExport Virtual Workspace

### OpenSSF Best Practices

The kcp project is continuously improving its practices based on the OpenSSF recommendations, see [Scorecard Results](https://securityscorecards.dev/viewer/?uri=github.com/kcp-dev/kcp) and [Best Practices Badge](https://www.bestpractices.dev/en/projects/8119).
