# The online compatibility check (obx-conformance check-server) for the
# Cloudflare Container behind the obx-checker Worker. Built by wrangler from
# deploy/checker with the repository root as build context.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS=linux TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" -o /out/obx-conformance ./cmd/obx-conformance

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 obx
COPY --from=build /out/obx-conformance /usr/local/bin/
USER obx
EXPOSE 8080
# One check per instance (the smallest instance type); the Worker caps the
# number of instances. Budget per check: 800 requests, 32 MiB, 100 ms pacing,
# 3 minutes.
ENTRYPOINT ["obx-conformance", "check-server", "--addr", ":8080", "--max-concurrent", "1", "--max-requests", "800", "--max-mib", "32", "--min-interval", "100ms", "--total-timeout", "3m"]
