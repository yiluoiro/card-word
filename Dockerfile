# syntax=docker/dockerfile:1
FROM golang:1.27.3-alpine AS build
WORKDIR /src

# Dependencies first for better layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=v1.0.0
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/yiluoiro/card-word/internal/version.Version=${VERSION} -X github.com/yiluoiro/card-word/internal/version.Commit=${COMMIT}" \
      -o /out/cardword ./cmd/cardword \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/cardword-sign ./cmd/cardword-sign

FROM alpine:3.20
RUN adduser -D -u 10001 -h /data cardword \
 && apk add --no-cache ca-certificates tzdata

COPY --from=build /out/cardword /usr/local/bin/cardword
COPY --from=build /out/cardword-sign /usr/local/bin/cardword-sign

USER 10001
WORKDIR /data
VOLUME ["/data"]
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/cardword"]
CMD ["--addr", ":8080", "--data", "/data"]
