# syntax=docker/dockerfile:1

# ---------- 前端构建：纯静态产物，与目标架构无关 ----------
# 固定在 BUILDPLATFORM 本地执行，避免 QEMU 模拟 npm。
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY frontend/ ./
RUN npm run build

# ---------- 后端构建：Go 跨编译到 TARGETARCH ----------
# 同样跑在 BUILDPLATFORM 上，用 GOARCH=$TARGETARCH 交叉编译，
# arm64 不再需要 QEMU 模拟 go build。
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web ./web
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w" -o /out/linode-panel .

# ---------- 面板镜像 ----------
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 app \
 && mkdir -p /data \
 && chown app:app /data
WORKDIR /app
COPY --from=build /out/linode-panel /app/linode-panel
USER app
VOLUME ["/data"]
EXPOSE 8787
ENTRYPOINT ["/app/linode-panel"]
CMD ["--addr", "0.0.0.0:8787", "--state", "/data/state.db"]
