# Build the demo from the repo root so the whole module is in context.
# Usage (from demo/local/): handled by docker-compose.yml (context: ../..).
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /demo ./demo/local

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /demo /demo
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/demo"]
