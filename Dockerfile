FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /xui-remote .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /xui-remote /usr/local/bin/xui-remote
# The staging area and config snapshots live under HOME.
ENV HOME=/state
VOLUME /state
ENTRYPOINT ["/usr/local/bin/xui-remote", "-config", "/etc/xui-remote/config.yaml"]
