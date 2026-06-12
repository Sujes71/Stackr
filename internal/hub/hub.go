package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

const baseURL = "https://hub.docker.com/v2"

var client = &http.Client{Timeout: 10 * time.Second}

// ── Search ────────────────────────────────────────────────────────────────────

type SearchResult struct {
	Name        string `json:"repo_name"`
	Description string `json:"short_description"`
	IsOfficial  bool   `json:"is_official"`
	StarCount   int    `json:"star_count"`
	PullCount   int    `json:"pull_count"`
}

type searchResponse struct {
	Results []SearchResult `json:"results"`
	Count   int            `json:"count"`
}

func Search(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 15
	}
	u := fmt.Sprintf("%s/search/repositories/?query=%s&page_size=%d",
		baseURL, url.QueryEscape(query), limit)

	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("hub search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub search returned %d", resp.StatusCode)
	}

	var sr searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, err
	}
	return sr.Results, nil
}

// ── Tags ─────────────────────────────────────────────────────────────────────

type Tag struct {
	Name        string    `json:"name"`
	LastUpdated time.Time `json:"last_updated"`
	FullSize    int64     `json:"full_size"`
}

type tagsResponse struct {
	Results []Tag `json:"results"`
	Next    string `json:"next"`
}

// FetchTags returns the most recent tags for an image.
// image can be "postgres", "library/postgres", or "myorg/myimage".
func FetchTags(image string, limit int) ([]Tag, error) {
	if limit <= 0 {
		limit = 20
	}
	// Official images live under library/
	if !containsSlash(image) {
		image = "library/" + image
	}
	u := fmt.Sprintf("%s/repositories/%s/tags?page_size=%d&ordering=last_updated",
		baseURL, image, limit)

	resp, err := client.Get(u)
	if err != nil {
		return nil, fmt.Errorf("hub tags: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hub tags returned %d", resp.StatusCode)
	}

	var tr tagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, err
	}
	return tr.Results, nil
}

func containsSlash(s string) bool {
	for _, c := range s {
		if c == '/' {
			return true
		}
	}
	return false
}

// ── Curated catalog ──────────────────────────────────────────────────────────

type CatalogEntry struct {
	Name               string
	Image              string
	Description        string
	DefaultTag         string
	DefaultPorts       []string
	DefaultEnv         map[string]string
	DefaultVolumeTarget string // e.g. /var/lib/postgresql/data
}

