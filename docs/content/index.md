# kcp Documentation

[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/8119/badge)](https://www.bestpractices.dev/projects/8119)
[![Go Report Card](https://goreportcard.com/badge/github.com/kcp-dev/kcp)](https://goreportcard.com/report/github.com/kcp-dev/kcp)
[![GitHub](https://img.shields.io/github/license/kcp-dev/kcp)](https://github.com/kcp-dev/kcp/blob/main/LICENSE)
[![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/kcp-dev/kcp?sort=semver)](https://github.com/kcp-dev/kcp/releases/latest)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkcp-dev%2Fkcp.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkcp-dev%2Fkcp?ref=badge_shield)

!!! tip ""
    Looking for other project documentation? Check out: [api-syncagent](https://docs.kcp.io/api-syncagent) | [kcp-operator](https://docs.kcp.io/kcp-operator)

## Overview

kcp is a Kubernetes-like control plane focusing on:

- A **control plane** for many independent, **isolated** “clusters” known as **workspaces**
- Enabling API service providers to **offer APIs centrally** using **multi-tenant operators**
- Easy **API consumption** for users in their workspaces
- **Advanced deployment strategies** for scenarios such as affinity/anti-affinity, geographic replication, cross-cloud
  replication, etc.

kcp can be a building block for SaaS service providers who need a **massively multi-tenant platform** to offer services
to a large number of fully isolated tenants using Kubernetes-native APIs. The goal is to be useful to cloud
providers as well as enterprise IT departments offering APIs within their company.

## Quickstart

To get started with trying out kcp on your local system, check out our [Quickstart](./setup/quickstart.md) instructions.

## Contributing

We ❤️ our contributors! If you're interested in helping us out, please head over to our [Contributing](./contributing/index.md)
guide.


## Getting in touch

There are several ways to communicate with us:

- On the [Kubernetes Slack workspace](https://slack.k8s.io).
    - [`#kcp-users`](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) for discussions and questions regarding kcp's setup and usage.
    - [`#kcp-dev`](https://kubernetes.slack.com/archives/C09C7UP1VLM) for conversations about developing kcp itself.
- Our mailing lists.
    - [kcp-users](https://groups.google.com/g/kcp-users) for discussions among users and potential users.
    - [kcp-dev](https://groups.google.com/g/kcp-dev) for development discussions.
- The bi-weekly community meetings.
    - By joining the kcp-dev mailing list, you should receive an invite to our bi-weekly community meetings.
    - The next community meeting dates are also available via our [CNCF community group](https://community.cncf.io/kcp/).
    - Check the [community meeting notes document](https://docs.google.com/document/d/1PrEhbmq1WfxFv1fTikDBZzXEIJkUWVHdqDFxaY1Ply4) for future and past meeting agendas.
    - See recordings of past community meetings on [YouTube](https://www.youtube.com/channel/UCfP_yS5uYix0ppSbm2ltS5Q).
- Browse the [shared Google Drive](https://drive.google.com/drive/folders/1FN7AZ_Q1CQor6eK0gpuKwdGFNwYI517M?usp=sharing) to share design docs, notes, etc.
    - Members of the kcp-dev mailing list can view this drive.

## Additional references

- [KubeCon EU 2021: Kubernetes as the Hybrid Cloud Control Plane Keynote - Clayton Coleman (video)](https://www.youtube.com/watch?v=oaPBYUfdFE8)
- [OpenShift Commons: Kubernetes as the Control Plane for the Hybrid Cloud - Clayton Coleman (video)](https://www.youtube.com/watch?v=Y3Y11Aj_01I)
- [TGI Kubernetes 157: Exploring kcp: apiserver without Kubernetes](https://youtu.be/FD_kY3Ey2pI)
- [K8s SIG Architecture meeting discussing kcp - June 29, 2021](https://www.youtube.com/watch?v=YrdAYoo-UQQ)
- [Let's Learn kcp - A minimal Kubernetes API server with Saiyam Pathak - July 7, 2021](https://www.youtube.com/watch?v=M4mn_LlCyzk)
