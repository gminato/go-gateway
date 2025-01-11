package main

import (
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sony/gobreaker/v2"
)

var CircuitBreakerConfig map[string]*gobreaker.CircuitBreaker[any]

// proxyRequest handles proxying the incoming request to the specified service URL
// and returns the response back to the client. It uses a circuit breaker to manage
// the request execution and handle failures gracefully.
//
// Parameters:
//   - c: The Gin context for the current request.
//   - serviceURL: The URL of the target service to which the request should be proxied.
//   - cb: A circuit breaker instance to manage the request execution.
//
// The function performs the following steps:
//  1. Parses the service URL and logs the full proxy URL.
//  2. Executes the request using the circuit breaker.
//  3. Creates a new HTTP request with timeout of 10s with the same method and body as the original request.
//  4. Copies the headers from the original request to the new request.
//  5. Sends the new request to the target service and receives the response.
//  6. Copies the response headers and body back to the original client response.
//  7. Handles errors at each step and returns appropriate HTTP status codes and messages.
//
// If the target URL is invalid, it returns a 500 Internal Server Error.
// If the circuit breaker is open or the request fails, it returns a 503 Service Unavailable.
func proxyRequest(c *gin.Context, serviceURL string, cb *gobreaker.CircuitBreaker[any]) {

	proxyUrl, err := url.Parse(serviceURL)
	log.Print("Proxy URL: ", proxyUrl.String()+c.Param("rest"))

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		return
	}
	_, err = cb.Execute(func() (interface{}, error) {
		req, err := http.NewRequest(c.Request.Method, proxyUrl.String()+c.Param("rest"), c.Request.Body)

		if err != nil {
			return nil, errors.New("Error creating request")
		}

		req.Header = c.Request.Header

		client := &http.Client{
			Timeout: 10 * time.Second,
		}

		resp, err := client.Do(req)

		if err != nil {
			return nil, errors.New("Error sending request")
		}

		for k, v := range resp.Header {
			c.Header(k, v[0])
		}

		defer resp.Body.Close()

		c.Status(resp.StatusCode)

		j, err := io.Copy(c.Writer, resp.Body)

		log.Print("Copied: ", j)

		if err != nil {
			return nil, errors.New("Error copying response body")
		}

		return nil, nil
	})

	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "Service unavailable",
			"msg":   err.Error(),
		})
		return
	}

}

func main() {
	var r *gin.Engine = gin.Default()

	var services = map[string]string{
		"/account": "http://localhost:8082",
		"/loans":   "http://localhost:8081",
	}

	CircuitBreakerConfig = make(map[string]*gobreaker.CircuitBreaker[interface{}])

	for prefix := range services {
		cbSetting := gobreaker.Settings{
			Name: prefix,
			ReadyToTrip: func(counts gobreaker.Counts) bool {
				return counts.ConsecutiveFailures > 5
			},
			OnStateChange: func(name string, from, to gobreaker.State) {
				log.Printf("Circuit breaker for %s changed state from %s to %s", name, from.String(), to.String())
			},
			MaxRequests: 5,
			Timeout:     5 * time.Second,
		}

		CircuitBreakerConfig[prefix] = gobreaker.NewCircuitBreaker[any](cbSetting)

	}

	for prefix, targetUrl := range services {

		cb := CircuitBreakerConfig[prefix]

		r.Any(prefix+"/*rest", func(c *gin.Context) {
			proxyRequest(c, targetUrl, cb)
		})

	}

	r.Run(":8080")
}
