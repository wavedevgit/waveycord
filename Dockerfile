# Build stage
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -tags nosqlite -trimpath -ldflags="-s -w" -o /out/waveycord ./cmd/waveycord \
 && CGO_ENABLED=0 go build -tags nosqlite -trimpath -ldflags="-s -w" -o /out/waveycord-lb ./cmd/waveycord-lb

# The image is Postgres-only (static); SQLite needs cgo and is built locally.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/waveycord /usr/local/bin/waveycord
COPY --from=build /out/waveycord-lb /usr/local/bin/waveycord-lb
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/waveycord"]
