package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/go-playground/validator/v10"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"golang.org/x/time/rate"
)

var (
	version    = "dev"
	buildTime  = ""
	log        = logrus.New()
	validate   = validator.New()
	httpClient = &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  false,
			DisableKeepAlives:   false,
			MaxIdleConnsPerHost: 10,
		},
	}
)

type Config struct {
	CrossSeedEnabled       bool   `mapstructure:"CROSS_SEED_ENABLED"`
	CrossSeedURL          string `mapstructure:"CROSS_SEED_URL"`
	CrossSeedAPIKey       string `mapstructure:"CROSS_SEED_API_KEY"`
	CrossSeedSleepInterval int    `mapstructure:"CROSS_SEED_SLEEP_INTERVAL"`
	PushoverEnabled       bool   `mapstructure:"PUSHOVER_ENABLED"`
	PushoverUserKey       string `mapstructure:"PUSHOVER_USER_KEY"`
	PushoverToken         string `mapstructure:"PUSHOVER_TOKEN"`
}

type ReleaseInfo struct {
	Name     string `validate:"required"`
	InfoHash string `validate:"required,hexadecimal,len=40"`
	Category string `validate:"required"`
	Size     int64  `validate:"gt=0"`
	Indexer  string `validate:"required,url"`
	Type     string `validate:"required"`
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	configureLogger()
	log.WithFields(logrus.Fields{
		"version":    version,
		"buildTime":  buildTime,
	}).Info("Starting torrent notifier")

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("Configuration error: %v", err)
	}

	if len(os.Args) != 6 {
		log.Fatalf("Usage: %s <release_name> <info_hash> <category> <size> <indexer>", os.Args[0])
	}

	release, err := parseAndValidateReleaseInfo(os.Args[1:])
	if err != nil {
		log.Fatalf("Invalid input: %v", err)
	}

	limiter := rate.NewLimiter(rate.Every(5*time.Second), 2)

	if cfg.PushoverEnabled {
		if err := validatePushoverConfig(cfg); err != nil {
			log.Fatalf("Pushover config error: %v", err)
		}

		if err := limiter.Wait(ctx); err != nil {
			log.Warnf("Rate limit exceeded for Pushover: %v", err)
		} else {
			if err := sendPushoverNotification(ctx, cfg, release); err != nil {
				log.Errorf("Pushover notification failed: %v", err)
			}
		}
	}

	if cfg.CrossSeedEnabled {
		if err := validateCrossSeedConfig(cfg); err != nil {
			log.Fatalf("CrossSeed config error: %v", err)
		}

		if err := limiter.Wait(ctx); err != nil {
			log.Warnf("Rate limit exceeded for CrossSeed: %v", err)
		} else {
			if err := searchCrossSeed(ctx, cfg, release); err != nil {
				log.Errorf("CrossSeed search failed: %v", err)
			}
		}
	}

	log.Info("Processing completed successfully")
}

func configureLogger() {
	log.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyLevel: "severity",
			logrus.FieldKeyMsg:   "message",
		},
	})
	log.SetOutput(os.Stdout)

	if level, err := logrus.ParseLevel(os.Getenv("LOG_LEVEL")); err == nil {
		log.SetLevel(level)
	} else {
		log.SetLevel(logrus.InfoLevel)
	}
}

func loadConfig() (*Config, error) {
	viper.AutomaticEnv()
	viper.SetConfigName("config")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/etc/torrent-notifier/")
	viper.SetDefault("CROSS_SEED_SLEEP_INTERVAL", 30)

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, fmt.Errorf("config file error: %w", err)
		}
	}

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("config unmarshal failed: %w", err)
	}

	return cfg, nil
}

func parseAndValidateReleaseInfo(args []string) (*ReleaseInfo, error) {
	if len(args) != 5 {
		return nil, errors.New("invalid number of arguments (need 5)")
	}

	size, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil || size <= 0 {
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
	hostname, err := extractHostname(release.Indexer)
	if err != nil {
		return fmt.Errorf("failed to parse indexer URL: %w", err)
	}

	message := fmt.Sprintf("<b>%s</b><small>\n<b>Category:</b> %s</small><small>\n<b>Indexer:</b> %s</small><small>\n<b>Size:</b> %s</small>",
		strings.TrimSuffix(release.Name, ".torrent"),
		release.Category,
		hostname,
		humanize.Bytes(uint64(release.Size)),
	)

	payload := map[string]string{
		"token":     cfg.PushoverToken,
		"user":      cfg.PushoverUserKey,
		"title":     fmt.Sprintf("%s Downloaded", release.Type),
		"message":   message,
		"priority":  "-2",
		"html":      "1",
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
	data := url.Values{}
	data.Set("infoHash", release.InfoHash)
	data.Set("includeSingleEpisodes", "true")

	return retryOperation(ctx, 3, 2*time.Second, func() error {
		return sendHTTPRequest(
			ctx,
			http.MethodPost,
			fmt.Sprintf("%s/api/webhook", cfg.CrossSeedURL),
			data.Encode(),
			map[string]string{
				"Content-Type": "application/x-www-form-urlencoded",
				"X-Api-Key":    cfg.CrossSeedAPIKey,
			},
			http.StatusOK,
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

	switch v := body.(type) {
	case string:
		reqBody = strings.NewReader(v)
	default:
		jsonData, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonData)
	}

	req, err := http.NewRequestWithContext(ctx, method, targetURL, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != expectedStatus {
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	log.WithFields(logrus.Fields{
		"url":    targetURL,
		"status": resp.StatusCode,
	}).Debug("HTTP request successful")

	return nil
}

func retryOperation(ctx context.Context, maxAttempts int, initialDelay time.Duration, op func() error) error {
	var err error
	delay := initialDelay

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = op()
		if err == nil {
			return nil
		}

		if attempt == maxAttempts {
			break
		}

		log.Warnf("Attempt %d failed: %v. Retrying in %v", attempt, err, delay)

		select {
		case <-time.After(delay):
			delay *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return fmt.Errorf("after %d attempts, last error: %w", maxAttempts, err)
}

func validateCrossSeedConfig(cfg *Config) error {
	if cfg.CrossSeedURL == "" {
		return errors.New("cross-seed URL is required")
	}
	if _, err := url.ParseRequestURI(cfg.CrossSeedURL); err != nil {
		return fmt.Errorf("invalid cross-seed URL: %w", err)
	}
	if cfg.CrossSeedAPIKey == "" {
		return errors.New("cross-seed API key is required")
	}
	return nil
}

func validatePushoverConfig(cfg *Config) error {
	if cfg.PushoverUserKey == "" {
		return errors.New("pushover user key is required")
	}
	if cfg.PushoverToken == "" {
		return errors.New("pushover token is required")
	}
	return nil
}

func extractHostname(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	return u.Hostname(), nil
}
