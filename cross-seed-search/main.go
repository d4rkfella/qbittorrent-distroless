package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-playground/validator/v10"
	"golang.org/x/time/rate"
)

func isHexString(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

var (
	version    = "dev"
	commit     = ""
	date       = ""
	log        *slog.Logger
	validate   = validator.New()
	httpClient = createHTTPClient()
)

type Config struct {
	CrossSeedEnabled bool
	CrossSeedURL     string
	CrossSeedAPIKey  string
	PushoverEnabled  bool
	PushoverUserKey  string
	PushoverToken    string
}

type ReleaseInfo struct {
	Name     string `validate:"required"`
	InfoHash string `validate:"required,infohash"`
	Category string `validate:"required"`
	Size     int64  `validate:"gt=0"`
	Indexer  string `validate:"required,url"`
	Type     string `validate:"required"`
}

func init() {
	err := validate.RegisterValidation("infohash", func(fl validator.FieldLevel) bool {
		hash := fl.Field().String()
		return len(hash) == 40 && isHexString(hash)
	})

	if err != nil {
		panic("Failed to register custom validator: " + err.Error())
	}
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	defer func() {
		if r := recover(); r != nil {
			log.Error("Critical error recovered",
				"panic", r,
				"stack", string(debug.Stack()))
			os.Exit(1)
		}
	}()

	configureLogger()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info("Starting torrent notifier",
		"version", version,
		"commit", commit,
		"date", date)

	cfg := loadConfig()
	log.Debug("Loaded configuration",
		"cross_seed_enabled", cfg.CrossSeedEnabled,
		"pushover_enabled", cfg.PushoverEnabled,
	)

	if len(os.Args) != 6 {
		log.Error("Invalid arguments",
			"usage", fmt.Sprintf("%s <release_name> <info_hash> <category> <size> <indexer>", os.Args[0]))
		os.Exit(1)
	}

	release, err := parseAndValidateReleaseInfo(os.Args[1:])
	if err != nil {
		log.Error("Invalid input", "error", err)
		os.Exit(1)
	}

	limiter := rate.NewLimiter(rate.Every(5*time.Second), 2)

	if cfg.PushoverEnabled {
		if cfg.PushoverUserKey == "" || cfg.PushoverToken == "" {
			log.Error("Pushover enabled but missing credentials")
			os.Exit(1)
		}

		if err := limiter.Wait(ctx); err != nil {
			log.WarnContext(ctx, "Rate limit exceeded for Pushover", "error", err)
		} else {
			if err := sendPushoverNotification(ctx, cfg, release); err != nil {
				log.ErrorContext(ctx, "Pushover notification failed", "error", err)
			}
		}
	}

	if cfg.CrossSeedEnabled {
		if cfg.CrossSeedURL == "" || cfg.CrossSeedAPIKey == "" {
			log.Error("CrossSeed enabled but missing configuration")
			os.Exit(1)
		}

		if err := limiter.Wait(ctx); err != nil {
			log.WarnContext(ctx, "Rate limit exceeded for CrossSeed", "error", err)
		} else {
			if err := searchCrossSeed(ctx, cfg, release); err != nil {
				log.ErrorContext(ctx, "CrossSeed search failed", "error", err)
			}
		}
	}

	log.Info("Processing completed successfully")
}

func createHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				CipherSuites: []uint16{
					tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
					tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				},
			},
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 0,
			}).DialContext,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func configureLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     getLogLevel(),
		AddSource: false,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			switch a.Key {
			case slog.LevelKey:
				return slog.Attr{Key: "severity", Value: a.Value}
			case slog.TimeKey:
				return slog.Attr{Key: "timestamp", Value: a.Value}
			case slog.MessageKey:
				return slog.Attr{Key: "message", Value: a.Value}
			}
			return a
		},
	}).WithAttrs([]slog.Attr{
		slog.String("service", "qbittorrent-notifier"),
	})

	log = slog.New(handler)
}

func getLogLevel() slog.Level {
	level := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch level {
	case "DEBUG":
		return slog.LevelDebug
	case "INFO":
		return slog.LevelInfo
	case "WARN", "WARNING":
		return slog.LevelWarn
	case "ERROR":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func loadConfig() *Config {
	return &Config{
		CrossSeedEnabled: getEnvBool("CROSS_SEED_ENABLED", false),
		CrossSeedURL:     os.Getenv("CROSS_SEED_URL"),
		CrossSeedAPIKey:  os.Getenv("CROSS_SEED_API_KEY"),
		PushoverEnabled:  getEnvBool("PUSHOVER_ENABLED", false),
		PushoverUserKey:  os.Getenv("PUSHOVER_USER_KEY"),
		PushoverToken:    os.Getenv("PUSHOVER_TOKEN"),
	}
}

func getEnvBool(key string, defaultValue bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	return strings.ToLower(val) == "true"
}

func getEnvInt(key string, defaultValue int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultValue
	}
	result, err := strconv.Atoi(val)
	if err != nil {
		return defaultValue
	}
	return result
}

func parseAndValidateReleaseInfo(args []string) (*ReleaseInfo, error) {
	if len(args) != 5 {
		return nil, errors.New("invalid number of arguments (need 5)")
	}

	size, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid size: %w", err)
	}

	release := &ReleaseInfo{
		Name:     strings.TrimSpace(args[0]),
		InfoHash: strings.ToLower(strings.TrimSpace(args[1])),
		Category: strings.TrimSpace(args[2]),
		Size:     size,
		Indexer:  strings.TrimSpace(args[4]),
		Type:     "Torrent",
	}

	if err := validate.Struct(release); err != nil {
		return nil, fmt.Errorf("validation failed: %w", err)
	}

	return release, nil
}

