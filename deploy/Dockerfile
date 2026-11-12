# 多阶段构建：编译阶段用 Go 官方镜像，运行阶段用精简 alpine，减小体积且不泄露源码
FROM golang:1.26-alpine AS build
WORKDIR /src
# go.mod 要求 go >= 1.26（依赖版本决定的），构建镜像的 Go 不能更旧
# 容器内默认走 goproxy.cn；网络环境不同可用 --build-arg GOPROXY=... 覆盖
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=$GOPROXY
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN adduser -D -H app
USER app
COPY --from=build /out/server /server
EXPOSE 8080
ENTRYPOINT ["/server"]
