# Copyright 2026 The kcp Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# kcp_static.Tiltfile — reusable, parameterized static kcp install.
#
# This module exposes a single function, deploy_kcp(), that generates the
# kcp-operator custom resources (RootShard, Shard, FrontProxy, their TLSRoutes
# and admin Kubeconfigs) and extracts the admin kubeconfigs. It deliberately
# does NOT provision infrastructure (cert-manager, the Gateway API
# implementation, etcd, the kcp-operator itself, observability): the caller
# owns those, because a parent project already brings its
# own gateway, etcd and cert issuer and only needs kcp wired on top.
#
# Loaded two ways:
#   * kcp's own contrib/tilt/Tiltfile.static — installs the infra inline, then
#     calls deploy_kcp() with the envoy/kcp.localhost defaults.
#   * a downstream project — installs ITS infra, then calls deploy_kcp() with
#     its own gateway name/namespace, hostnames, authorization webhook and
#     OIDC issuer.
#
# Because the module reads no sibling files (every manifest is built in
# Starlark), it is safe to load() across repositories where relative paths
# would otherwise resolve against the parent Tiltfile's directory.

# ---------------------------------------------------------------------------
# manifest builders
# ---------------------------------------------------------------------------

def _host_aliases(cfg):
    # A single hostAliases entry pointing every kcp hostname at the gateway's
    # fake ingress IP so in-cluster clients resolve the same names as external
    # ones. Returns the value for spec.template.spec.hostAliases.
    if not cfg["hostalias_ip"]:
        return None
    return [{
        "ip": cfg["hostalias_ip"],
        "hostnames": cfg["hostnames"],
    }]


def _deployment_template(cfg, extra_annotations = None):
    spec = {}
    aliases = _host_aliases(cfg)
    pod = {}
    if aliases:
        pod["hostAliases"] = aliases
    tmpl = {"spec": pod}
    if extra_annotations:
        tmpl["metadata"] = {"annotations": extra_annotations}
    if not pod and not extra_annotations:
        return None
    return {"spec": {"template": tmpl}}


def _apply_image(spec, cfg):
    # kcp-operator defaults to its version-matched upstream image when
    # spec.image is unset. Only inject an override when the caller pins one.
    img = {}
    if cfg["image_repo"]:
        img["repository"] = cfg["image_repo"]
    if cfg["image_tag"]:
        img["tag"] = cfg["image_tag"]
    if img:
        spec["image"] = img


def _apply_auth(spec, cfg):
    auth = {"serviceAccount": {"enabled": True}}
    if cfg["token_auth_secret"]:
        # Static-token auth: the operator mounts the referenced CSV secret and
        # adds --token-auth-file. The secret's contents are the caller's
        # responsibility; we only inject the reference.
        taf = {"secretName": cfg["token_auth_secret"]}
        if cfg["token_auth_key"]:
            taf["key"] = cfg["token_auth_key"]
        auth["tokenAuthFile"] = taf
    oidc = cfg["oidc"]
    if oidc:
        # Mirrors the infra chart's kcp.auth.oidc block so a downstream issuer
        # (dex, keycloak) can front kcp identically to production.
        auth["oidc"] = {
            "enabled": True,
            "issuerURL": oidc["issuerURL"],
            "clientID": oidc["clientID"],
            "groupsClaim": oidc.get("groupsClaim", "groups"),
            "usernameClaim": oidc.get("usernameClaim", "sub"),
        }
        if oidc.get("caFileRef"):
            auth["oidc"]["caFileRef"] = oidc["caFileRef"]
    spec["auth"] = auth


def _apply_authorization(spec, cfg):
    secret = cfg["authorization_webhook_secret"]
    if not secret:
        return
    # Mirrors infra chart kcp.webhook wiring — required by downstream ReBAC
    # (rebac-authz-webhook). The operator mounts the referenced secret and adds
    # --authorization-webhook-config-file to the shard.
    spec["authorization"] = {
        "webhook": {
            "configSecretName": secret,
            "version": cfg["authorization_webhook_version"],
        },
    }


def _extra_args(cfg, etcd_prefix):
    args = []
    if cfg["feature_gates"]:
        args.append("--feature-gates=" + cfg["feature_gates"])
    args.append("--etcd-prefix=" + etcd_prefix)
    return args + cfg["extra_args"]


def _shard_common(spec, cfg, shard_name):
    _apply_auth(spec, cfg)
    _apply_authorization(spec, cfg)
    _apply_image(spec, cfg)
    spec["external"] = {"hostname": cfg["base_domain"], "port": cfg["port"]}
    spec["certificates"] = {"issuerRef": cfg["issuer_ref"]}
    spec["cache"] = {"embedded": {"enabled": True}}
    spec["etcd"] = {"endpoints": [cfg["etcd_endpoint"]]}
    dt = _deployment_template(cfg)
    if dt:
        spec["deploymentTemplate"] = dt
    spec["certificateTemplates"] = {
        "server": {"spec": {"dnsNames": [cfg["shard_hostnames"][shard_name]]}},
    }


