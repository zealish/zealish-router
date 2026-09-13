FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Cross-compiling from the build platform keeps the image build fast under
# buildx; the binary is static, so no C toolchain is needed per target.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/zealish-router ./cmd/server

# Distroless has no shell, so the writable data directory is prepared here and
# copied in with the right ownership.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/zealish-router /app/zealish-router
COPY --from=build --chown=nonroot:nonroot /out/data /app/data
COPY config.yaml /app/config.yaml

EXPOSE 8787
USER nonroot:nonroot
VOLUME ["/app/data"]

ENTRYPOINT ["/app/zealish-router"]
CMD ["-config", "/app/config.yaml", "serve"]
