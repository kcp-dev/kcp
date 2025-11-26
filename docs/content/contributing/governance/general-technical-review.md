---
title: General Technical Review
---

# General Technical Review - kcp / Incubating

- **Project:** kcp
- **Project Version:** v0.28.1
- **Website:** [kcp.io](https://kcp.io)
- **Date Updated:** 2025-09-26
- **Template Version:** v1.0
- **Description:** kcp is an open source horizontally scalable control plane for Kubernetes-like APIs. It extends the Kubernetes Resource Model with additional multi-tenancy and API management capabilities.


## Day 0 – Planning Phase

### Scope

  * **Describe the roadmap process, how scope is determined for mid to long term features, as well as how the roadmap maps back to current contributions and maintainer ladder?**
    
    Our public roadmap is tracked in [GitHub milestones](https://github.com/kcp-dev/kcp/milestones). Scope is usually determined in the bi-weekly community calls, i.e. ideas with a larger impact on kcp as a project are brought there to be discussed and scheduled into the overall development roadmap for the next few releases.
    
    Roadmap disputes are at worst solved by a maintainer vote on the public mailing list. If maintainers couldn't agree, they would seek an outside arbiter.
    
    An enhancement proposal process has been decided upon but not yet implemented.

  * **Describe the target persona or user(s) for the project?**

    * **Platform Owners**: Central stakeholders tasked with providing a central platform through which IT services can be offered in a centralized and standardized way.
    * **Service Providers**: Internal or external entities that provide a specialized IT service. Their focus is providing a high quality service (e.g. a DBaaS), but they need to provide a way to "order" instances of their DBaaS through an API.
    * **Service Consumers**: A diverse set of stakeholders that want to use IT services provided by a *Service Provider*. This might include software engineers, data analysts or management roles.

  * **Explain the primary use case for the project. What additional use cases are supported by the project?**

    The primary use case for kcp is providing a global control plane for declarative, Kubernetes-like APIs. It embraces the strength of the Kubernetes API server while adding opinionated multi-tenancy features not present in the upstream project. Given the nature of kcp, the project functions as a generic declarative control plane. As such, it can be used in a variety of use cases, orchestrating any kind of IT systems. kcp itself doesn't directly publish code for integration with such arbitrary systems, but provides tooling (e.g. a provider for [mulitcluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime)) to write such integrations.

    The project directly supports the use case of publishing Kubernetes CRDs from multiple Kubernetes clusters into a central kcp instance as a global control plane through the [api-syncagent](https://github.com/kcp-dev/api-syncagent) project. *Service Consumers* can then create objects in kcp that get synchronized back to the target Kubernetes cluster.

  * **Explain which use cases have been identified as unsupported by the project.**
    
    In the past, kcp included a "transparent multi-cluster" (TMC) component in the project core. Since then, it has been identified as out-of-scope for the "core" kcp project, but it is feasible to be implemented as an application on top of kcp.
    
    In general, adding application-specific logic into kcp itself is considered out of scope for the project. Specifically, container orchestration is out-of-scope for kcp.

  * **Describe the intended types of organizations who would benefit from adopting this project. (i.e. financial services, any software manufacturer, organizations providing platform engineering services)?**

    * (Cloud) Service Providers, i.e. organizations which offer IT services on their infrastructure. These organizations would benefit from adopting kcp by either:
        * making it their internal control plane that orchestrates a global deployment of IT services across several regions / datacenters.
        * or by using it as a central contorol plane for "ordering" IT services, i.e. replacing custom REST APIs with the standardized KRM interface that kcp provides.
    * (Large) corporations looking for an internal (developer) platform, trying to bundle their various internal IT services in one central API platform, while keeping strong multi-tenancy separation between API owners.
    * Software Vendors looking for a control plane for their complex software stack that requires orchestration across multiple Kubernetes clusters and/or regions.

  * **Please describe any completed end user research and link to any reports.**

    None completed we are aware of.


### Usability

  * **How should the target personas interact with your project?**
    
    All personas primarily interact with kcp via `kubectl`, the Kubernetes command line client. kcp provides several [kubectl plugins](https://docs.kcp.io/kcp/latest/setup/kubectl-plugin/) for navigating multi-tenancy concepts not known to `kubectl`.
    
    Navigation between workspaces happens with the `kubectl-ws` plugin, which allows changing workspaces similar to changing directories:

    ```bash
    $ kubectl ws test
    Current workspace is 'root:test' (type root:organization).
    $ kubectl ws .
    Current workspace is 'root:test'.
    $ kubectl ws ..
    Current workspace is 'root'.
    $ kubectl ws :root:test
    Current workspace is 'root:test' (type root:organization).
    ```

    kcp "speaks" Kubernetes' API model, the KRM, and thus its API is compatible with existing Kubernetes API tooling like [client-go](https://github.com/kubernetes/client-go). Personas that automate against the kcp API (e.g. writing a kcp-aware controller) can use upstream libraries like `k8s.io/client-go` or [multicluster-runtime](https://github.com/kubernetes-sigs/multicluster-runtime) in combination with kcp specific extensions to those ([kcp-dev/client-go](https://github.com/kcp-dev/client-go) and [kcp-dev/multicluster-provider](https://github.com/kcp-dev/multicluster-provider), respectively) for programmatic access.

    A graphical user interface (i.e. a web portal) is feasible but not in scope for kcp.

  * **Describe the user experience (UX) and user interface (UI) of the project.**
    * **User Experience**: The user experience of kcp is defined by its Kubernetes-like API. Therefore interactions with kcp are primarily driven by creating and updating declarative object states stored in API endpoints which are then reconciled by controllers/operators. All personas primarily interact with the API.
    * **User Interface**: kcp doesn't provide its own user interface and instead relies on users using `kubectl` or other user interfaces to interact with kcp through the Kubernetes Resource Model.

  * **Describe how this project integrates with other projects in a production environment.**
    
    kcp integrates with a variety of other projects when used in a production environment. Production setups are recommended to be installed on Kubernetes. Same as Kubernetes, it provides several interfaces that allow plugging in different projects. For example:

      * [Admission Webhooks](https://docs.kcp.io/kcp/latest/concepts/apis/admission-webhooks/) to integrate with any project that supports Kubernetes admission webhooks like [OPA Gatekeeper](https://open-policy-agent.github.io/gatekeeper/website/).
      * [Webhook Authorization](https://docs.kcp.io/kcp/latest/concepts/authorization/authorizers/#webhook-authorizer) allows integration of third-party authorization tools like [OpenFGA](https://openfga.dev/).
      * [OIDC Authentication](https://docs.kcp.io/kcp/latest/concepts/authentication/oidc/) allows using identity providers like [Dex](https://dexidp.io) or [Keycloak](https://keycloak.org) to authenticate users.
      * kcp's [api-syncagent](https://docs.kcp.io/api-syncagent) allows for integration with any controller/operator based service running in external Kubernetes clusters like [Crossplane](https://crossplane.io/).

### Design

  * **Explain the design principles and best practices the project is following.**
    
    Design principles are documented [here](https://docs.kcp.io/kcp/latest/GOALS/#principles). Below is a list of them:

      * Convention over configuration / optimize for the user's benefit.
      * Support both a push model and a pull model that fit the control plane mindset.
      * Balance between speed of local development AND running as a high scale service.
      * Be simple, composable, and orthogonal.
      * Be open to change.
      * Consolidate efforts in the ecosystem into a more focused effort.

  * **Outline or link to the project’s architecture requirements? Describe how they differ for Proof of Concept, Development, Test and Production environments, as applicable.**
    
    kcp can be installed on top of a Kubernetes cluster and primarily requires a mean to expose its API endpoint (e.g. load balancer support in the Kubernetes cluster). The requirements for that don't significantly change between environments. Specifically for development, the `kcp` binary has a "all-in-one" mode that makes local development against kcp possible.

    For test and production environments it is strongly encouraged to run a sharded setup to validate that integrations correctly work with multiple shards.

  * **Define any specific service dependencies the project relies on in the cluster.**

    kcp requires an [etcd](https://etcd.io)-compatible datastore to store its data in. This can be a single etcd instance, an etcd cluster or any other project that provides an etcd-compatible API.

    Installation by Helm chart has a dependency on [cert-manager](https://cert-manager.io) to manage mTLS certificates between kcp's components, but kcp itself does not rely on cert-manager, just on certificates being passed to it.

  * **Describe how the project implements Identity and Access Management.**

    kcp builds on top of Kubernetes' kube-apiserver code and as such, implements similar authentication and authorization methods. Specifically, kcp supports Kubernetes' Role-Based Access Control (RBAC) to assign permissions to user identities. kcp adds a few verbs and subresources to "stock" Kubernetes RBAC, which are documented [here](https://docs.kcp.io/kcp/latest/concepts/authorization/authorizers/).

  * **Describe how the project has addressed sovereignty.**
    
    kcp can be entirely self-hosted on a Kubernetes cluster. All data is stored in an etcd instance, which can be fully managed by the installation owner. Except for container images, no access to internet resources is required, and thus a kcp setup can be run fully air-gapped to address any data sovereignty concerns.

  * **Describe any compliance requirements addressed by the project.**

    As kcp can be run fully self-hosted, it might address any compliance requirements that require full ownership of all data. It might fulfil other compliance requirements as well, but no research has been done in this area so far.

  * **Describe the project’s High Availability requirements.**

    kcp requires a HA etcd cluster (so 3 or 5 etcd nodes creating a cluster) to run in HA itself. kcp scales horizontally, so multiple kcp processes pointing to the same etcd cluster can be started to accept more traffic. Between those kcp processes, a leader election (via Kubernetes-style `Lease` objects) takes place to ensure that in case of a crash, another kcp process takes over active reconciliation of the kcp instance. To scale horizontally for handling more workspaces (i.e. more "tenants" on kcp), sharding can be utilized. Shards are kcp processes pointing to different etcd instances, thus handling a different subset of workspaces stored on the particular datastore of the shard. The front-proxy component routes requests to the right shard depending on target workspace.

  * **Describe the project’s resource requirements, including CPU, Network and Memory.**

    A default installation from the Helm chart requires at least:
    
      * 1.5 cpu + 6GB RAM for three-node etcd cluster
      * 0.1 cpu + 512MB RAM for kcp server
      * 0.1 cpu + 128MB RAM for kcp-front-proxy

    Actual memory requirements for the kcp server depend on the amount of workspaces and objects stored in it. For capacity planning, at least 4MB should be planned per workspace (this does not include objects in said workspace).

  * **Describe the project’s storage requirements, including its use of ephemeral and/or persistent storage.**

    kcp itself does not require any storage. It uses etcd to store all its data, thus storage requirements primarily depend on etcd's requirements.

  * **Please outline the project’s API Design:**
    * **Describe the project’s API topology and conventions**

        kcp implements the [Kubernetes Resource Model](https://github.com/kubernetes/design-proposals-archive/blob/main/architecture/resource-management.md). As such, its API is compatible with Kubernetes, and the same topology and conventions apply.

    * **Describe the project defaults**

        kcp attempts to provide a good experience both for developers and users. As such, the project's "default" configuration is targeted at running kcp standalone on a local machine. It is as easy as running `kcp start`, which will start kcp with all components embedded into a single binary, including an etcd server for storing data. The `kcp` binary has the concepts of "batteries" with some of them enabled by default to ease the initial use of kcp (following a "batteries included" approach).

        With that being said, the kcp project considers the Helm chart to be the default for deployment on a Kubernetes cluster. By default this deploys an etcd cluster alongside it.

    * **Outline any additional configurations from default to make reasonable use of the project**

        kcp provides a multitude of command line options to configure its behaviour. A complete list can be accessed by running `kcp start options`, with most of the options derived from kube-apiserver.
        
        A few configuration options that would be useful are:
        
         * `--authorization-webhook-config-file` allows referencing a [webhook configuration file](https://docs.kcp.io/kcp/latest/concepts/authorization/authorizers/#webhook-authorizer) for authorization via a webhook.
         * Several `--oidc-*` flags exist to enable and configure OIDC authentication. Alternatively, `--authentication-config` can be used to reference a [structured authentication configuration file](https://docs.kcp.io/kcp/main/concepts/authentication/oidc/#configure-kcp-oidc-authentication-using-structured-authentication-configuration).
         * `--audit-webhook-config-file` allows referencing a configuration file for an audit webhook endpoint. An audit policy can be configured via `--audit-policy-file`.

    * **Describe any new or changed API types and calls \- including to cloud providers \- that will result from this project being enabled and used**

        kcp itself provides an API and doesn't register any CRDs with the underlying cluster (unless kcp-operator is used to deploy it).

        The main API resources a typical user would interact with are `Workspaces`, `APIExports` and `APIBindings`. While `Workspaces` allow to create multi-tenant units in kcp (see below), `APIExports` and `APIBindings` iterate on the idea of Custom Resource Definitions and provide a provider/consumer split for them: A persona that wants to offer a KRM-style API creates an `APIExport` and grants permissions to other entities, and those entities can create `APIBindings` to "bind" the provided APIs into their own workspace.

    * **Describe compatibility of any new or changed APIs with API servers, including the Kubernetes API server**

        Since kcp implements the Kubernetes Resource Model and is in fact based on the kube-apiserver code, it is compatible with with most tools and clients meant for Kubernetes.
        
        The main addition of kcp to a Kubernetes-style API is the concept of logical clusters, kcp's multi-tenancy unit. A kcp instance doesn't provide Kubernetes API resources under one unified endpoint, instead it provides access to multiple endpoints that each act as fully independent Kubernetes API endpoints. This means that e.g. `/clusters/a` and `/clusters/b` are both Kubernetes-compatible API endpoints, but they return different API resources and objects.

        As such, each logical cluster can be accessed with a Kubernetes client (e.g. `kubectl`) and switching between them is possible via a `kubectl` plugin provided by the kcp project. It can also be done manually by updating server URLs (see the `/clusters/` schema above). Logical clusters have dedicated resources, objects and RBAC.

    * **Describe versioning of any new or changed APIs, including how breaking changes are handled**

        kcp versions its API resources according to the Kubernetes Resource Model (so e.g. `v1alpha1` -> `v1beta1` -> `v1`). Breaking changes are only allowed between new API versions and are handled by conversion logic that retains all information only available in one API version to ensure API fidelity.

  * **Describe the project’s release processes, including major, minor and patch releases.**

    kcp has its [release process publicly documented](https://docs.kcp.io/kcp/main/contributing/guides/publishing-a-new-kcp-release/).
    Releases are published by the CI/CD pipelines (Prow and GitHub Actions) after a git tag has been pushed. As such, automation handles the majority of the release process.
    
    Minor and patch releases are relatively uniform in their release process, the major difference is which branch the new release is cut from. New major releases have not been cut so far and would require bumping Go modules to include the version name, which would require changes across the codebase.

### Installation

  * **Describe how the project is installed and initialized, e.g. a minimal install with a few lines of code or does it require more complex integration and configuration?**
    
    * A Helm chart is available for installation on Kubernetes. A full installation walkthrough is available [here](https://github.com/kcp-dev/helm-charts/tree/main/charts/kcp), but generally speaking, installation is as easy as `helm install`.

        The main consideration is how to make the kcp API endpoint accessible. Several expose strategies are documented, the primary task outside of configuring the Helm chart is setting up the proper DNS records for the chosen external DNS name.

    * An [operator](https://docs.kcp.io/kcp-operator) is also available to run multi-shard kcp setups.

    * kubectl plugins are available via krew:

        ```bash
        $ kubectl krew index add kcp-dev https://github.com/kcp-dev/krew-index.git
        $ kubectl krew install kcp-dev/kcp
        $ kubectl krew install kcp-dev/ws
        $ kubectl krew install kcp-dev/create-workspace
        ```


  * **How does an adopter test and validate the installation?**

    They follow the [Helm chart setup walkthrough](https://github.com/kcp-dev/helm-charts/tree/main/charts/kcp). This README file will provide instructions on how to generate credentials to access kcp and connect to it via `kubectl` to validate it is responding to requests.

### Security

  * **Please provide a link to the project’s cloud native [security self assessment](https://tag-security.cncf.io/community/assessments/).**

    The [self-assessment is available on docs.kcp.io](https://docs.kcp.io/kcp/main/contributing/governance/security-self-assessment/).

  * **Please review the [Cloud Native Security Tenets](https://github.com/cncf/tag-security/blob/main/community/resources/security-whitepaper/secure-defaults-cloud-native-8.md) from TAG Security.**
    * **How are you satisfying the tenets of cloud native security projects?**

      * Secure by Design: The core architectural concept of the Workspace provides strong tenant isolation by design. Each workspace is a logically separate cluster with its own scoped RBAC, preventing tenants from accessing each other's resources. For test and production environments it is strongly encouraged to run a sharded setup to validate that integrations correctly work with multiple shards.

      * Automate and Enforce Policy: Policy is enforced through standard Kubernetes RBAC, which is a declarative, API-driven model well-suited for automation. All access control is defined as code in Role and RoleBinding objects.

      * Defense in Depth: kcp enables a defense-in-depth strategy. An operator can secure the underlying infrastructure running kcp, use kcp's workspace-scoped RBAC for control plane isolation, and then implement additional security in the controllers and applications built on top of kcp.

      * Assume a Compromised Environment (Zero Trust): The strong isolation of workspaces helps contain the blast radius of a compromise. A breach within one tenant's workspace does not automatically grant access to others. The APIExport/APIBinding model acts as an explicit, policy-controlled gateway for cross-workspace communication.

      * Clarity of Security Responsibility: The project's focus as a pure control plane framework creates a clear line of responsibility. The kcp project is responsible for the security of the control plane software itself. The operator is responsible for securing the underlying infrastructure where kcp runs and for correctly configuring its IAM policies (authentication and RBAC).

    * **Describe how each of the cloud native principles apply to your project.**

        * kcp is **secure** by default by encrypting (with TLS), authenticating (with mTLS or OIDC) and authorizing (with Kubernetes RBAC) requests made to it.
    
        * kcp is **resilient** by supporting a High Availability setup, in which individual kcp processes can crash or restart without the kcp instance having reduced availability.
    
        * kcp **manageable** by being KRM driven and exposing its main configuration primitives via its Kubernetes-like API, i.e. `Workspaces` allow creating new units of its multitenancy boundary.

        * kcp is **sustainable** by avoiding a vendor lock-in and instead building on top of the Kubernetes Resource Model, which subsequently allows the project to build on top of Kubernetes technology both on the server and the client side (i.e. to interact with kcp, you can use known client tools or client libraries with some extensions that the kcp project develops).

        * kcp is **observable** because it produces logs and Prometheus metrics, which can be scraped and stored for reporting and alerting purposes.

    * **How do you recommend users alter security defaults in order to "loosen" the security of the project? Please link to any documentation the project has written concerning these use cases.**

        kcp in general does not recommend to "loosen" security settings. With that being said, it supports a variety of flags that kube-apiserver supports as well, so it is possible to change/disable authorization methods like RBAC, disable certain authorizers or admission controllers, etc. Those options are "documented" in the `kcp start options` command.

  * **Security Hygiene**
    * **Please describe the frameworks, practices and procedures the project uses to maintain the basic health and security of the project.**

        * Vulnerability Management: kcp has a defined security policy and a private process for reporting vulnerabilities, either through GitHub's security advisory feature or a dedicated private email address (kcp-dev-private@googlegroups.com). This allows for coordinated disclosure.   

        * Security Response Committee: A formal committee of project maintainers is responsible for triaging and responding to security reports in a timely manner.   

        * Public Advisories: Once a vulnerability is addressed, kcp publishes public security advisories on GitHub to inform users. Past advisories for both "Critical" and "Moderate" severity issues are available, demonstrating the process is active.   

        * Dependency Scanning: as shown in release notes, dependencies are regularly updated to address known CVEs, showing that dependency scanning is part of the release process. kcp uses GitHub's Dependabot feature to be informed about known dependency vulnerabilities.

    * **Describe how the project has evaluated which features will be a security risk to users if they are not maintained by the project?**

         The most critical security feature was workspace isolation. A bug in the isolation logic would be a severe security risk. This was the area of highest scrutiny in design and code review. The security self-assessment gives more insights into which elements of kcp are considered security surfaces from which security risks can stem.

  * **Cloud Native Threat Modeling**
    * **Explain the least minimal privileges required by the project and reasons for additional privileges.**

        The project needs full access to an etcd instance to store data. Additional privileges are not required by the project itself but its installation methods (e.g. for the Helm chart, you need to be allowed to create various objects in Kubernetes).

    * **Describe how the project is handling certificate rotation and mitigates any issues with certificates.**

        kcp is based on Kubernetes code and follows the same principles. Certificates can be rotated by restarting kcp with a new set of certificates (in a HA setup, this can be done without downtime). Issues with (client) certificates are generally mitigated by removing permissions assigned to the client certificate in RBAC.

    * **Describe how the project is following and implementing [secure software supply chain best practices](https://project.linuxfoundation.org/hubfs/CNCF\_SSCP\_v1.pdf)**

        kcp secures the source code by ensuring minimal permissions of contributors on the GitHub repositories. Instead, PR merge automation in form of Prow is enabled. Through Prow configuration, it is not possible for the PR author to approve their own PR, enforcing a four-eyes principle. Branch protection is automated via configuration in the [kcp-dev/infra](https://github.com/kcp-dev/infra) repository. kcp uses GitHub features to track dependencies and vulnerabilities in them and ensure no secrets are pushed.
        
        The kcp build infrastructure is deployed with OpenTofu, with deployment to it automated via the same kcp-dev/infra repository and minimal direct access (i.e. only a small subset of maintainers have access to troubleshoot issues in automation). Since Prow is based on containers, it is easy to reproduce build environments by using the same container image referenced in a Prow job definition. Job / pipeline definition is stored in code and is subject to the same review process as application code changes.
        
        The kcp project uses GitHub to define teams and associate those teams with specific permissions. As such, only maintainers have elevated permissions on the GitHub organization, and most members have read-only access to the repositories and settings of [github.com/kcp-dev](https://github.com/kcp-dev).

## Day 1 – Installation and Deployment Phase

### Project Installation and Configuration

  * **Describe what project installation and configuration look like.**

    Installation depends on the exact method chosen: Currently, kcp supports a [Helm chart](https://github.com/kcp-dev/helm-charts) and an [operator](https://github.com/kcp-dev/kcp-operator).
    
      * The Helm chart is configured via a Helm values file. This file configures a variety of behavior for the deployed kcp installation, most importantly the external hostname under which the kcp instance will be accessible.

        A minimal Helm values file would look like this:

        ```yaml
        externalHostname: "example.kcp.io"

        kcpFrontProxy:
          service:
            type: LoadBalancer
        ```

        Which would then be installed with the following command:

        ```bash
        $ helm repo add kcp https://kcp-dev.github.io/helm-charts
        $ helm upgrade --install --namespace kcp --create-namespace kcp kcp/kcp -f values.yaml
        ```

        kcp would then start and look similar to this:
    
        ```bash
        $ kubectl get pods
        NAME                                      READY   STATUS    RESTARTS      AGE
        kcp-7544fcb98d-7kz8x                      1/1     Running   0             1d
        kcp-7544fcb98d-tmv5n                      1/1     Running   0             1d
        kcp-etcd-0                                1/1     Running   0             1d
        kcp-etcd-1                                1/1     Running   0             1d
        kcp-etcd-2                                1/1     Running   0             1d
        kcp-front-proxy-7f6b7dfdbf-7fr4d          1/1     Running   0             1d
        kcp-front-proxy-7f6b7dfdbf-7thwx          1/1     Running   0             1d
        ```
        
        To generate credentials to access it, [a client certificate needs to be generated](https://github.com/kcp-dev/helm-charts/tree/main/charts/kcp#initial-access).

        Eventually, accessing kcp is possible via `kubectl`:

        ```bash
        $ KUBECONFIG=$(pwd)/kcp.kubeconfig kubectl get workspaces
        No resources found
        ```

      * kcp-operator comes with several CRDs that can be installed into a Kubernetes cluster to host one or more kcp instances. It allows to create a `RootShard`, one or more `FrontProxy` installations and additional `Shards`. CRDs are documented [here](https://docs.kcp.io/kcp-operator/v0.2/reference/). The operator enables deployment of more complex kcp setups through this flexibility.

        Installation of the kcp-operator is done via Helm:

        ```bash
        $ helm repo add kcp https://kcp-dev.github.io/helm-chart
        $ helm upgrade --install --namespace kcp-operator --create-namespace kcp-operator kcp/kcp-operator
        ```

        See the [quickstart guide](https://docs.kcp.io/kcp-operator/v0.2/setup/quickstart/) for a full walkthrough.

        Here is what a `RootShard` would look like getting started:

        ```yaml
        apiVersion: operator.kcp.io/v1alpha1
        kind: RootShard
        metadata:
          name: root
        spec:
          external:
            # replace the hostname with the external DNS name for your kcp instance
            hostname: example.operator.kcp.io
            port: 6443
          certificates:
            # this references the issuer created above
            issuerRef:
              group: cert-manager.io
              kind: Issuer
              name: selfsigned
          cache:
            embedded:
              # kcp comes with a cache server accessible to all shards,
              # in this case it is fine to enable the embedded instance
              enabled: true
          etcd:
            endpoints:
              # this is the service URL to etcd. Replace if Helm chart was
              # installed under a different name or the namespace is not "default"
              - http://etcd.default.svc.cluster.local:2379
        ```

        In addition, at least one `FrontProxy` is required:

        ```yaml
        apiVersion: operator.kcp.io/v1alpha1
        kind: FrontProxy
        metadata:
          name: frontproxy
        spec:
          rootShard:
            ref:
              # the name of the RootShard object created before
              name: root
          serviceTemplate:
            spec:
              # expose this front-proxy via a load balancer
              type: LoadBalancer
        ```

        To gain access credentials, a `Kubeconfig` object is required:

        ```yaml
        apiVersion: operator.kcp.io/v1alpha1
        kind: Kubeconfig
        metadata:
          name: kubeconfig-kcp-admin
        spec:
          # the user name embedded in the kubeconfig
          username: kcp-admin
          groups:
            # system:kcp:admin is a special privileged group in kcp.
            # the kubeconfig generated from this should be kept secure at all times
            - system:kcp:admin
          # the kubeconfig will be valid for 365d but will be automatically refreshed
          validity: 8766h # 1 year
          secretRef:
            # the name of the secret that the assembled kubeconfig should be written to
            name: admin-kubeconfig
          target:
            # a reference to the frontproxy deployed previously so the kubeconfig is accepted by it
            frontProxyRef:
              name: frontproxy
        ```

        The following command writes the generated kubeconfig to disk:

        ```bash
        $ kubectl get secret admin-kubeconfig -o jsonpath="{.data.kubeconfig}" | base64 -d > admin.kubeconfig
        ```

### Project Enablement and Rollback

  * **How can this project be enabled or disabled in a live cluster? Please describe any downtime required of the control plane or nodes.**

    kcp is run like any service within a cluster, hence there isn't a way to enable or disable it beyond installing or removing its resources. In specific, it is its own control plane and therefore doesn't directly integrate with the Kubernetes API in any critical capacity.

  * **Describe how enabling the project changes any default behavior of the cluster or running workloads.**
    
    As a standalone control plane, kcp does not change behavior of the underlying cluster or workloads running alongside it.

  * **Describe how the project tests enablement and disablement.**

    Since no enablement/disablement exists for kcp, this is also not tested.

  * **How does the project clean up any resources created, including CRDs?**
    
    The two installation methods (Helm chart or operator) both include cleanup logic (Helm cleans up resources created by it, the operator uses owner references) when uninstalling a kcp instance.

### Rollout, Upgrade and Rollback Planning

  * **How does the project intend to provide and maintain compatibility with infrastructure and orchestration management tools like Kubernetes and with what frequency?**
    
    kcp intends to maintain compatibility with all upstream supported Kubernetes minor versions at the time of a kcp release. kcp minor releases happen every 3-4 months, which is the frequency at which compatibility with Kubernetes is evaluated.

  * **Describe how the project handles rollback procedures.**

    Upgrading the kcp server would follow a standard rolling update pattern if deployed in a HA configuration. A rollback would involve redeploying the previous version of the server binary. Because state is in etcd, a stateless rollback of the server is possible as long as there are no breaking API changes in the stored etcd data between versions. Major version upgrades might require a data migration strategy.

  * **How can a rollout or rollback fail? Describe any impact to already running workloads.**

    A rollout/rollback could fail if the new/old version of kcp cannot read the data format in etcd written by the other version (e.g. if a new API version has been used already). This would cause the control plane to become unavailable. However, this would not impact already running workloads on any physical clusters orchestrated by additional components such as api-syncagent, as they run independently. The impact would be an inability to schedule new service instances, update existing ones, or otherwise interact with the Kubernetes-like API of kcp until the control plane is restored.

  * **Describe any specific metrics that should inform a rollback.**
    
    Request level metrics derived from the kube-apiserver codebase that kcp is based on, in particular a high amount of 5xx HTTP errors reported.

  * **Explain how upgrades and rollbacks were tested and how the upgrade-\>downgrade-\>upgrade path was tested.**

    Upgrades and rollbacks are tested manually before a minor release is released. The test is conducted by changing the deployed kcp version back and forth and interacting with kcp.

  * **Explain how the project informs users of deprecations and removals of features and APIs.**

    APIs are marked as deprecated in API field descriptions and release notes. As this follows the same patterns that Kubernetes (and CRDs) use, tooling (like linters) will inform integrators that import the kcp API SDK about API field deprecations.
    
    API removal happens after a grace period following deprecation and is communicated in the release notes.

  * **Explain how the project permits utilization of alpha and beta capabilities as part of a rollout.**
    
    kcp provides access to Kubernetes feature gates and adds its own feature gates to make sure such capabilities are only used with intention. Feature gates are configured via the flag `--feature-gates` on the kcp binary.

<!--

TODO: Uncomment and fill this part of the document when applying for Graduated.

## Day 2 \- Day-to-Day Operations Phase

### Scalability/Reliability

  * Describe how the project increases the size or count of existing API objects.
  * Describe how the project defines Service Level Objectives (SLOs) and Service Level Indicators (SLIs).  
  * Describe any operations that will increase in time covered by existing SLIs/SLOs.  
  * Describe the increase in resource usage in any components as a result of enabling this project, to include CPU, Memory, Storage, Throughput.  
  * Describe which conditions enabling / using this project would result in resource exhaustion of some node resources (PIDs, sockets, inodes, etc.)  
  * Describe the load testing that has been performed on the project and the results.  
  * Describe the recommended limits of users, requests, system resources, etc. and how they were obtained.  
  * Describe which resilience pattern the project uses and how, including the circuit breaker pattern.

### Observability Requirements

  * Describe the signals the project is using or producing, including logs, metrics, profiles and traces. Please include supported formats, recommended configurations and data storage.  
  * Describe how the project captures audit logging.  
  * Describe any dashboards the project uses or implements as well as any dashboard requirements.  
  * Describe how the project surfaces project resource requirements for adopters to monitor cloud and infrastructure costs, e.g. FinOps  
  * Which parameters is the project covering to ensure the health of the application/service and its workloads?  
  * How can an operator determine if the project is in use by workloads?  
  * How can someone using this project know that it is working for their instance?  
  * Describe the SLOs (Service Level Objectives) for this project.
  * What are the SLIs (Service Level Indicators) an operator can use to determine the health of the service?

### Dependencies

  * Describe the specific running services the project depends on in the cluster.  
  * Describe the project’s dependency lifecycle policy.  
  * How does the project incorporate and consider source composition analysis as part of its development and security hygiene? Describe how this source composition analysis (SCA) is tracked.
  * Describe how the project implements changes based on source composition analysis (SCA) and the timescale.

### Troubleshooting

  * How does this project recover if a key component or feature becomes unavailable? e.g Kubernetes API server, etcd, database, leader node, etc.  
  * Describe the known failure modes.

### Security

  * Security Hygiene  
    * How is the project executing access control?  
  * Cloud Native Threat Modeling  
    * How does the project ensure its security reporting and response team is representative of its community diversity (organizational and individual)?  
    * How does the project invite and rotate security reporting team members?

-->
