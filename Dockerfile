# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.25.3-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/jakecyr/stalemate/internal/buildinfo.Version=$VERSION -X github.com/jakecyr/stalemate/internal/buildinfo.Commit=$COMMIT -X github.com/jakecyr/stalemate/internal/buildinfo.Date=$BUILD_DATE" \
    -o /out/stalemate ./cmd/stalemate

FROM alpine:3.22
RUN apk add --no-cache ca-certificates git su-exec \
    && addgroup -g 10001 stalemate && adduser -D -u 10001 -G stalemate stalemate \
    && mkdir /data && chown stalemate:stalemate /data
COPY --from=build /out/stalemate /usr/local/bin/stalemate
COPY docker/entrypoint.sh /usr/local/bin/stalemate-entrypoint
COPY docker/config.json /etc/stalemate/config.json
RUN chmod 755 /usr/local/bin/stalemate-entrypoint
ENV HOME=/home/stalemate STALEMATE_DATA_DIR=/data
LABEL org.opencontainers.image.source="https://github.com/jakecyr/stalemate" \
      org.opencontainers.image.description="Evidence-backed engineering memory and dependency impact service"
VOLUME /data
EXPOSE 7433
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=3 CMD wget -q -O /dev/null http://127.0.0.1:7433/healthz || exit 1
ENTRYPOINT ["stalemate-entrypoint"]
CMD ["serve"]
