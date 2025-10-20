---
description: >
    Monorepo structure for kcp projects.
---

# Monorepo Structure

The [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository is a monorepo that contains the source code for:

- the kcp core
- libraries that are used in the kcp core or are close to the kcp core

At the moment, the following libraries are part of the monorepo:

- [kcp-dev/apimachinery](https://github.com/kcp-dev/apimachinery)
- [kcp-dev/client-go](https://github.com/kcp-dev/client-go)
- [kcp-dev/code-generator](https://github.com/kcp-dev/code-generator)

## Terminology

- The monorepo: the [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository
- Staging repository: a repository that is part of the monorepo
  (located within `kcp-dev/kcp/staging/src/github.com/kcp-dev`)
- Published repository: a standalone repository that's a mirror of the staging repository

Note: the term 'staging repository' does **not** imply a Git repository (with `.git` directory), but an
authoritative place that stores code. Every staging repository is a standalone Go module.

## Similarities with Kubernetes

This pattern is the same as the Kubernetes staging repositories pattern, with an exception of how tags are handled
within published repositories (this is explained later in this document).

## High Level Overview

The source code for staging repositories is located on the special path in the monorepo which is
[`./staging/src/github.com/kcp-dev`](https://github.com/kcp-dev/kcp/tree/main/staging/src/github.com/kcp-dev).
Every subdirectory on that path corresponds to a single staging repository.
The code in the staging/ directory is authoritative, i.e. the only copy of the code. You can directly modify such code.

In order to preserve standalone Go modules for each staging repository, the source code from staging repositories
is mirrored to standalone (aka published) repositories. The mirroring is an automatic process happening every
several hours.

Aside from the source code, the mirroring process includes branches and tags. This means that branches from
the monorepo for which mirroring is enabled will be created in each published repository. Tags on those branches
will be automatically mirrored to the published repositories. At the moment, mirroring is enabled for
the `main` branch and release branches starting from `release-0.29`.

## Handling Tags

Tags published to the published repositories inherit the major version of their original Go module.
In other words, the tag's major version will **not** correspond to the kcp major version.

For example, if we push v0.29.0 in the kcp repository, the tag in the published repository that has Go module major
version v2 will be v2.29.0.

Concretely:

- kcp-dev/client-go has the Go module major version at v0, so tags will be v0.x.y
- kcp-dev/apimachinery has the Go module major version at v2, so tags will be v2.x.y
- kcp-dev/code-generator has the Go module major version at v3, so tags will be v3.x.y

Note: the reason for the special handling of tags is to ensure the backwards compatibility, as these libraries
already had tags/releases that did not correspond to the kcp versioning.

## Implementation

The mirroring process is implemented via the publishing-bot tool. This tool has been originally created by
the Kubernetes project. We're maintaining a fork of this tool that has been adjusted for our purposes at
[kcp-dev/publishing-bot](https://github.com/kcp-dev/publishing-bot). Overall, the changes made to the original tool
include:

- Add support for running publishing-bot on ARM64
- Add image building process compatible with kcp
- Add support for customizing tags mirroring
- Add kcp-related configuration

Some of these changes will be contributed to the upstream project in the future.

The publishing-bot is running as a Pod in the Prow control plane cluster used by kcp. The status of the bot is
automatically posted to the [`kcp-dev/kcp#3619`](https://github.com/kcp-dev/kcp/issues/3619) issue.
The issue being closed indicates that the bot is healthy and has at least one successful run, while the
issue being open indicates that the bot is not healthy and at least the last run has failed.

The kcp configuration for publishing-bot can be found at https://github.com/kcp-dev/publishing-bot/blob/master/configs/kcp-dev

The mirroring rules are located in the kcp-dev/kcp repoitory and can be found at https://github.com/kcp-dev/kcp/blob/main/staging/publishing/rules.yaml

At the moment, the bot is configured to run every 4 hours.

## Creating a New Staging Repository

Adding a completely new staging repository to the monorepo must follow this process:

- Send an email to the [the developer mailing list](https://groups.google.com/g/kcp-dev) requesting a new staging
  repository
- After the request is approved by a simple majority of all Maintainers, create a tracking issue in the
  [kcp-dev/kcp](https://github.com/kcp-dev/kcp) repository
- Create a staging repository in `staging/src/github.com/kcp-dev` with all required files for a repository
  (e.g. LICENSE, OWNERS...)
- Create a new Git repository on GitHub to serve as a published repository with an empty initial commit
- Update the publishing-bot rules to mirror from the new staging repository to the new published repository

Adding an existing standalone repository to the monorepo is generally discouraged and will be considered on
a case-by-case basis by Maintainers. This case requires following different steps that will be documented
in the future.
