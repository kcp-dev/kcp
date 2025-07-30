# <img alt="Logo" width="80px" src="./contrib/logo/blue-green.png" style="vertical-align: middle;" /> kcp

[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/8119/badge)](https://www.bestpractices.dev/projects/8119)
[![Go Report Card](https://goreportcard.com/badge/github.com/kcp-dev/kcp)](https://goreportcard.com/report/github.com/kcp-dev/kcp)
[![GitHub](https://img.shields.io/github/license/kcp-dev/kcp)](https://github.com/kcp-dev/kcp/blob/main/LICENSE)
[![GitHub release (latest SemVer)](https://img.shields.io/github/v/release/kcp-dev/kcp?sort=semver)](https://github.com/kcp-dev/kcp/releases/latest)
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkcp-dev%2Fkcp.svg?type=shield)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkcp-dev%2Fkcp?ref=badge_shield)

## Overview

kcp is a Kubernetes-like control plane focusing on:

- A **control plane** for many independent, **isolated** “clusters” known as **workspaces**
- Enabling API service providers to **offer APIs centrally** using **multi-tenant operators**
- Easy **API consumption** for users in their workspaces

kcp can be a building block for SaaS service providers who need a **massively multi-tenant platform** to offer services
to a large number of fully isolated tenants using Kubernetes-native APIs. The goal is to be useful to cloud
providers as well as enterprise IT departments offering APIs within their company.

**NB:** In May 2023, the kcp project was restructured and components related to workload scheduling (e.g. the syncer) and the transparent multi cluster (tmc) code were removed due to lack of interest/maintainers. Please refer to the [`main-pre-tmc-removal` branch](https://github.com/kcp-dev/kcp/tree/main-pre-tmc-removal) if you are interested in the related code.

## Documentation

Please visit [docs.kcp.io/kcp](https://docs.kcp.io/kcp/latest) for our documentation.

## Contributing

We ❤️ our contributors! If you're interested in helping us out, please check out [contributing to kcp](https://docs.kcp.io/kcp/main/contributing/).

This community has a [Code of Conduct](./code-of-conduct.md). Please make sure to follow it.

## Getting in touch

There are several ways to communicate with us:

- The [`#kcp-users` channel](https://app.slack.com/client/T09NY5SBT/C021U8WSAFK) in the [Kubernetes Slack workspace](https://slack.k8s.io).
- Our mailing lists:
    - [kcp-dev](https://groups.google.com/g/kcp-dev) for development discussions.
    - [kcp-users](https://groups.google.com/g/kcp-users) for discussions among users and potential users.
- By joining the kcp-dev mailing list, you should receive an invite to our bi-weekly community meetings.
- See recordings of past community meetings on [YouTube](https://www.youtube.com/channel/UCfP_yS5uYix0ppSbm2ltS5Q).
- The next community meeting dates are available via our [CNCF community group](https://community.cncf.io/kcp/).
- Check the [community meeting notes document](https://docs.google.com/document/d/1PrEhbmq1WfxFv1fTikDBZzXEIJkUWVHdqDFxaY1Ply4) for future and past meeting agendas.
- Browse the [shared Google Drive](https://drive.google.com/drive/folders/1FN7AZ_Q1CQor6eK0gpuKwdGFNwYI517M?usp=sharing) to share design docs, notes, etc.
    - Members of the kcp-dev mailing list can view this drive.

## Additional references

- [Platform Engineering Day Europe 2024: Building a Platform Engineering API Layer with kcp – Marvin Beckers](https://www.youtube.com/watch?v=az5Rm8Snms4)
- [KubeCon EU 2024: Why Kubernetes Is Inappropriate for Platforms, and How to Make It Better – Stefan Schimanski, Mangirdas Judeikis, Sebastian Scheele](https://www.youtube.com/watch?v=7op_r9R0fCo)
- [KubeCon EU 2024: Kubernetes-style APIs for SaaS-like Control Planes with kcp – Marvin Beckers, Mangirdas Judeikis](https://www.youtube.com/watch?v=-P1kUo5zZR4)
- [KubeCon US 2022: Kcp: Towards 1,000,000 Clusters, Name^WWorkspaced CRDs - Stefan Schimanski](https://www.youtube.com/watch?v=fGv5dpQ8X5I)
- [Rejekts US 2022: What if namespaces provided more isolation than just names? – Stefan Schimanski](https://www.youtube.com/watch?v=WGrPUyx7qQE)
- [Let's Learn kcp - A minimal Kubernetes API server with Saiyam Pathak - July 7, 2021](https://www.youtube.com/watch?v=M4mn_LlCyzk)
- [TGI Kubernetes 157: Exploring kcp: apiserver without Kubernetes](https://youtu.be/FD_kY3Ey2pI)
- [K8s SIG Architecture meeting discussing kcp - June 29, 2021](https://www.youtube.com/watch?v=YrdAYoo-UQQ)
- [OpenShift Commons: Kubernetes as the Control Plane for the Hybrid Cloud - Clayton Coleman](https://www.youtube.com/watch?v=Y3Y11Aj_01I)
- [KubeCon EU 2021: Kubernetes as the Hybrid Cloud Control Plane Keynote - Clayton Coleman](https://www.youtube.com/watch?v=oaPBYUfdFE8)


## License
[![FOSSA Status](https://app.fossa.com/api/projects/git%2Bgithub.com%2Fkcp-dev%2Fkcp.svg?type=large)](https://app.fossa.com/projects/git%2Bgithub.com%2Fkcp-dev%2Fkcp?ref=badge_large)
