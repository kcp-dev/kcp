# 0001: Project process refinement for 2023

## Summary

As our project grows we need to scale our collaboration processes to meet the needs of a growing community. The 
approchability of a project is important to foster continued growth and ensure the mission and, perhaps just 
as important, non-goals of the project are clearly understood. This enhancement proposes some next steps to take 
in the refinement of kcp's existing processes.

## Motivation

Over the past year there has been a drive to show the "realness" of the kcp project. That investment has been 
made by contributing enough code to each of the core features of kcp that an early adopter could, with enough drive,
integrate with the APIs to understand the value proposition. Optimizing for that goal has allowed quick progress but
at the expense of things like documentation.

Within kcp there are also a few logical projects that can be separated in order to provide more clarity on the 
direction of each project and facilitate growing contributors in those areas.

In order to keep growing it is essential that the kcp project provides an easily approachable method for consumers to
quickly understand the goals of the project, see the current state of features, and run the latest build. It is also
important that potential contributors be able to quickly setup their development environment, understand the 
engagement model for development, and be able to discover the right forums for their area of interest.

This document proposes that now is the right time to take these steps in order to facilitate objectives in 2023.

### Goals

1. Agree on the need for an official enhancement process
2. Agree on the need to split the workloads and control plane areas of interest
3. Agree on the need to create a formal definition of done that includes documentation and testing requirements
4. Agree on the need to identify graduation criteria of existing APIs
5. Assign ownership of the above for definition and implementation

### Non-Goals

1. Actually define the specifics of each of the goals - this should be up to the community

## Proposal

### Implement an enhancement proposal process

An enhancement process has many benefits for a project, including:
* creating an approchable method to driving change in a project
* offering a forum for asynchronous feedback
* providing a discoverable history of project change and the motivations behind the changes
* setting expectations on the bar for contribution in areas like quality and graduation criteria

This proposal suggests that kcp adopt an enhancement process that follows practices familiar to many in the 
community via the [KEP process](https://github.com/kubernetes/enhancements/blob/adae507daeb490cfeb7f4d520d3d711362090c45/keps/NNNN-kep-template/README.md) 
by tailoring aspects of that process to fit the kcp project's needs.

#### Suggested action items

1. Definition of an enhancement proposal template
2. Creation of an enhancement proposal repository and removal of this proposal from the main repo to that repository.
3. Establishing the approval criteria for said process and the expectations for review and feedback in a way that can
be adopted by sub-projects identified later in this proposal.
   
This definition for enhancements should help establish the definition of done for an enhancement that includes any
documentation and testing requirements, helping establish clear expectations for authors as well as helping the 
community (as consumers of kcp) approach new features uniformly. 

#### Suggested timeline

Completed in: Q1 2023

Of the items in this proposal this process is likely to have the highest impact on our ability to collaborate 
on new items going forward.

### Create enhancements for existing APIs to document graduation criteria

As part of this proposal it is suggested that the project would benefit from a bit of "back porting" of documentation
to cover existing APIs. As a minimum bar it is proposed that we:

#### Suggested Action Items
1. Create an enhancement for each existing API in the main repository
2. Identify the current level of the API and the known development path
3. Identify the graduation criteria of the API
4. Link any existing work to the enhancement for the API
5. As an optional follow on, update the [kcp.io](https://kcp.io) site to include links to proposals as part of
   the documentation similar to [thanos](https://thanos.io/v0.29/proposals-done/202003-thanos-rules-federation.md/)
6. Present enhancements during community meetings

#### Suggested timeline

Completed by: end of Q3 2023

Existing APIs can adopt change via the enhancement proposal as they go. Our understanding of how we may move through 
alpha, beta, and v1 becomes more clear based on the rate of feedback. If nothing else, by that time documentation
should exist that minimizes the need to further document historical decisions.
   
### Split the workloads project from the control plane

Within kcp there exists the following components that can be thought of as individual investment areas. 

1. [kcp](https://github.com/kcp-dev/kcp) - the generic control plane with workspaces and API Import and API Export components
1. [kcp workloads](https://github.com/kcp-dev/kcp) - the transparent multi-cluster components
1. [kcp edge workloads](https://github.com/kcp-dev/edge-mc) - the transparent multi-cluster components focused on edge use cases
1. [kcp controllers dev](https://github.com/kcp-dev/controller-runtime) - the components focused on tooling and development of kcp aware controllers
1. [kcp catalog](https://github.com/kcp-dev/catalog) - discover component for published APIs

Right now the components that stand out as unnecessarily coupled are the generic control plane and workloads pieces.
This proposal suggests that each of these have a clear split. This scoping of responsibility enables a more clear
engagement model of where some of these components need to collaborate with other upstream projects who are also
interested in the same spaces.

#### Out of scope

Separation of the generic control plane from tenancy out of scope for now. It is possible 
that it is revisited in the future. 

#### Known dependencies
* kcp workloads will depend on kcp (same repo currently)
* kcp edge workloads may depend on kcp worklaods
* kcp catalog will [depend on kcp](https://github.com/kcp-dev/catalog/blob/4bb12851b66857b035b15e6d790b27af621d8693/go.mod#L6)

#### Suggested action items

1. new repo be created for kcp workloads
2. source code be moved as approporate
3. optionally, special interest groups may be established to own the processes and discussion around the areas 
   independently, but following the overall project's process model

#### Suggested timeline

Level of effort analysis completed in: Q1 2023

Target completion by: end of Q2 2023

Transparent multi-cluster features are under heavy development. An initial level of effort evaluation should be 
conducted in order to know the scope of this change in order to plan it in a way that is not overly disruptive to 
continuing on the critical path of any MVP development.

## Drawbacks

* May be considered too much, too soon.
* Slows down the contribution velocity of those already involved

## Alternatives

As an alternative, the project could do nothing and hope that a shared understanding of how contribution happens 
establishes itself based on a lead-by-example method or by inheriting the established patterns of other communities. This
proposal suggests that it is more beneficial to codify these items rather than rely on hope as a strategy. 