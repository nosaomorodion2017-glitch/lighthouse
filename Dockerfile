# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lighthouse ./cmd/server

FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=build /out/lighthouse /app/lighthouse
COPY data /app/data
ENV DATA_DIR=/app/data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/lighthouse"]
