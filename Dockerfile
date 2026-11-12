# Container image with the obx command (ghcr.io/openbrandexchange/obx).
#   docker build -t obx .
#   docker run --rm -e OBX_URL -e OBX_TOKEN -v "$PWD/export:/data" obx push /data
#   docker run -d --restart unless-stopped -e OBX_URL -e OBX_TOKEN -v /data/outbox:/outbox obx watch /outbox
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/obx ./cmd/obx \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o /out/obx-conformance ./cmd/obx-conformance

FROM alpine:3.24
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 obx
COPY --from=build /out/obx /out/obx-conformance /usr/local/bin/
USER obx
ENV OBX_CONFIG_DIR=/home/obx/.obx
WORKDIR /home/obx
ENTRYPOINT ["obx"]
CMD ["help"]
