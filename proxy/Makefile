images:
	KO_DOCKER_REPO=ghcr.io/mjudeikis/kcp-proxy ko build ./cmd/proxy

run-proxy:
    NAMESPACE=default \
	go run ./proxy/cmd/proxy/main.go \
	--from-kubeconfig=child.kubeconfig \
    --proxy-target-name=tilt \
    --proxy-target-uid=b481207f-96e3-4323-b762-08ede4407444 \
    --from-cluster=elzcle28zdhiazmu \
	--to-kubeconfig=tilt.kubeconfig

