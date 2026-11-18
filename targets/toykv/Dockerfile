# Build from the repo root: docker build -t kuru/toykv -f targets/toykv/Dockerfile .
# golang:1.26 (not 1.22): go.mod requires Go 1.26, see docs/SETUP_NOTES.md.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY targets/toykv/ targets/toykv/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /toykv ./targets/toykv

FROM gcr.io/distroless/static-debian12
COPY --from=build /toykv /toykv
ENV PEER_LISTEN=:7000 CLIENT_LISTEN=:8080 DATA_DIR=/data
EXPOSE 7000 8080
VOLUME /data
ENTRYPOINT ["/toykv"]
