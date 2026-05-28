# Built from the repo root so the whole module is in context:
#   docker build -f examples/cloudrun/Dockerfile -t runcache-cloudrun .
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /app ./examples/cloudrun

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/app"]
