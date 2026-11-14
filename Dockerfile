FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=
# Embed Agents for both architectures so the image can serve the one-line installer.
RUN VERSION=${VERSION:-$(sh scripts/version.sh)} && \
    for arch in amd64 arm64; do \
      CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags="-s -w -X github.com/leeska/Lodestar/internal/model.Version=$VERSION" -o internal/agentdist/bin/lodestar-agent-linux-$arch ./cmd/agent || exit 1; \
    done \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/leeska/Lodestar/internal/model.Version=$VERSION" -o /lodestar ./cmd/server

FROM alpine:3.23@sha256:075c2c1a4068c1e251228ca2b0ea8b163f835ca5d66e758b93c7fc89b1995616
RUN apk add --no-cache ca-certificates tzdata && addgroup -g 65532 lodestar && adduser -D -H -u 65532 -G lodestar lodestar && mkdir /data && chown lodestar:lodestar /data
COPY --from=build /lodestar /usr/local/bin/lodestar
USER 65532:65532
VOLUME /data
EXPOSE 25774
ENTRYPOINT ["/usr/local/bin/lodestar"]
CMD ["--listen", "0.0.0.0:25774", "--data", "/data"]
