// Echo server with TCP, UDP, and metrics reporting
package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync/atomic"
	"time"
)

var (
	tcpConnections int64
	tcpBytes       int64
	udpPackets     int64
	udpBytes       int64
)

func main() {
	// Start metrics reporter
	go reportMetrics()

	// Start TCP echo server
	go runTCPServer(":5000")

	// Start UDP echo server
	go runUDPServer(":5001")

	log.Println("Echo server started")
	log.Println("  TCP echo: port 5000")
	log.Println("  UDP echo: port 5001")
	log.Println("  iperf3:   port 5201 (started separately)")

	// Keep main goroutine alive
	select {}
}

func runTCPServer(addr string) {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("TCP listen error: %v", err)
	}
	defer listener.Close()

	log.Printf("TCP server listening on %s", addr)

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("TCP accept error: %v", err)
			continue
		}
		go handleTCPConn(conn)
	}
}

func handleTCPConn(conn net.Conn) {
	defer conn.Close()

	atomic.AddInt64(&tcpConnections, 1)
	start := time.Now()
	remoteAddr := conn.RemoteAddr().String()

	log.Printf("TCP connection from %s", remoteAddr)

	buf := make([]byte, 64*1024)
	var totalBytes int64

	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		n, err := conn.Read(buf)
		if err != nil {
			if err != io.EOF {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout is normal for idle connections
				} else {
					log.Printf("TCP read error from %s: %v", remoteAddr, err)
				}
			}
			break
		}

		if n > 0 {
			totalBytes += int64(n)
			atomic.AddInt64(&tcpBytes, int64(n))

			// Echo back
			_, err = conn.Write(buf[:n])
			if err != nil {
				log.Printf("TCP write error to %s: %v", remoteAddr, err)
				break
			}
		}
	}

	duration := time.Since(start)
	log.Printf("TCP connection from %s closed: %d bytes in %v", remoteAddr, totalBytes, duration)
}

func runUDPServer(addr string) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		log.Fatalf("UDP resolve error: %v", err)
	}

	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		log.Fatalf("UDP listen error: %v", err)
	}
	defer conn.Close()

	log.Printf("UDP server listening on %s", addr)

	buf := make([]byte, 64*1024)

	for {
		n, remoteAddr, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("UDP read error: %v", err)
			continue
		}

		atomic.AddInt64(&udpPackets, 1)
		atomic.AddInt64(&udpBytes, int64(n))

		// Echo back
		_, err = conn.WriteToUDP(buf[:n], remoteAddr)
		if err != nil {
			log.Printf("UDP write error to %s: %v", remoteAddr, err)
		}
	}
}

func reportMetrics() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	var lastTCPBytes, lastUDPBytes int64
	lastTime := time.Now()

	for range ticker.C {
		now := time.Now()
		duration := now.Sub(lastTime).Seconds()

		currentTCPBytes := atomic.LoadInt64(&tcpBytes)
		currentUDPBytes := atomic.LoadInt64(&udpBytes)
		currentTCPConns := atomic.LoadInt64(&tcpConnections)
		currentUDPPackets := atomic.LoadInt64(&udpPackets)

		tcpThroughput := float64(currentTCPBytes-lastTCPBytes) / duration / 1024 / 1024
		udpThroughput := float64(currentUDPBytes-lastUDPBytes) / duration / 1024 / 1024

		fmt.Fprintf(os.Stderr, "\n=== Metrics ===\n")
		fmt.Fprintf(os.Stderr, "TCP: %d connections, %.2f MB/s throughput, %d total bytes\n",
			currentTCPConns, tcpThroughput, currentTCPBytes)
		fmt.Fprintf(os.Stderr, "UDP: %d packets, %.2f MB/s throughput, %d total bytes\n",
			currentUDPPackets, udpThroughput, currentUDPBytes)
		fmt.Fprintf(os.Stderr, "===============\n\n")

		lastTCPBytes = currentTCPBytes
		lastUDPBytes = currentUDPBytes
		lastTime = now
	}
}
