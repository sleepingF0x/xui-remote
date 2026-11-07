# Cross-compile on the build host instead of emulating each target platform:
# the binary is pure Go, so only GOOS/GOARCH differ between images.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" -o /xui-remote .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /xui-remote /usr/local/bin/xui-remote
# The staging area and config snapshots live under HOME.
ENV HOME=/state
VOLUME /state
ENTRYPOINT ["/usr/local/bin/xui-remote", "-config", "/etc/xui-remote/config.yaml"]
