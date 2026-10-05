# Static atrt binary on distroless (CA certificates, non-root user 65532). The Athens time
# zone is embedded (timetzdata), so the image needs no tzdata.
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags timetzdata \
    -ldflags="-s -w" -o /out/atrt ./cmd/atrt && mkdir -p /out/state

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/atrt /usr/local/bin/atrt
COPY --from=build --chown=65532:65532 /out/state /var/lib/atrt
VOLUME /var/lib/atrt
ENV GOMEMLIMIT=100MiB
ENTRYPOINT ["/usr/local/bin/atrt"]
CMD ["serve", "--listen", "127.0.0.1:8095", "--metrics-listen", "127.0.0.1:8096", "--state", "/var/lib/atrt"]
