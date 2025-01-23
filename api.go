package main

import (
	"context"
	"database/sql"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"encoding/json"

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
	// Removed esClient.Stop() as it is not defined in the new elasticsearch.Client
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
	e.POST("/crawl", func(c echo.Context) error {
		url := c.FormValue("url")
		if url == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "URL is required"})
		}

		err := Crawl(db, es, url)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}

		return c.JSON(http.StatusOK, map[string]string{"message": "Crawling started"})
	})

	e.GET("/", func(c echo.Context) error {
		return c.Render(http.StatusOK, "index.html", nil)
	})

	e.GET("/search", func(c echo.Context) error {
		query := c.QueryParam("q")
		if query == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "search query is required"})
		}

		var b esapi.SearchRequest
		b = esapi.SearchRequest{
			Index: []string{"pages"},
			Query: "{\"multi_match\":{\"query\":\"" + query + "\",\"fields\":[\"title\",\"content\"]}}",
		}

		result, err := b.Do(context.Background(), es)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		defer result.Body.Close()

		var r map[string]interface{}
		if err := json.NewDecoder(result.Body).Decode(&r); err != nil {
			log.Printf("Error parsing the response body: %s", err)
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to parse response"})
		}

		// Safely check if hits exists and is a map
		if hits, ok := r["hits"].(map[string]interface{}); ok {
			if hitsResult, ok := hits["hits"].([]interface{}); ok {
				return c.Render(http.StatusOK, "search_results.html", hitsResult)
			}
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Invalid hits structure"})
		}

		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "Failed to get hits from response"})
	})
}

func connectElasticsearch() (*elasticsearch.Client, error) {
	retries := 5
	var lastErr error

	elasticURL := os.Getenv("ELASTICSEARCH_URL")
	if elasticURL == "" {
		elasticURL = "http://localhost:9200"
	}

	for i := 0; i < retries; i++ {
		client, err := elasticsearch.NewClient(
			elasticsearch.Config{
				Addresses: []string{elasticURL},
				// Remove MaxRetries from the config
				// MaxRetries: 5,
				// Remove RetryOnStatus from the config
				// RetryOnStatus: []int{502, 503, 504},
			},
		)
		if err != nil {
			fmt.Printf("Error creating Elasticsearch client (attempt %d): %v\n", i+1, err)
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}

		// Ping the Elasticsearch server to get e.g. the version number
		_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		info, err := client.Info()
		if err != nil {
			fmt.Printf("Error pinging Elasticsearch (attempt %d): %v\n", i+1, err)
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}

		var responseBody map[string]interface{}
		json.NewDecoder(info.Body).Decode(&responseBody)
		version := responseBody["version"].(map[string]interface{})["number"].(string)
		fmt.Printf("Elasticsearch returned with version %s\n", version)
		return client, nil
	}

	return nil, fmt.Errorf("failed to connect to Elasticsearch after %d attempts: %v", retries, lastErr)
}
