# TILT

Tilt setup for KCP development.
Benefits of using Tilt here is that is can be used to build and deploy the KCP
automatically when code changes are detected. It also provides tools like
Prometheus, Grafana, Loki and port forwarding into local machine for debugging.
It uses helm chart as base and inject locally built images into kind cluster

## Prerequisites

- [Docker](https://docs.docker.com/get-docker/)
- [Tilt](https://docs.tilt.dev/install.html)
- [Kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- [Helm](https://helm.sh/docs/intro/install/)
- [CloudFlare](https://www.cloudflare.com/) account


You will need to precreate cloudflare tunnel with domain you own and point it to
`https://ingress-nginx-controller.ingress-nginx.svc:443` with `noTLSVerify`.

Tunnels are found at: `Zero Trust -> Access -> Tunnels`

Once you have cloudFlare tunnel TOKEN, copy `contrib/tilt/cloud-flare-tunnel.yaml.example`
to `contrib/tilt/cloud-flare-tunnel.yaml` and replace `CF_TOKEN` with your token.


## Usage

To start tilt run:

```bash
./contrib/tilt/kind.sh

# Output example:
....
Install KCP
Tooling:
Grafana: http://localhost:3333/
Prometheus: http://localhost:9091
KCP API Server: https://localhost:9443
KCP FrontProxy Server: https://localhost:9444
Tilt started on http://localhost:10350/
v0.33.6, built 2023-09-29

(space) to open the browser
(s) to stream logs (--stream=true)
(t) to open legacy terminal mode (--legacy=true)
(ctrl-c) to exit
```

Once tilt start press `space` and track the progress. First boot might take
a while as it needs to build all the images, run prometheus, grafana, loki, etc.
