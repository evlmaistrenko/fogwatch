# Build on the runner's native arch and cross-compile (pure Go, no cgo),
# so multi-arch images don't need QEMU.
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /bot . && mkdir /data

FROM gcr.io/distroless/static:nonroot
COPY --from=build /bot /bot
COPY --from=build --chown=65532:65532 /data /data
WORKDIR /
VOLUME /data
ENTRYPOINT ["/bot"]