def _root_shard(cfg):
    spec = {}
    _shard_common(spec, cfg, "root")
    spec["extraArgs"] = _extra_args(cfg, "/shard/root")
    spec["shardBaseURL"] = "https://{}:{}".format(cfg["shard_hostnames"]["root"], cfg["port"])
    # The front proxy runs alongside the root shard and needs the same name
    # resolution.
    proxy = {}
    pdt = _deployment_template(cfg)
    if pdt:
        proxy["deploymentTemplate"] = pdt
    if cfg["image_repo"] or cfg["image_tag"]:
        pimg = {}
        if cfg["image_repo"]:
            pimg["repository"] = cfg["image_repo"]
        if cfg["image_tag"]:
            pimg["tag"] = cfg["image_tag"]
        proxy["image"] = pimg
    if proxy:
        spec["proxy"] = proxy
    return {
        "apiVersion": "operator.kcp.io/v1alpha1",
        "kind": "RootShard",
        "metadata": {"name": "root", "namespace": cfg["namespace"]},
        "spec": spec,
    }


def _shard(name, cfg):
    spec = {"rootShard": {"ref": {"name": "root"}}}
    _shard_common(spec, cfg, name)
    spec["extraArgs"] = _extra_args(cfg, "/shard/" + name)
    spec["shardBaseURL"] = "https://{}:{}".format(cfg["shard_hostnames"][name], cfg["port"])
    return {
        "apiVersion": "operator.kcp.io/v1alpha1",
        "kind": "Shard",
        "metadata": {"name": name, "namespace": cfg["namespace"]},
        "spec": spec,
    }


def _front_proxy(cfg):
    spec = {
        "rootShard": {"ref": {"name": "root"}},
        "external": {"hostname": cfg["base_domain"], "port": cfg["port"]},
        "certificateTemplates": {
            "server": {"spec": {"dnsNames": [cfg["base_domain"]]}},
        },
    }
    _apply_auth(spec, cfg)
    _apply_image(spec, cfg)
    dt = _deployment_template(cfg)
    if dt:
        spec["deploymentTemplate"] = dt
    return {
        "apiVersion": "operator.kcp.io/v1alpha1",
        "kind": "FrontProxy",
        "metadata": {"name": "frontproxy", "namespace": cfg["namespace"]},
        "spec": spec,
    }


def _tls_route(name, hostname, service, port, cfg):
    gw = cfg["gateway"]
    parent = {"name": gw["name"]}
    if gw.get("namespace"):
        parent["namespace"] = gw["namespace"]
    if gw.get("group"):
        parent["group"] = gw["group"]
    if gw.get("kind"):
        parent["kind"] = gw["kind"]
    if gw.get("section"):
        parent["sectionName"] = gw["section"]
    return {
        "apiVersion": "gateway.networking.k8s.io/" + cfg["route_version"],
        "kind": "TLSRoute",
        "metadata": {"name": name, "namespace": cfg["namespace"]},
        "spec": {
            "parentRefs": [parent],
            "hostnames": [hostname],
            "rules": [{
                "backendRefs": [{
                    "name": service,
                    "port": port,
                    "namespace": cfg["namespace"],
                }],
            }],
        },
    }


def _kubeconfig(name, target):
    groups = ["system:masters"]
    if name == "root":
        groups.append("system:kcp:admin")
    return {
        "apiVersion": "operator.kcp.io/v1alpha1",
        "kind": "Kubeconfig",
        "metadata": {"name": name},
        "spec": {
            "username": "kcp-admin",
            "groups": groups,
            "validity": "8766h",
            "secretRef": {"name": "kcp-{}-kubeconfig".format(name)},
            "target": target,
        },
    }


# ---------------------------------------------------------------------------
# config defaults
# ---------------------------------------------------------------------------

def _defaults(base_domain, extra_shards):
    all_shards = ["root"] + extra_shards
    hostnames = [base_domain] + ["{}.{}".format(s, base_domain) for s in all_shards]
    shard_hostnames = {}
    for s in all_shards:
        shard_hostnames[s] = "{}.{}".format(s, base_domain)
    return {
        "base_domain": base_domain,
        "port": 8443,
        "namespace": "default",
        "extra_shards": extra_shards,
        "hostnames": hostnames,
        "shard_hostnames": shard_hostnames,
        "hostalias_ip": "10.96.2.2",
        "feature_gates": "CacheAPIs=true,WorkspaceMounts=true",
        "extra_args": [],
        "etcd_endpoint": "http://etcd.kcp-etcd.svc.cluster.local:2379",
        "issuer_ref": {"group": "cert-manager.io", "kind": "Issuer", "name": "selfsigned"},
        "image_repo": "",
        "image_tag": "",
        "oidc": None,
        "token_auth_secret": None,
        "token_auth_key": None,
        "authorization_webhook_secret": None,
        "authorization_webhook_version": "v1",
        # Envoy Gateway defaults (kcp's own static install). Downstream callers
        # override these to attach to their own gateway (name/namespace/section)
        # and route version (production uses v1alpha2 with group/kind set).
        "gateway": {
            "name": "eg",
            "namespace": "envoy-gateway-system",
            "section": None,
            "group": None,
            "kind": None,
        },
        "route_version": "v1alpha3",
        "labels": ["kcp"],
        "kubeconfig_dir": "../..",
    }


