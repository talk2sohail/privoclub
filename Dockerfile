# syntax=docker/dockerfile:1
FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
ARG VERSION=dev COMMIT=none DATE=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X privo.club/privo/internal/buildinfo.Version=${VERSION} -X privo.club/privo/internal/buildinfo.Commit=${COMMIT} -X privo.club/privo/internal/buildinfo.Date=${DATE}" \
    -o /out/privo ./cmd/privo

# Static, non-root, no shell. The stdio runner is a separate image (ADR 0007).
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/privo /privo
ENV PRIVO_LISTEN=:8080 PRIVO_DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=3 CMD ["/privo", "healthcheck"]
ENTRYPOINT ["/privo"]
CMD ["serve"]
