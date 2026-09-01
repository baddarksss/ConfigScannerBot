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

COPY --from=build /out/cfgscan-bot /usr/local/bin/cfgscan-bot

# persistent settings: attach a Railway VOLUME at /data from the dashboard
# (the Dockerfile VOLUME instruction is rejected by Railway builds)
ENV DATA_DIR=/data
ENV XRAY_BIN=/usr/local/bin/xray

ENTRYPOINT ["/usr/local/bin/cfgscan-bot"]
