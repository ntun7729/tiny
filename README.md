# tiny-vless-ws

A small, zero-external-dependency VLESS relay written in Go. One HTTP listener can serve both the existing WebSocket transport and Pulse, a lightweight server-side transport compatible with the normal v2rayNG/Xray XHTTP client modes.

> This server does not provide TLS by itself. Use a trusted reverse proxy, load balancer, CDN tunnel, or other TLS terminator when exposing it to the internet.

## Highlights

- One port for WebSocket and Pulse
- Standard-library-only Go implementation
- VLESS TCP and UDP forwarding
- Xray-compatible WebSocket early data for lower connection startup latency
- Pulse compatibility with v2rayNG/Xray `auto`, `packet-up`, `stream-up`, and `stream-one`
- Packet reordering for concurrent `packet-up` requests
- Streaming responses with anti-buffering headers and Go HTTP full-duplex support
- Fast IPv4/IPv6 destination fallback for imperfect dual-stack networks
- Strict WebSocket handshake and frame validation
- Bounded WebSocket and Pulse allocations
- Graceful shutdown and session cleanup
- Tiny embedded landing page at `/`
- `/healthz` liveness endpoint
- Minimal scratch-based container running as a non-root user
- Unit tests, race detection, vetting, formatting checks, and container CI

## Configuration

| Variable | Required | Default | Description |
|---|---:|---|---|
| `UUID` | Yes | — | VLESS client UUID. The all-zero UUID is rejected. |
| `PORT` | No | `8080` | Single listener used by both transports. |
| `WS_PATH` | No | `/assets/js/main.js` | Exact WebSocket endpoint path. |
| `PULSE_PATH` | No | `/assets/api/v1` | Base path used by Pulse. Keep it different from `WS_PATH`. |
| `MAX_WS_MESSAGE_BYTES` | No | `4194304` | Maximum accepted WebSocket frame/message size. Safety limit, not a performance buffer. |
| `PULSE_MAX_PACKET_BYTES` | No | `1000000` | Maximum `packet-up` request payload. The default matches Xray's normal `scMaxEachPostBytes`. |

## Run with Docker

```bash
docker run --rm \
  --name tiny-vless-ws \
  -p 8080:8080 \
  -e UUID="00112233-4455-6677-8899-aabbccddeeff" \
  -e WS_PATH="/assets/js/main.js" \
  -e PULSE_PATH="/assets/api/v1" \
  ghcr.io/ntun7729/tiny-vless-ws:latest
```

Check liveness:

```bash
curl --fail http://127.0.0.1:8080/healthz
```

## WebSocket client

Use standard VLESS-over-WebSocket settings:

- Protocol: `VLESS`
- Address: your public hostname
- Port: normally `443` when TLS is terminated upstream
- UUID: the same value supplied to the server
- Transport: `ws`
- WebSocket path: `/assets/js/main.js?ed=2560` with the default server path
- TLS: enabled at the reverse proxy or tunnel

Appending `?ed=2560` enables Xray-compatible WebSocket early data. Keep the server-side `WS_PATH` as the plain path without the query string.

The reverse proxy must preserve WebSocket upgrade headers and must not strip `Sec-WebSocket-Protocol` when early data is used.

## Pulse client in v2rayNG

Pulse is the server-side name used in this repository. In v2rayNG, select the client's **XHTTP** transport because that is the wire format Pulse accepts.

Recommended settings:

- Protocol: `VLESS`
- Address: your public hostname
- Port: normally `443`
- UUID: the same UUID as the server
- Transport: `XHTTP`
- Path: `/assets/api/v1`
- Mode: `auto`
- TLS: enabled at the reverse proxy/CDN/tunnel

`auto` is the recommended starting point. With a normal TLS connection and no REALITY transport, current Xray resolves `auto` to `packet-up`. Pulse also accepts explicit `packet-up`, `stream-up`, and `stream-one`.

### Mode behavior

- `auto`: normally behaves as `packet-up` for this deployment style.
- `packet-up`: one long download GET plus sequenced upload requests. This is the most reverse-proxy/CDN-friendly mode and the recommended default.
- `stream-up`: separate long-lived upload and download requests using the same session ID.
- `stream-one`: one full-duplex HTTP request carries both directions.

Pulse supports the normal v2rayNG defaults: session ID and sequence in the URL path and upload payload in the HTTP body. Custom Xray settings that move session IDs, sequence numbers, or payloads into cookies/headers/query fields are not implemented.

For `packet-up`, keep the client's `scMaxEachPostBytes` at or below `PULSE_MAX_PACKET_BYTES`. Both defaults are `1000000` bytes.

`stream-up` and especially `stream-one` require every reverse proxy between the client and tiny to permit streaming/full-duplex behavior. If a CDN or proxy buffers request/response bodies, use `auto`/`packet-up` instead.

## Shared port routing

Both transports use the same `PORT`. They are distinguished by path and protocol shape:

- WebSocket: exact `WS_PATH` plus an HTTP WebSocket upgrade
- Pulse: `PULSE_PATH/`, `PULSE_PATH/<session>`, and `PULSE_PATH/<session>/<sequence>`

No second backend port is required.

### Nginx example

```nginx
location = / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
}

location = /healthz {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
}

location = /assets/js/main.js {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_set_header Sec-WebSocket-Protocol $http_sec_websocket_protocol;
}

location = /assets/api/v1 {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_request_buffering off;
    proxy_buffering off;
}

location ^~ /assets/api/v1/ {
    proxy_pass http://127.0.0.1:8080;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_request_buffering off;
    proxy_buffering off;
    proxy_read_timeout 1d;
    proxy_send_timeout 1d;
}
```

If your edge proxy forwards all paths to tiny already, separate upstream routing rules are not required; the application dispatches both transports internally on the same listener.

## Web endpoints

- `/` serves a tiny status page.
- `/assets/js/main.js` serves its JavaScript during a normal `GET` or `HEAD` request.
- A WebSocket upgrade on `WS_PATH` is routed to VLESS.
- Pulse requests under `PULSE_PATH` are routed to the HTTP transport.
- `/healthz` returns `ok` for liveness checks.

## Build and test

The module has no third-party Go dependencies.

```bash
gofmt -w .
go vet ./...
go test -race ./...
go build -trimpath -ldflags="-s -w" -o tiny-vless-ws .
```

Build the container locally:

```bash
docker build -t tiny-vless-ws:local .
```

## Operational notes

- WebSocket query parameters such as `?ed=2560` do not change `WS_PATH` matching.
- Client WebSocket frames must be masked as required by RFC 6455.
- Pulse sessions that never establish their download side are reaped after 30 seconds.
- Pulse buffers a bounded number of out-of-order packet uploads per session.
- `MAX_WS_MESSAGE_BYTES` should normally remain at its default; increasing it is not expected to reduce latency.
- Long-lived proxied connections are intentionally allowed. Place appropriate connection/rate limits at the edge for a public service.
- Do not reuse the example UUID.
