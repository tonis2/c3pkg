FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /c3pkg ./cmd/c3pkg
# The data directory must be writable by the non-root user the image runs as.
RUN mkdir -p /data/uploads && chown -R 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /c3pkg /c3pkg
COPY --from=build --chown=65532:65532 /data /data
ENV C3PKG_ADDR=:8080 C3PKG_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER 65532:65532
# The image has no shell or curl, so the binary checks itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/c3pkg", "healthcheck"]
ENTRYPOINT ["/c3pkg"]