def _merge(defaults, overrides):
    cfg = dict(defaults)
    for k, v in overrides.items():
        if k not in defaults:
            fail("deploy_kcp: unknown option '{}'".format(k))
        # Shallow-merge the gateway dict so callers can override just the name.
        if k == "gateway" and v != None:
            g = dict(defaults["gateway"])
            for gk, gv in v.items():
                g[gk] = gv
            cfg[k] = g
        else:
            cfg[k] = v
    return cfg


# ---------------------------------------------------------------------------
# public entrypoint
# ---------------------------------------------------------------------------

def deploy_kcp(base_domain = "kcp.localhost", extra_shards = [], **overrides):
    """Generate and apply the static kcp custom resources.

    Args:
      base_domain: DNS suffix for shard hostnames (root.<base_domain>, ...).
      extra_shards: additional Shard names beyond the root shard (e.g.
        ["theseus"]). Empty for a single-shard install.
      **overrides: any key from _defaults(): gateway, route_version,
        authorization_webhook_secret, oidc, image_repo, image_tag, namespace,
        port, etcd_endpoint, issuer_ref, hostalias_ip, feature_gates,
        extra_args, labels, kubeconfig_dir.

    The caller is responsible for cert-manager, the Gateway API implementation,
    etcd and the kcp-operator being present before these resources reconcile.
    """
    cfg = _merge(_defaults(base_domain, extra_shards), overrides)

    docs = [_root_shard(cfg)]
    for name in cfg["extra_shards"]:
        docs.append(_shard(name, cfg))
    docs.append(_front_proxy(cfg))

    # TLSRoutes: front proxy on the base domain, each shard on its own host.
    docs.append(_tls_route("front-proxy", cfg["base_domain"], "frontproxy-front-proxy", 8443, cfg))
    docs.append(_tls_route("root", cfg["shard_hostnames"]["root"], "root-kcp", 6443, cfg))
    for name in cfg["extra_shards"]:
        docs.append(_tls_route(name, cfg["shard_hostnames"][name], "{}-shard-kcp".format(name), 6443, cfg))

    k8s_yaml(encode_yaml_stream(docs))

    # Group each CR with its same-named TLSRoute under one Tilt resource. We do
    # not register the shard/proxy kinds as image workloads (no image_object),
    # so Tilt names the resource after the CR ("root") rather than
    # "root:rootshard".
    k8s_resource("root", labels=cfg["labels"])
    k8s_resource("frontproxy", labels=cfg["labels"])
    for name in cfg["extra_shards"]:
        k8s_resource(name, labels=cfg["labels"])

    # Admin kubeconfigs.
    kcfgs = [
        _kubeconfig("frontproxy", {"frontProxyRef": {"name": "frontproxy"}}),
        _kubeconfig("root", {"rootShardRef": {"name": "root"}}),
    ]
    for name in cfg["extra_shards"]:
        kcfgs.append(_kubeconfig(name, {"shardRef": {"name": name}}))
    k8s_kind("Kubeconfig")
    k8s_yaml(encode_yaml_stream(kcfgs))

    _extract_kubeconfigs(cfg)


def _extract_kubeconfigs(cfg):
    # Inlined so the module has no sibling-file dependency (see header). Waits
    # for the operator API, then dumps each admin kubeconfig secret to
    # <kubeconfig_dir>/tilt-<name>.kubeconfig.
    names = ["frontproxy", "root"] + cfg["extra_shards"]
    script = '\n'.join([
        'set -eo pipefail',
        'DIR="${KCP_KUBECONFIG_DIR:-' + cfg["kubeconfig_dir"] + '}"',
        'mkdir -p "$DIR"',
        'until [ "$(kubectl api-resources --api-group operator.kcp.io 2>/dev/null | wc -l)" -ge 2 ]; do',
        '  echo "waiting for kcp-operator API..."; sleep 1;',
        'done',
        'for n in ' + ' '.join(names) + '; do',
        '  kubectl wait "kubeconfig/$n" --for=create --timeout=5m',
        '  kubectl wait "kubeconfig/$n" --for=condition=Available --timeout=5m',
        '  kubectl wait "secret/kcp-$n-kubeconfig" --for=create --timeout=5m',
        '  kubectl get "secret/kcp-$n-kubeconfig" -o jsonpath="{.data.kubeconfig}" | base64 -d > "$DIR/tilt-$n.kubeconfig"',
        'done',
    ])
    local_resource(
        "kcp-admin extract",
        cmd = script,
        deps = [],
        resource_deps = ["root", "frontproxy"] + cfg["extra_shards"],
        labels = cfg["labels"],
        allow_parallel = True,
    )
