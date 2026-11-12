# The public read-only demo portal (obx-conformance mock --read-only) with the
# Kestrel test data, behind the obx-demo Worker. In memory: a restart resets
# it. OBX_MOCK_CLIENT (id:secret) and OBX_PUBLIC_URL come from the Worker.
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
ENTRYPOINT ["obx-conformance", "mock", "--read-only", "--addr", ":8080", "--rate-limit", "1200"]
