# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
# The version comes from internal/buildinfo/VERSION, which is part of the source.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/privo ./cmd/privo
# The data dir must exist and belong to nonroot, or a fresh volume is root-owned.
RUN mkdir -p /out/data

# Static, non-root, no shell. The stdio runner is a separate image (ADR 0007).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/privo /privo
COPY --from=build --chown=65532:65532 /out/data /data
ENV PRIVO_LISTEN=:8080 PRIVO_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=3 CMD ["/privo", "healthcheck"]
ENTRYPOINT ["/privo"]
CMD ["serve"]
