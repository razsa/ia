package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/labstack/echo/v4"
	_ "github.com/lib/pq" // Import the postgres driver
)

type Template struct {
	templates *template.Template
}

func (t *Template) Render(w io.Writer, name string, data interface{}, c echo.Context) error {
	return t.templates.ExecuteTemplate(w, name, data)
}

func main() {
	// Database Connection
	db, err := sql.Open("postgres", "postgresql://root:secret@localhost/crawler?sslmode=disable")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Elasticsearch client initialization moved to main
	esClient, err := connectElasticsearch()
	if err != nil {
		log.Fatalf("Failed to connect to Elasticsearch: %v", err)
	}

	e := echo.New()
	setupAPI(e, db, esClient)
	e.Logger.Fatal(e.Start(":1323"))
}

func setupAPI(e *echo.Echo, db *sql.DB, es *elasticsearch.Client) {
	// Initialize template renderer
	t := &Template{
		templates: template.Must(template.New("").Funcs(template.FuncMap{
			"truncate": func(s string, length int) string {
				if len(s) > length {
					return s[:length] + "..."
				}
				return s
			},
		}).ParseGlob("views/*.html")),
	}
	e.Renderer = t

	// Define routes
	e.GET("/", func(c echo.Context) error {
		return c.Render(http.StatusOK, "index.html", nil)
	})

	e.GET("/search", func(c echo.Context) error {
		query := c.QueryParam("q")
		if query == "" {
			return c.Render(http.StatusOK, "search_results.html", nil)
		}

		var b esapi.SearchRequest
		b = esapi.SearchRequest{
			Index: []string{"pages"},
			Body:  strings.NewReader(fmt.Sprintf(`{"query":{"multi_match":{"query":"%s","fields":["title","content"]}}}`, query)),
		}
		result, err := b.Do(context.Background(), es)
		if err != nil {
			log.Printf("Error performing search: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		defer result.Body.Close()

		if result.IsError() {
			var e map[string]interface{}
			if err := json.NewDecoder(result.Body).Decode(&e); err != nil {
				log.Printf("Error parsing error response: %v", err)
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Error parsing Elasticsearch response"})
			}
			log.Printf("Elasticsearch error: %v", e)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("%v", e["error"])})
		}

		var r map[string]interface{}
		if err := json.NewDecoder(result.Body).Decode(&r); err != nil {
			log.Printf("Error parsing the response body: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to parse response"})
		}

		hitsObj, exists := r["hits"].(map[string]interface{})
		if !exists {
			log.Printf("No hits object in response: %v", r)
			return c.Render(http.StatusOK, "search_results.html", []interface{}{})
		}

		hits, exists := hitsObj["hits"].([]interface{})
		if !exists {
			log.Printf("No hits array in response: %v", hitsObj)
			return c.Render(http.StatusOK, "search_results.html", []interface{}{})
		}

		return c.Render(http.StatusOK, "search_results.html", hits)
	})

	e.POST("/start-crawler", func(c echo.Context) error {
		// Ensure index exists
		if err := ensureIndexExists(es); err != nil {
			log.Printf("Failed to create index: %v", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to create index"})
		}

		// Start the crawler with a default URL (you can make this configurable via query parameter)
		go func() {
			startURL := "https://example.com" // Replace with your desired start URL
			if err := Crawl(db, es, startURL); err != nil {
				log.Printf("Crawler error: %v", err)
			}
		}()

		return c.JSON(http.StatusOK, map[string]string{"message": "Crawler started"})
	})
}

func ensureIndexExists(es *elasticsearch.Client) error {
	// Create the index if it doesn't exist
	indexName := "pages"
	res, err := es.Indices.Exists([]string{indexName})
	if err != nil {
		return err
	}
	defer res.Body.Close()

	// StatusCode 404 means index doesn't exist
	if res.StatusCode == 404 {
		// Create the index with a mapping
		mapping := `{
			"mappings": {
				"properties": {
					"url": {"type": "text"},
					"title": {"type": "text"},
					"content": {"type": "text"}
				}
			}
		}`
		req := esapi.IndicesCreateRequest{
			Index: indexName,
			Body:  strings.NewReader(mapping),
		}
		res, err := req.Do(context.Background(), es)
		if err != nil {
			return err
		}
		defer res.Body.Close()

		if res.IsError() {
			var e map[string]interface{}
			if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
				return err
			}
			return fmt.Errorf("Failed to create index: %v", e)
		}
	}
	return nil
}

func connectElasticsearch() (*elasticsearch.Client, error) {
	retries := 5
	var lastErr error

	elasticURL := os.Getenv("ELASTICSEARCH_URL")
	if elasticURL == "" {
		elasticURL = "https://localhost:9200"
	}

	// Load the CA certificate
	caCert, err := os.ReadFile("http_ca.crt")
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate: %v", err)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	// Configure the Elasticsearch client to use HTTPS and the CA certificate
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: caCertPool},
	}
	httpClient := &http.Client{Transport: transport}

	for i := 0; i < retries; i++ {
		client, err := elasticsearch.NewClient(
			elasticsearch.Config{
				Addresses: []string{elasticURL},
				Username:  "elastic",
				Password:  "=TqLInd1UL44AL49kmVm",
				Transport: httpClient.Transport,
			},
		)
		if err != nil {
			fmt.Printf("Error creating Elasticsearch client (attempt %d): %v\n", i+1, err)
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}

		// Health check to ensure Elasticsearch is ready
		for j := 0; j < 10; j++ {
			res, err := client.Cluster.Health(client.Cluster.Health.WithContext(context.Background()))
			if err != nil {
				fmt.Printf("Error checking Elasticsearch health (attempt %d): %v\n", j+1, err)
				time.Sleep(2 * time.Second)
				continue
			}
			defer res.Body.Close()

			if res.StatusCode == http.StatusOK {
				var responseBody map[string]interface{}
				if err := json.NewDecoder(res.Body).Decode(&responseBody); err != nil {
					fmt.Printf("Error decoding Elasticsearch health response (attempt %d): %v\n", j+1, err)
					time.Sleep(2 * time.Second)
					continue
				}

				status := responseBody["status"].(string)
				if status == "green" || status == "yellow" {
					fmt.Println("Elasticsearch is healthy and ready")
					return client, nil
				}
			}
		}

		fmt.Printf("Elasticsearch not ready after health checks (attempt %d)\n", i+1)
		lastErr = fmt.Errorf("Elasticsearch not ready")
		time.Sleep(5 * time.Second)
	}

	return nil, fmt.Errorf("failed to connect to Elasticsearch after %d attempts: %v", retries, lastErr)
}
