package app

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// probeSelf requests http://127.0.0.1:<port><path> and returns the exit code:
// 0 for HTTP 200, 1 otherwise.
func probeSelf(httpAddr, path string) int {
	url, err := selfURL(httpAddr, path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: %s returned %d\n", url, resp.StatusCode)
		return 1
	}
	return 0
}

// selfURL turns HTTP_ADDR (":8082", "0.0.0.0:8082", "[::]:8082",
// "127.0.0.1:8082") into a loopback URL.
func selfURL(httpAddr, path string) (string, error) {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return "", fmt.Errorf("HTTP_ADDR %q: %w", httpAddr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + path, nil
}
