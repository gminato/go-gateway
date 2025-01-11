package main

import (
	"io"
	"log"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

// proxyRequest handles proxying HTTP requests to a specified service URL.
//
// The function takes a Gin context and target service URL as parameters and:
// - Parses the target URL and appends any additional path parameters
// - Creates a new HTTP request copying the original method, body and headers
// - Sends the request to the target service
// - Copies the response headers and status code back to the original request
// - Streams the response body back to the client
//
// Parameters:
//   - c *gin.Context: The Gin context containing the original HTTP request
//   - serviceURL string: The base URL of the target service to proxy to
//
// The function handles several error cases:
// - Invalid target URL parsing
// - Request creation errors
// - Request sending errors
// - Response copying errors
//
// In case of errors, it returns appropriate HTTP 500 status codes with error messages.
func proxyRequest(c *gin.Context, serviceURL string) {

	proxyUrl, error := url.Parse(serviceURL)
	log.Print("Proxy URL: ", proxyUrl.String()+c.Param("rest"))

	if error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Invalid target URL"})
		return
	}

	req, err := http.NewRequest(c.Request.Method, proxyUrl.String()+c.Param("rest"), c.Request.Body)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error creating request"})
		return
	}

	req.Header = c.Request.Header

	client := &http.Client{}

	resp, err := client.Do(req)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error sending request"})
		return
	}

	// TODO - if header has multiple values, then we need to send all values
	for k, v := range resp.Header {
		c.Header(k, v[0])
	}

	c.Status(resp.StatusCode)

	j, err := io.Copy(c.Writer, resp.Body)

	log.Print("Copied: ", j)

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Error copying response"})
		return
	}

	defer resp.Body.Close()

}

func main() {
	var r *gin.Engine = gin.Default()

	var services = map[string]string{
		"/account": "http://localhost:8082",
		"/loans":   "http://localhost:8081",
	}

	for prefix, targetUrl := range services {
		r.GET(prefix+"/*rest", func(c *gin.Context) {
			log.Print("Request URL: ", c.Param("rest"))
			proxyRequest(c, targetUrl)
		})

		r.POST(prefix+"/*rest", func(c *gin.Context) {
			proxyRequest(c, targetUrl)
		})

		r.PUT(prefix+"/*rest", func(c *gin.Context) {
			proxyRequest(c, targetUrl)
		})

		r.DELETE(prefix+"/*rest", func(c *gin.Context) {
			proxyRequest(c, targetUrl)
		})

		r.PATCH(prefix+"/*rest", func(c *gin.Context) {
			proxyRequest(c, targetUrl)
		})
	}

	r.Run(":8080")
}
