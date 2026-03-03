package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// headerFlag supports multiple -header flags
type headerFlag []string

func (h *headerFlag) String() string { return strings.Join(*h, "; ") }
func (h *headerFlag) Set(v string) error {
	*h = append(*h, v)
	return nil
}

// Config holds all load test configuration
type Config struct {
	// Target
	URL         string
	Method      string
	Headers     map[string]string
	Body        string
	ContentType string

	// HTTP client
	Timeout   time.Duration
	KeepAlive bool

	// Rate control
	RatePerHour int64
	RPS         float64 // overrides RatePerHour when > 0

	// Run control
	Duration    time.Duration
	MaxRequests int64
	Workers     int

	// Output
	OutputFile string
	Verbose    bool
}

// GetRPS returns the effective requests-per-second target
func (c *Config) GetRPS() float64 {
	if c.RPS > 0 {
		return c.RPS
	}
	return float64(c.RatePerHour) / 3600.0
}

// GetRPH returns the effective requests-per-hour target
func (c *Config) GetRPH() int64 {
	if c.RatePerHour > 0 {
		return c.RatePerHour
	}
	return int64(c.RPS * 3600)
}

func (c *Config) Validate() error {
	if c.URL == "" {
		return fmt.Errorf("-url is required")
	}
	if _, err := http.NewRequest(c.Method, c.URL, nil); err != nil {
		return fmt.Errorf("invalid URL or method: %v", err)
	}
	if c.RatePerHour <= 0 && c.RPS <= 0 {
		return fmt.Errorf("-rate or -rps must be positive")
	}
	if c.Duration <= 0 {
		return fmt.Errorf("-duration must be positive")
	}
	if c.Workers <= 0 {
		return fmt.Errorf("-workers must be positive")
	}
	return nil
}

func (c *Config) Banner() string {
	var b strings.Builder
	b.WriteString("\n╔══════════════════════════════════════════════════════╗\n")
	b.WriteString("║              Load Test Platform  v1.0               ║\n")
	b.WriteString("╚══════════════════════════════════════════════════════╝\n\n")
	fmt.Fprintf(&b, "  URL        : %s\n", c.URL)
	fmt.Fprintf(&b, "  Method     : %s\n", c.Method)
	fmt.Fprintf(&b, "  Rate       : %s req/h  (~%.2f req/s)\n", commaInt(c.GetRPH()), c.GetRPS())
	fmt.Fprintf(&b, "  Duration   : %s\n", c.Duration)
	fmt.Fprintf(&b, "  Workers    : %d\n", c.Workers)
	fmt.Fprintf(&b, "  Timeout    : %s\n", c.Timeout)
	fmt.Fprintf(&b, "  Keep-Alive : %v\n", c.KeepAlive)
	if c.MaxRequests > 0 {
		fmt.Fprintf(&b, "  Max Req    : %s\n", commaInt(c.MaxRequests))
	}
	if len(c.Headers) > 0 {
		for k, v := range c.Headers {
			fmt.Fprintf(&b, "  Header     : %s: %s\n", k, v)
		}
	}
	b.WriteString("\n")
	return b.String()
}

func parseConfig() *Config {
	var rawHeaders headerFlag
	cfg := &Config{}

	flag.StringVar(&cfg.URL, "url", "", "Target URL (required)")
	flag.StringVar(&cfg.Method, "method", "GET", "HTTP method: GET, POST, PUT, DELETE, PATCH, HEAD")
	flag.Var(&rawHeaders, "header", "HTTP header in 'Key: Value' format (repeatable)")
	flag.StringVar(&cfg.Body, "body", "", "Request body (for POST/PUT/PATCH)")
	flag.StringVar(&cfg.ContentType, "content-type", "", "Content-Type header shortcut")
	flag.Int64Var(&cfg.RatePerHour, "rate", 200_000, "Target requests per hour")
	flag.Float64Var(&cfg.RPS, "rps", 0, "Target requests per second (overrides -rate when set)")
	flag.DurationVar(&cfg.Duration, "duration", 60*time.Second, "Test duration (e.g. 30s, 5m, 1h)")
	flag.Int64Var(&cfg.MaxRequests, "max-requests", 0, "Maximum total requests to send (0 = no limit)")
	flag.IntVar(&cfg.Workers, "workers", 200, "Number of concurrent worker goroutines")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "HTTP request timeout")
	flag.BoolVar(&cfg.KeepAlive, "keepalive", true, "Enable HTTP keep-alive / connection reuse")
	flag.StringVar(&cfg.OutputFile, "output", "", "Save JSON report to file (optional)")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Show verbose error output")

	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, `Load Test Platform — high-throughput HTTP load testing (default: 200k req/hour)

Usage:
  loadtest [options]

Options:`)
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr, `
Examples:
  # 200k req/hour for 1 minute (default rate)
  loadtest -url http://localhost:8080/api -duration 1m

  # POST with JSON body at 500k req/hour for 5 minutes
  loadtest -url http://api.example.com/v1/events \
    -method POST -body '{"event":"click"}' -content-type application/json \
    -rate 500000 -duration 5m

  # 1000 req/s with custom headers, save report
  loadtest -url http://localhost:8080 -rps 1000 \
    -header "Authorization: Bearer mytoken" \
    -duration 30s -output report.json

  # Stress test: 1M req/hour, 500 workers
  loadtest -url http://localhost:8080 -rate 1000000 -workers 500 -duration 10m`)
	}

	flag.Parse()

	// Parse raw headers
	cfg.Headers = make(map[string]string, len(rawHeaders))
	for _, h := range rawHeaders {
		if idx := strings.IndexByte(h, ':'); idx > 0 {
			cfg.Headers[strings.TrimSpace(h[:idx])] = strings.TrimSpace(h[idx+1:])
		}
	}

	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		flag.Usage()
		os.Exit(1)
	}
	return cfg
}

// commaInt formats an int64 with thousand separators
func commaInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+(len(s)-1)/3)
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}
