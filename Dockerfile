# 默认镜像内置 mock 提供者（开箱可演示全链路）；
# 真实推理请参考 README「ONNX 部署」自行构建带 onnx 标签的镜像。
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fitpose-server ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 fitpose
USER fitpose
COPY --from=build /out/fitpose-server /usr/local/bin/fitpose-server
EXPOSE 8080
ENTRYPOINT ["fitpose-server"]
