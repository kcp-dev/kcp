# Contributing to kcp

kcp is [Apache 2.0 licensed](https://github.com/kcp-dev/kcp/tree/main/LICENSE) and we accept contributions via
GitHub pull requests.

Please read the following guide if you're interested in contributing to kcp.

## Code of Conduct

Please be aware that this project is governed by the [CNCF Code of Conduct](https://github.com/kcp-dev/kcp/blob/main/code-of-conduct.md),
which boils down to "let's be excellent to each other". Code of Conduct violations can be reported to the
[CNCF Code of Conduct Committee](https://www.cncf.io/conduct/committee/), which can be reached via [conduct@cncf.io](mailto:conduct@cncf.io).

## Certificate of Origin

By contributing to this project you agree to the Developer Certificate of
Origin (DCO). This document was created by the Linux Kernel community and is a
simple statement that you, as a contributor, have the legal right to make the
contribution. See the [DCO](https://github.com/kcp-dev/kcp/tree/main/DCO) file for details.

The DCO requires a human to certify the contribution. AI agents cannot legally provide this certification.

For how to correctly set this on your commits, check out the [Getting Started](./getting-started.md#developer-certificate-of-origin-dco) guide.

## Use of AI Coding Assistants

Contributors are welcome to use AI tools to aid development. However, the human author remains fully responsible
for every contribution they submit. Before opening a pull request, filing an issue, or pushing a commit,
contributors must review and understand the content in its entirety — just as they would for any code they write themselves.

### Authorship and Responsibility

AI tools are not authors. The human submitting the contribution is the sole author and bears full responsibility for it,
including correctness, licensing compliance, and adherence to project conventions.

We will not accept contributions that attribute authorship or sign-off to an AI tool. This includes the following
or similar markers, which shift the responsibility to an AI:

- `Signed-off-by: <AI tool name>`
- `Co-authored-by: <AI tool name>`
- `Committed-by: <AI tool name>`
- `Generated with <AI tool name>`

### Attribution

There is no need to attribute your commit messages using `Assisted-by` markers.

## Community Roles

### Maintainers

The project maintainers are the central [gonvernance entity](https://github.com/kcp-dev/kcp/blob/main/GOVERNANCE.md) of
kcp. They review and approve PRs into all projects in the kcp-dev GitHub organization and decide on project direction
and other governance matters.

### Subproject Maintainers

The kcp-dev GitHub organization hosts several subprojects that utilize or extend kcp in some form. These subprojects
might take on additional subproject maintainers that participate in code review and project goal decisions to steer
their respective subprojects.

### Approvers

Consistent contributors with a specific area of expertise in the code base might be chosen to be approvers by the
(subproject) maintainers. Approvers are allowed to approve (and subsequently, merge) code for parts of a specific project.
Approvers are generally not allowed to have full approval rights for a (sub)project but only specific folders within it.

### Contributors

People that are consistently contributing to the project (through code, documentation or other means) are considered
project contributors. They are invited by maintainers to join the kcp-dev GitHub organization, which allows them
to submit PRs that do not need approval to run CI jobs in Prow.

Contributors are able to LGTM pull requests in the kcp-dev GitHub organization, but they cannot merge them.

## Project Management

### Priorities & Milestones

We prioritize issues and features both synchronously (during community meetings) and asynchronously (Slack/GitHub conversations).

We group issues together into milestones. Each milestone represents a planned kcp release and subsequently can be open for a period of 3-5 months.

You can see the [current list of milestones](https://github.com/kcp-dev/kcp/milestones?direction=asc&sort=due_date&state=open) in GitHub.

For a given issue or pull request, its milestone may be:

- **unset/unassigned**: we haven't looked at this yet, or if we have, we aren't sure if we want to do it and it needs more community discussion
- **assigned to a version milestone**
- **assigned to `TBD`** - we have looked at this, decided that it is important and we eventually would like to do it, but we aren't sure exactly when

If you are confident about the target milestone for your issue or PR, please set it. If you don’t have permissions, please ask & we’ll set it for you.

### Epics

We sometimes use the [epic label](https://github.com/kcp-dev/kcp/issues?q=is%3Aopen+is%3Aissue+label%3Aepic+) to track large features that typically involve multiple stories.
When creating a new epic, please use the [epic issue template](https://github.com/kcp-dev/kcp/issues/new?assignees=&labels=epic&template=epic.md&title=).

Please make sure that you fill in all the sections of the template (it's ok if some of this is done later, after creating the issue). If you need help with anything, please let us know.

#### Story Tasks

Story tasks in an epic should generally represent an independent chunk of work that can be implemented. These don't necessarily need to be copied to standalone GitHub issues; it's ok if we just track the story in the epic as a task. On a case by case basis, if a story seems large enough that it warrants its own issue, we can discuss creating one.

Please tag yourself using your GitHub handle next to a story task you plan to work on. If you don't have permission to do this, please let us know by either commenting on the issue, or reaching out in Slack, and we'll assist you.

When you open a PR for a story task, please edit the epic description and add a link to the PR next to your task.

When the PR has been merged, please make sure the task is checked off in the epic.

### Tracking Work

#### Issue Status and Project Board

We use GitHub Projects for project management, compare [our project board](https://github.com/orgs/kcp-dev/projects/1). Please add issues and PRs into the kcp project and update the status (new, in-progress, ...) for those you are actively working on.

#### Unplanned/Untracked Work

If you find yourself working on something that is unplanned and/or untracked (i.e., not an open GitHub issue or story task in an epic), that's 100% ok, but we'd like to track this type of work too! Please file a new issue for it, and when you have a PR ready, mark the PR as fixing the issue.

## Getting your PR Merged

The `kcp` project uses `OWNERS` files to denote the collaborators who can assist you in getting your PR merged.  There
are two roles: reviewer and approver.  Merging a PR requires sign off from both a reviewer and an approver.

In general, maintainers strive to pick up PRs for review when they can. If you feel like your PR has been missed,
do not hesitate to ping maintainers directly or ask on the project communication channels about your PR.

### The Monorepo Structure

The core `kcp-dev/kcp` repository is a monorepo containing the kcp core and some close to the core libraries.
See the [monorepo document](./monorepo.md) for more details.

When contributing to projects like `kcp-dev/apimachinery`, `kcp-dev/client-go`, and `kcp-dev/code-generator`,
you must create a PR in the `kcp-dev/kcp` repository. Direct PRs to these repositories will be closed.
