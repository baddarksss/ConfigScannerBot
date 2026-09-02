# ---- build the bot (static Go binary, stdlib only) ----
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/cfgscan-bot ./cmd/bot

# ---- runtime ----
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl unzip \
    && rm -rf /var/lib/apt/lists/*

# pin the xray core version (same line the app updates to)
ARG XRAY_VERSION=26.7.28
RUN curl -fsSL "https://github.com/XTLS/Xray-core/releases/download/v${XRAY_VERSION}/Xray-linux-64.zip" -o /tmp/xray.zip \
    && unzip -qo /tmp/xray.zip -d /tmp/xray \
    && install /tmp/xray/xray /usr/local/bin/xray \
    && rm -rf /tmp/xray /tmp/xray.zip \
    && /usr/local/bin/xray version | head -1

# native Hysteria client — Xray-core's salamander/gecko UDP obfs is broken
# upstream, so every hy2 link is tested through this binary (app parity).
# Same version line the app bundles (hy2 core v2.12.2).
ARG HYSTERIA_VERSION=v2.12.2
RUN curl -fsSL "https://github.com/HyNetworks/hysteria/releases/download/app/${HYSTERIA_VERSION}/hysteria-linux-amd64" \
        -o /usr/local/bin/hysteria \
    && chmod +x /usr/local/bin/hysteria \
    && (/usr/local/bin/hysteria version | grep -i "Version:" || true)

COPY --from=build /out/cfgscan-bot /usr/local/bin/cfgscan-bot

# persistent settings: attach a Railway VOLUME at /data from the dashboard
# (the Dockerfile VOLUME instruction is rejected by Railway builds)
ENV DATA_DIR=/data
ENV XRAY_BIN=/usr/local/bin/xray
ENV HYSTERIA_BIN=/usr/local/bin/hysteria

ENTRYPOINT ["/usr/local/bin/cfgscan-bot"]
