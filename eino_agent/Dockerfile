FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /build

COPY go.mod go.sum ./
ENV GOPROXY=https://goproxy.cn,direct
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /eino_agent .

FROM alpine:3.19
RUN apk add --no-cache ca-certificates curl
COPY --from=builder /eino_agent /eino_agent
EXPOSE 8005
ENTRYPOINT ["/eino_agent"]
