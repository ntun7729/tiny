# syntax=docker/dockerfile:1.7

FROM golang:1.25-alpine AS build

WORKDIR /src
COPY go.mod ./
COPY main.go ./
COPY server.go ./
COPY vless.go ./
COPY relay.go ./
COPY websocket.go ./
COPY pulse.go ./
COPY web.go ./

RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -buildid=" \
      -o /out/tiny-vless-ws \
      .

FROM scratch

COPY --from=build /out/tiny-vless-ws /tiny-vless-ws

USER 65532:65532
EXPOSE 8080
STOPSIGNAL SIGTERM

ENTRYPOINT ["/tiny-vless-ws"]
