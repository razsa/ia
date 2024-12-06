package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/gocolly/colly/v2"
	_ "github.com/lib/pq"
)

// Crawl starts the web crawling process.  It takes the database and Elasticsearch client as parameters.
func Crawl(db *sql.DB, es *elasticsearch.Client, startURL string) error {
	// Ensure index exists before starting
	if err := ensureIndexExists(es); err != nil {
		return fmt.Errorf("failed to create index: %v", err)
	}

	c := colly.NewCollector()

	c.OnHTML("a[href]", func(e *colly.HTMLElement) {
		link := e.Attr("href")
		// Properly handle relative URLs
		absURL, err := url.Parse(link)
		if err != nil {
			log.Printf("Error parsing URL '%s': %v", link, err)
			return
		}

		baseURL, err := url.Parse(startURL) // Parse startURL into a *url.URL
		if err != nil {
			log.Printf("Error parsing base URL '%s': %v", startURL, err)
			return // Handle error appropriately, perhaps retrying or skipping
		}

		absURL = baseURL.ResolveReference(absURL) // Now this works correctly

		_, err = db.Exec("INSERT INTO crawl_queue (url) VALUES ($1)", absURL.String())
		if err != nil {
			log.Printf("Error inserting '%s' into queue: %v", absURL.String(), err)
		}
	})

	c.OnResponse(func(r *colly.Response) {
		// Create a URL-safe document ID
		docID := url.QueryEscape(r.Request.URL.String())

		// Create a structured document
		doc := map[string]interface{}{
			"url":       r.Request.URL.String(),
			"content":   string(r.Body),
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}

		// Convert document to JSON
		docJSON, err := json.Marshal(doc)
		if err != nil {
			log.Printf("Error marshaling document: %v", err)
			return
		}

		// Create index request
		req := esapi.IndexRequest{
			Index:      "pages",
			DocumentID: docID,
			Body:       strings.NewReader(string(docJSON)),
			Refresh:    "true",
		}

		res, err := req.Do(context.Background(), es)
		if err != nil {
			log.Printf("Error indexing page '%s': %v", r.Request.URL.String(), err)
			return
		}
		defer res.Body.Close()

		if res.IsError() {
			var e map[string]interface{}
			if err := json.NewDecoder(res.Body).Decode(&e); err != nil {
				log.Printf("Error parsing error response: %v", err)
				return
			}
			log.Printf("Error indexing document: %v", e)
			return
		}

		log.Printf("Successfully indexed page '%s'", r.Request.URL.String())
	})

	// Error Handling for Visit
	err := c.Visit(startURL)
	if err != nil {
		return err
	}

	return nil
}
