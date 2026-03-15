# GRPC generation
FROM golang:1.25.7 AS proto_base

WORKDIR /src

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
RUN go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
RUN go install github.com/bufbuild/buf/cmd/buf@v1.13.1

COPY daemon/proto .
COPY buf.gen.yaml .
COPY buf.yaml .

RUN buf lint && buf generate -v

#---------------------------------------------- Stage 2: Build Base : Build mesnet binraries (Plugin & daemon) - ---------------------------------
# Building the binaries
FROM golang:1.25.7 AS build_base

WORKDIR /go/src/github.com/networkop/meshnet-cni
ENV CGO_ENABLED=0
COPY go.mod .
COPY go.sum .
RUN go mod download && \
    apt-get update -y && \
    apt-get install -y --no-install-recommends libpcap-dev libsystemd-dev && \
    rm -rf /var/lib/apt/lists/*

FROM --platform=${BUILDPLATFORM:-linux/amd64} build_base AS build

ARG LDFLAGS
ARG TARGETOS
ARG TARGETARCH

COPY daemon/ daemon/
COPY api/ api/
COPY plugin/ plugin/
COPY utils/ utils/
COPY --from=proto_base /src/ .


RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -o meshnet plugin/meshnet.go plugin/grpcwires-plugin.go 
RUN GOOS=${TARGETOS} CGO_ENABLED=1 GOARCH=${TARGETARCH} go build -ldflags="-s -w" -o meshnetd daemon/main.go; 


#----------------------------------------------------- Final Container ---------------------------------
FROM ubuntu:20.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends jq libpcap-dev && \
    rm -rf /var/lib/apt/lists/*

RUN groupadd -r meshnet && useradd -r -g meshnet meshnet

COPY --from=build /go/src/github.com/networkop/meshnet-cni/meshnet /
COPY --from=build /go/src/github.com/networkop/meshnet-cni/meshnetd /
COPY docker/new-entrypoint.sh /entrypoint.sh
COPY LICENSE /
RUN chmod +x ./entrypoint.sh && \
    chmod +x /meshnetd

HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD pgrep meshnetd || exit 1

ENTRYPOINT ["./entrypoint.sh"]
