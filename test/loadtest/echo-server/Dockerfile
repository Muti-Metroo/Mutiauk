# Echo server with iperf3
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY main.go .
RUN CGO_ENABLED=0 go build -o /echo-server main.go

FROM alpine:3.19

# Install iperf3
RUN apk add --no-cache iperf3

COPY --from=builder /echo-server /usr/local/bin/echo-server
COPY start.sh /start.sh
RUN chmod +x /start.sh

EXPOSE 5000/tcp 5001/udp 5201/tcp 5201/udp

CMD ["/start.sh"]