// Catalog returns the built-in curated list of common dev services.
// Users can use any of these as a starting point before customising.
var Catalog = []CatalogEntry{
	{
		Name:        "PostgreSQL",
		Image:       "postgres",
		Description: "The world's most advanced open source relational database",
		DefaultTag:  "15",
		DefaultPorts: []string{"5432:5432"},
		DefaultEnv: map[string]string{
			"POSTGRES_DB":       "mydb",
			"POSTGRES_USER":     "postgres",
			"POSTGRES_PASSWORD": "postgres",
		},
		DefaultVolumeTarget: "/var/lib/postgresql/data",
	},
	{
		Name:        "MongoDB",
		Image:       "mongo",
		Description: "Document-oriented NoSQL database",
		DefaultTag:  "7",
		DefaultPorts: []string{"27017:27017"},
		DefaultEnv: map[string]string{
			"MONGO_INITDB_ROOT_USERNAME": "admin",
			"MONGO_INITDB_ROOT_PASSWORD": "admin",
			"MONGO_INITDB_DATABASE":      "mydb",
		},
		DefaultVolumeTarget: "/data/db",
	},
	{
		Name:        "Redis",
		Image:       "redis",
		Description: "In-memory data structure store",
		DefaultTag:  "7-alpine",
		DefaultPorts: []string{"6379:6379"},
		DefaultEnv:  map[string]string{},
		DefaultVolumeTarget: "/data",
	},
	{
		Name:        "MySQL",
		Image:       "mysql",
		Description: "Popular open source relational database",
		DefaultTag:  "8",
		DefaultPorts: []string{"3306:3306"},
		DefaultEnv: map[string]string{
			"MYSQL_ROOT_PASSWORD": "root",
			"MYSQL_DATABASE":      "mydb",
			"MYSQL_USER":          "user",
			"MYSQL_PASSWORD":      "password",
		},
		DefaultVolumeTarget: "/var/lib/mysql",
	},
	{
		Name:        "ActiveMQ",
		Image:       "apache/activemq-classic",
		Description: "Popular open source message broker",
		DefaultTag:  "latest",
		DefaultPorts: []string{"61616:61616", "8161:8161"},
		DefaultEnv: map[string]string{
			"ACTIVEMQ_USERNAME":    "admin",
			"ACTIVEMQ_PASSWORD":    "admin",
			"ACTIVEMQ_SSL_ENABLED": "false",
			"ACTIVEMQ_OPTS":        "-Xms512m -Xmx1g -Dcom.sun.management.jmxremote -Dcom.sun.management.jmxremote.port=1099 -Dcom.sun.management.jmxremote.rmi.port=1100 -Dcom.sun.management.jmxremote.authenticate=false -Dcom.sun.management.jmxremote.ssl=false -Djava.rmi.server.hostname=localhost",
		},
		DefaultVolumeTarget: "/opt/activemq/data",
	},
	{
		Name:        "RabbitMQ",
		Image:       "rabbitmq",
		Description: "Open source message broker with management UI",
		DefaultTag:  "3-management",
		DefaultPorts: []string{"5672:5672", "15672:15672"},
		DefaultEnv: map[string]string{
			"RABBITMQ_DEFAULT_USER": "admin",
			"RABBITMQ_DEFAULT_PASS": "admin",
		},
		DefaultVolumeTarget: "/var/lib/rabbitmq",
	},
	{
		Name:        "Kafka",
		Image:       "apache/kafka",
		Description: "Distributed event streaming platform",
		DefaultTag:  "latest",
		DefaultPorts: []string{"9092:9092"},
		DefaultEnv: map[string]string{
			"KAFKA_LISTENERS":            "PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093",
			"KAFKA_ADVERTISED_LISTENERS": "PLAINTEXT://localhost:9092",
		},
		DefaultVolumeTarget: "/var/lib/kafka/data",
	},
	{
		Name:        "Elasticsearch",
		Image:       "elasticsearch",
		Description: "Distributed search and analytics engine",
		DefaultTag:  "8.13.0",
		DefaultPorts: []string{"9200:9200", "9300:9300"},
		DefaultEnv: map[string]string{
			"discovery.type":         "single-node",
			"ELASTIC_PASSWORD":       "elastic",
			"xpack.security.enabled": "true",
		},
		DefaultVolumeTarget: "/usr/share/elasticsearch/data",
	},
	{
		Name:        "MinIO",
		Image:       "minio/minio",
		Description: "High-performance S3-compatible object storage",
		DefaultTag:  "latest",
		DefaultPorts: []string{"9000:9000", "9001:9001"},
		DefaultEnv: map[string]string{
			"MINIO_ROOT_USER":     "minioadmin",
			"MINIO_ROOT_PASSWORD": "minioadmin",
		},
		DefaultVolumeTarget: "./data",
	},
	{
		Name:        "Mailpit",
		Image:       "axllent/mailpit",
		Description: "Email testing tool with web UI",
		DefaultTag:  "latest",
		DefaultPorts: []string{"1025:1025", "8025:8025"},
		DefaultEnv:  map[string]string{},
	},
	{
		Name:        "MockServer",
		Image:       "mockserver/mockserver",
		Description: "HTTP mock server & proxy for API testing",
		DefaultTag:  "latest",
		DefaultPorts: []string{"1080:1080"},
		DefaultEnv: map[string]string{
			"MOCKSERVER_LOG_LEVEL": "INFO",
		},
		DefaultVolumeTarget: "/config",
	},
}
