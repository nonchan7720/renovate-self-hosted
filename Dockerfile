# syntax=docker/dockerfile:1

FROM golang:1.26.7-alpine AS build
WORKDIR /src

# The service depends on the standard library only, so go.mod is the whole
# dependency graph.
COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/renovate-webhook ./cmd/renovate-webhook

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/renovate-webhook /usr/local/bin/renovate-webhook
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/renovate-webhook"]
