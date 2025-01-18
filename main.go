package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sony/gobreaker/v2"
	"golang.org/x/time/rate"
)

// Configuration and setup for CircuitBreaker, Rate Limiter, and services
var CircuitBreakerConfig map[string]*gobreaker.CircuitBreaker[any]
var LokiURL = "http://loki:3100/loki/api/v1/push" // Loki URL

// Function to send log to Loki
func sendLogToLoki(logEntry string, streamLabels map[string]string) {
	// Prepare the log entry for Loki
	logData := map[string]interface{}{
		"streams": []map[string]interface{}{
			{
				"stream": streamLabels,
				"values": []interface{}{
					[]interface{}{fmt.Sprintf("%d", time.Now().UnixNano()), logEntry},
				},
			},
		},
	}

	// Marshal the log entry to JSON
	jsonData, err := json.Marshal(logData)
	if err != nil {
		log.Error().Err(err).Msg("Error marshaling log data to JSON")
		return
	}

	// Send the log to Loki using HTTP POST
	resp, err := http.Post(LokiURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Error().Err(err).Msg("Error sending log to Loki")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Error().Int("status_code", resp.StatusCode).Msg("Failed to push log to Loki")
	}
}

// Middleware for rate-limiting
func RateLimterMiddleware(limiter *rate.Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		log.Print("Limit used: ", limiter.Limit())
		if !limiter.Allow() && c.Request.Method != "POST" {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests"})
			c.Abort()
			return
		}
		c.Next()
	}
}

// Proxy request handler with Circuit Breaker and error handling
func proxyRequest(c *gin.Context, serviceURL string, cb *gobreaker.CircuitBreaker[any]) {
	proxyUrl, err := url.Parse(serviceURL)
	log.Print("Proxy URL: ", proxyUrl.String()+c.Param("rest"))

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		sendLogToLoki("Invalid target URL", map[string]string{"level": "error", "path": c.Request.URL.Path})
		return
	}

	_, err = cb.Execute(func() (interface{}, error) {
		req, err := http.NewRequest(c.Request.Method, proxyUrl.String()+c.Param("rest"), c.Request.Body)
		if err != nil {
			sendLogToLoki("Error creating request", map[string]string{"level": "error", "path": c.Request.URL.Path})
			return nil, errors.New("Error creating request")
		}

		req.Header = c.Request.Header
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)

		if err != nil {
			sendLogToLoki("Error sending request", map[string]string{"level": "error", "path": c.Request.URL.Path})
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
			sendLogToLoki("Error copying response body", map[string]string{"level": "ERROR", "path": c.Request.URL.Path})
			return nil, errors.New("Error copying response body")
		}

		// Log successful proxy
		sendLogToLoki("Proxy request successful", map[string]string{"level": "INFO", "path": c.Request.URL.Path})

		return nil, nil
	})

	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Service unavailable", "msg": err.Error()})
		sendLogToLoki("Service unavailable", map[string]string{"level": "error", "path": c.Request.URL.Path})
		return
	}
}

// Main function to setup Gin server
func main() {
	var r *gin.Engine = gin.Default()

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix

	httpRequests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests made.",
	}, []string{"path", "method"})

	prometheus.MustRegister(httpRequests)

	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

	var services = map[string]string{
		"/account": "http://accounts:8080",
		"/loans":   "http://loans:8080",
	}
	CircuitBreakerConfig = make(map[string]*gobreaker.CircuitBreaker[interface{}])
	var RateLimiterConfig = make(map[string]*rate.Limiter)

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
		RateLimiterConfig[prefix] = rate.NewLimiter(10, 20)
	}

	for prefix, targetUrl := range services {
		cb := CircuitBreakerConfig[prefix]
		limter := RateLimiterConfig[prefix]

		r.Any(prefix+"/*rest", RateLimterMiddleware(limter), func(c *gin.Context) {
			proxyRequest(c, targetUrl, cb)
		})
	}

	r.Run(":8080")
}
