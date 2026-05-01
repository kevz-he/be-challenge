// Command healthcheck is a tiny binary used as the Docker HEALTHCHECK for
// the distroless image (which has no shell and no curl/wget). Exits 0 if
// GET ${HEALTHCHECK_URL:-http://127.0.0.1:8080/healthz} returns 200, else 1.
package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	url := os.Getenv("HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:8080/healthz"
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
