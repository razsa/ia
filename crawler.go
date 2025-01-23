package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/url"
	"strings"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
	"github.com/gocolly/colly/v2"
	_ "github.com/lib/pq"
)

// Crawl starts the web crawling process.  It takes the database and Elasticsearch client as parameters.
func Crawl(db *sql.DB, es *elasticsearch.Client, startURL string) error {
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

		_, err = db.Exec("INSERT INTO crawl_queue (url) VALUES ($1) ON CONFLICT (url) DO NOTHING", absURL.String())
		if err != nil {
			log.Printf("Error inserting '%s' into queue: %v", absURL.String(), err)
		}

	})

	c.OnResponse(func(r *colly.Response) {
		// Create a new context for the request
		ctx := context.Background()

		// Create a document ID (you might want to generate this differently)
		// docID := r.Request.URL.String()

		// Create the document data
		data := map[string]interface{}{
			"url":     r.Request.URL.String(),
			"content": string(r.Body),
		}

		// Create the request body
		body, err := json.Marshal(data)
		if err != nil {
			log.Printf("Error marshaling document: %v", err)
			return
		}

		// Perform the index request
		req := esapi.IndexRequest{
			Index:   "pages",
			Body:    strings.NewReader(string(body)),
			Refresh: "true",
			// DocumentID: docID,
		}

		res, err := req.Do(ctx, es)
		if err != nil {
			log.Printf("Error indexing page '%s': %v", r.Request.URL.String(), err)
			return
		}
		defer res.Body.Close()

		if res.IsError() {
			// Read the response body for detailed error
			bodyBytes, err := io.ReadAll(res.Body)
			if err != nil {
				log.Printf("Error reading Elasticsearch error response: %v", err)
			} else {
				log.Printf("Indexing failed with status code %d", res.StatusCode, string(bodyBytes))
			}
		}
	})

	//Error Handling for Visit
	err := c.Visit(startURL)
	if err != nil {
		return err
	}

	return nil
}