func sendPushoverNotification(ctx context.Context, cfg *Config, release *ReleaseInfo) error {

	message := fmt.Sprintf(
		"<b>%s</b><small>\n<b>Category:</b> %s</small><small>\n<b>Indexer:</b> %s</small><small>\n<b>Size:</b> %s</small>",
		html.EscapeString(strings.TrimSuffix(release.Name, ".torrent")),
		html.EscapeString(release.Category),
		html.EscapeString(release.Indexer),
		humanize.Bytes(uint64(release.Size)),
	)

	payload := map[string]string{
		"token":    cfg.PushoverToken,
		"user":     cfg.PushoverUserKey,
		"title":    fmt.Sprintf("%s Downloaded", release.Type),
		"message":  message,
		"priority": "-2",
		"html":     "1",
	}

	return retryOperation(ctx, 3, 2*time.Second, func() error {
		return sendHTTPRequest(
			ctx,
			http.MethodPost,
			"https://api.pushover.net/1/messages.json",
			payload,
			map[string]string{"Content-Type": "application/json"},
			http.StatusOK,
		)
	})
}

func searchCrossSeed(ctx context.Context, cfg *Config, release *ReleaseInfo) error {
	targetURL, err := buildSafeURL(cfg.CrossSeedURL, "/api/webhook")
	if err != nil {
		return fmt.Errorf("failed to build safe URL: %w", err)
	}

	data := url.Values{}
	data.Set("infoHash", release.InfoHash)
	data.Set("includeSingleEpisodes", "true")

	return retryOperation(ctx, 3, 2*time.Second, func() error {
		return sendHTTPRequest(
			ctx,
			http.MethodPost,
			targetURL,
			data.Encode(),
			map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
				"X-Api-Key":    cfg.CrossSeedAPIKey,
			},
			http.StatusNoContent,
		)
	})
}

func sendHTTPRequest(
	ctx context.Context,
	method string,
	targetURL string,
	body interface{},
	headers map[string]string,
	expectedStatus int,
) error {
	var reqBody io.Reader

	if ct, exists := headers["Content-Type"]; exists {
		switch ct {
		case "application/x-www-form-urlencoded":
			s, ok := body.(string)
			if !ok {
				return fmt.Errorf("form data must be string, got %T", body)
			}
			reqBody = strings.NewReader(s)

		case "application/json":
			jsonData, err := json.Marshal(body)
			if err != nil {
				return fmt.Errorf("failed to marshal JSON: %w", err)
			}
			reqBody = bytes.NewReader(jsonData)

		default:
			return fmt.Errorf("unsupported Content-Type: %s", ct)
		}
	} else {
		if headers == nil {
			headers = make(map[string]string)
		}
		jsonData, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal JSON: %w", err)
		}
		reqBody = bytes.NewReader(jsonData)
		headers["Content-Type"] = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	log.DebugContext(ctx, "Sending HTTP request",
		"url", targetURL,
		"method", method,
		"headers", redactHeaders(headers))

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	log.DebugContext(ctx, "HTTP response received",
		"status", resp.StatusCode,
		"body", redactBody(string(respBody)),
	)

	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("unexpected status %d (expected %d)",
			resp.StatusCode, expectedStatus)
	}

	log.Info("HTTP request was successful")

	return nil
}

func redactHeaders(headers map[string]string) map[string]string {
	safe := make(map[string]string)
	for k, v := range headers {
		if strings.EqualFold(k, "X-Api-Key") {
			safe[k] = "[REDACTED]"
		} else {
			safe[k] = v
		}
	}
	return safe
}

func retryOperation(ctx context.Context, maxAttempts int, initialDelay time.Duration, op func() error) error {
	const maxTotalTimeout = 10 * time.Minute
	ctx, cancel := context.WithTimeout(ctx, maxTotalTimeout)
	defer cancel()

	var err error
	delay := initialDelay

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = op()
		if err == nil {
			return nil
		}

		if !isRetriableError(err) {
			return err
		}

		if attempt == maxAttempts {
			break
		}

		log.WarnContext(ctx, "Operation attempt failed",
			"attempt", attempt,
			"error", err,
			"retry_in", delay)

		select {
		case <-time.After(delay):
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return fmt.Errorf("operation failed after %d attempts: %w", maxAttempts, err)
}

func isRetriableError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}

	var statusErr interface{ StatusCode() int }
	if errors.As(err, &statusErr) {
		code := statusErr.StatusCode()
		return code == http.StatusTooManyRequests ||
			code >= http.StatusInternalServerError
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	return false
}

func redactBody(content string) string {
	if strings.Contains(content, "api_key") {
		return "[REDACTED_API_KEY]"
	}
	if strings.Contains(content, "token") {
		return "[REDACTED_TOKEN]"
	}
	if len(content) > 100 {
		return fmt.Sprintf("[TRUNCATED_LEN=%d]", len(content))
	}
	return content
}

func buildSafeURL(baseURL, urlPath string) (string, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid base URL: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid URL scheme: %s", u.Scheme)
	}

	if strings.Contains(urlPath, "..") || strings.Contains(urlPath, "//") {
		return "", errors.New("invalid path containing traversal attempt")
	}

	if os.Getenv("ENV") == "production" && u.Scheme != "https" {
		return "", errors.New("insecure scheme in production environment")
	}

	newURL, err := u.Parse(urlPath)
	if err != nil {
		return "", fmt.Errorf("failed to construct safe URL: %w", err)
	}

	newURL.Fragment = ""
	newURL.RawPath = ""

	newURL.Path = path.Clean(newURL.Path)

	return newURL.String(), nil
}
