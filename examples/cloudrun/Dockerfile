# examples/cloudrun is its own module; build from the repo root so its replace
# directives (-> ../.. and ../../invalidation/gcppubsub) resolve the core and
# GCP-bus modules from the copied tree:
#   docker build -f examples/cloudrun/Dockerfile -t nimbus-cloudrun .
FROM golang:1.25 AS build
WORKDIR /src
# GOWORK=off so a local, untracked go.work never changes resolution inside the
# image; the module's own go.mod + replaces are authoritative.
ENV GOWORK=off
COPY . .
RUN cd examples/cloudrun && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app"]
