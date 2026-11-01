# syntax=docker/dockerfile:1

# ---------- 前端构建：产物输出到 /src/web，供 Go 内嵌 ----------
FROM node:22-alpine AS web
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# ---------- 后端构建：面板静态二进制 ----------
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/linode-panel .

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
