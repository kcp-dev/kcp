FROM golang:1.26.5 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /access-vw ./cmd/access-vw/

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /access-vw /access-vw
USER 65532:65532
ENTRYPOINT ["/access-vw"]
