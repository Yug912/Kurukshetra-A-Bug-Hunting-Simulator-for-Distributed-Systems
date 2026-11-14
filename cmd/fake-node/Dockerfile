# Build: docker build -t kuru/fake-node -f cmd/fake-node/Dockerfile .
FROM golang:1.26 AS build
WORKDIR /src
COPY cmd/fake-node/main.go .
RUN go mod init fakenode && CGO_ENABLED=0 go build -o /fake-node .

FROM gcr.io/distroless/static-debian12
COPY --from=build /fake-node /fake-node
ENV PEER_LISTEN=:7000 CLIENT_LISTEN=:8080 DATA_DIR=/data
EXPOSE 7000 8080
ENTRYPOINT ["/fake-node"]
