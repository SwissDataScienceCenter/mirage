# Mirage is a static binary in a distroless image: it runs as an arbitrary UID
# and listens above port 1024, so no capabilities are needed.
FROM golang:1.26 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/

ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags='-s -w' -o /out/mirage ./cmd/mirage

FROM gcr.io/distroless/static:nonroot

# ghcr.io reads image.source to link the published package to this repository —
# without it the package page stands alone, with no commit history or README. In
# the final stage because labels belong to the image being shipped, not the
# builder. CI overwrites these with metadata-action's own values, which are the
# same ones derived from the repository; they are here so a `just image` build
# carries them too.
LABEL org.opencontainers.image.source="https://github.com/SwissDataScienceCenter/mirage"
LABEL org.opencontainers.image.url="https://github.com/SwissDataScienceCenter/mirage"
LABEL org.opencontainers.image.title="mirage"
LABEL org.opencontainers.image.description="Makes a Kubernetes controller believe it has cluster-wide access when it only has access to one namespace."

COPY --from=build /out/mirage /mirage

# Overridden by the Pod's securityContext in the documented patch; set here so
# the image is not root by default wherever it is run.
USER 65532:65532

EXPOSE 8001

ENTRYPOINT ["/mirage"]
