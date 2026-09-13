FROM golang:1.25-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags '-s -w' -o /out/zealish-router ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app
COPY --from=build /out/zealish-router /app/zealish-router
COPY config.yaml /app/config.yaml

EXPOSE 8787
USER nonroot:nonroot

ENTRYPOINT ["/app/zealish-router"]
CMD ["-config", "/app/config.yaml", "serve"]
