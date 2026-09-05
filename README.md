# tiny-vless-ws

A small, zero-external-dependency VLESS-over-WebSocket server written in Go. It supports TCP forwarding and VLESS UDP packet framing, and is designed for simple container deployments behind a TLS-terminating reverse proxy.

> This server does not provide TLS by itself. Use a trusted reverse proxy, load balancer, or tunnel when exposing it to the internet.

## Highlights

- Standard-library-only Go implementation
- VLESS TCP and UDP forwarding over binary WebSocket frames
- Xray-compatible WebSocket early data for lower connection startup latency
- Fast IPv4/IPv6 destination fallback for better behavior on imperfect dual-stack networks
- Strict WebSocket handshake and frame validation
- Bounded WebSocket message allocation to reduce denial-of-service risk
- Graceful shutdown and startup/configuration validation
- Tiny embedded landing page at `/`
- `/healthz` liveness endpoint
- Minimal scratch-based container running as a non-root user
- Unit tests, race detection, vetting, formatting checks, and container CI

## Configuration

| Variable | Required | Default | Description |
|---|---:|---|---|
| `UUID` | Yes | — | VLESS client UUID. The all-zero UUID is rejected. |
| `PORT` | No | `8080` | Listening port, from `1` to `65535`. |
| `WS_PATH` | No | `/assets/js/main.js` | Exact WebSocket endpoint path. Normal HTTP requests to the default path receive the landing-page JavaScript. |
| `MAX_WS_MESSAGE_BYTES` | No | `4194304` | Maximum accepted WebSocket frame/message size. Allowed range: 1 KiB to 64 MiB. This is a safety limit, not a performance buffer. |

## Run with Docker

```bash
docker run --rm \
  --name tiny-vless-ws \
  -p 8080:8080 \
  -e UUID="00112233-4455-6677-8899-aabbccddeeff" \
  -e WS_PATH="/assets/js/main.js" \
  ghcr.io/ntun7729/tiny-vless-ws:latest
```

Check liveness:

```bash
curl --fail http://127.0.0.1:8080/healthz
```

## Web endpoints

- `/` serves a tiny status page.
- `/assets/js/main.js` serves its JavaScript during a normal `GET` or `HEAD` request.
- A WebSocket upgrade on the configured `WS_PATH` is routed to VLESS instead of static content.
- Query parameters do not change WebSocket path matching, so clients may append `?ed=2560` without changing `WS_PATH` on the server.
- `/healthz` returns `ok` for liveness checks.

## Client settings

Use standard VLESS-over-WebSocket settings:

- Protocol: `VLESS`
- Address: your public hostname
- Port: normally `443` when TLS is terminated upstream
- UUID: the same value supplied to the server
- Transport: `ws`
- WebSocket path: `/assets/js/main.js?ed=2560` when using the default server path
- TLS: enabled at the reverse proxy or tunnel

For Xray/v2rayNG-compatible clients, appending `?ed=2560` enables WebSocket early data. The first VLESS packet can then be carried in the `Sec-WebSocket-Protocol` header during the HTTP upgrade instead of waiting for a separate WebSocket data frame after the `101 Switching Protocols` response. This can remove one network round trip from new proxied connections when the first packet fits within the configured early-data threshold.

Keep the server-side `WS_PATH` as the plain path without `?ed=2560`. The `ed` value is a client-side transport option.

The reverse proxy must preserve WebSocket upgrade headers and must not strip `Sec-WebSocket-Protocol` if early data is used.

### Nginx routing example

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
```

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

- The WebSocket path is matched by URL path; query parameters such as `?ed=2560` are allowed on client requests.
- Client WebSocket frames must be masked, as required by RFC 6455.
- Oversized or malformed frames are rejected before large allocations occur.
- `MAX_WS_MESSAGE_BYTES` should normally be left at its default. Increasing it does not make the relay wait for larger chunks and is not expected to reduce latency.
- Long-lived proxied connections are intentionally allowed; place connection and rate limits at the edge when operating a public service.
- Do not reuse the example UUID.
