# demo/local is its own module; build from the repo root (docker-compose sets
# context: ../..) so its replace directives (-> ../.. and
# ../../invalidation/gcppubsub) resolve the core and GCP-bus modules.
FROM golang:1.25 AS build
WORKDIR /src
# GOWORK=off so a local, untracked go.work never changes resolution inside the
# image; the module's own go.mod + replaces are authoritative.
ENV GOWORK=off
COPY . .
RUN cd demo/local && CGO_ENABLED=0 GOOS=linux go build -trimpath -o /demo .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /demo /demo
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/demo"]
