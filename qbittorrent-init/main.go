package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	defaultWebPort    = "8080"
	defaultBtPort     = "6881"
	defaultConfigPath = "/config/qBittorrent/qBittorrent.conf"
	defaultLogPath    = "/config/qBittorrent/logs/qbittorrent.log"
)

var (
	version = "dev"
	commit  = ""
	date    = ""
	logger  *slog.Logger
)

type ConfigTemplateData struct {
	PortRangeMin         string
	UPnP                 string
	Locale               string
	UseRandomPort        string
	WebUIAddress         string
	CSRFProtection       string
	HostHeaderValidation string
	LocalHostAuth        string
	ServerDomains        string
	WebUIUseUPnP         string
}

const defaultConfigTemplate = `[AutoRun]
enabled=false
program=

[LegalNotice]
Accepted=true

[BitTorrent]
Session\AsyncIOThreadsCount=10
Session\DiskCacheSize=-1
Session\DiskIOReadMode=DisableOSCache
Session\DiskIOType=SimplePreadPwrite
Session\DiskIOWriteMode=EnableOSCache
Session\DiskQueueSize=4194304
Session\FilePoolSize=40
Session\HashingThreadsCount=2
Session\ResumeDataStorageType=SQLite
Session\UseOSCache=true

[Preferences]
Connection\PortRangeMin={{ .PortRangeMin }}
Connection\UPnP={{ .UPnP }}
General\Locale={{ .Locale }}
General\UseRandomPort={{ .UseRandomPort }}
WebUI\Address={{ .WebUIAddress }}
WebUI\CSRFProtection={{ .CSRFProtection }}
WebUI\HostHeaderValidation={{ .HostHeaderValidation }}
WebUI\LocalHostAuth={{ .LocalHostAuth }}
WebUI\ServerDomains={{ .ServerDomains }}
WebUI\UseUPnP={{ .WebUIUseUPnP }}
`

func main() {
	configureLogger()
	logger.Info("Starting qBittorrent initializer",
		"version", version,
		"commit", commit,
		"date", date)

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := setupEnvironment(); err != nil {
		logger.Error("Failed to setup environment", "error", err)
		os.Exit(1)
	}

	if err := initializeConfig(); err != nil {
		logger.Error("Configuration initialization failed", "error", err)
		os.Exit(1)
	}

	if err := runQBittorrent(ctx); err != nil {
		logger.Error("qBittorrent process failed", "error", err)
		os.Exit(1)
	}

	logger.Info("qBittorrent initializer completed successfully")
}

func configureLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: false,
	}).WithAttrs([]slog.Attr{
		slog.String("service", "qbittorrent-init"),
	})
	logger = slog.New(handler)
}

func setupEnvironment() error {
	setPort("QBT_WEBUI_PORT", "QBITTORRENT__PORT", defaultWebPort)
	setPort("QBT_TORRENTING_PORT", "QBITTORRENT__BT_PORT", defaultBtPort)

	if os.Getenv("QBT_WEBUI_PORT") == "" {
		return fmt.Errorf("WEBUI_PORT must be set")
	}
	if os.Getenv("QBT_TORRENTING_PORT") == "" {
		return fmt.Errorf("TORRENTING_PORT must be set")
	}
	return nil
}

func setPort(targetVar, sourceVar, defaultValue string) {
	if val := os.Getenv(sourceVar); val != "" {
		os.Setenv(targetVar, val)
	} else {
		os.Setenv(targetVar, defaultValue)
		logger.Warn("Using default port", "variable", targetVar, "value", defaultValue)
	}
}

func initializeConfig() error {
	if err := ensureConfigFile(defaultConfigPath); err != nil {
		return fmt.Errorf("config file setup failed: %w", err)
	}
	if err := ensureLogSymlink(defaultLogPath); err != nil {
		return fmt.Errorf("log setup failed: %w", err)
	}
	return nil
}

func ensureConfigFile(configPath string) error {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		logger.Info("Configuration file does not exist, writing templated configuration", "path", configPath)

		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}

		tmpl, err := template.New("config").Parse(defaultConfigTemplate)
		if err != nil {
			return fmt.Errorf("failed to parse config template: %w", err)
		}

		data := ConfigTemplateData{
			PortRangeMin:         getEnv("QBT_TORRENTING_PORT", "6881"),
			UPnP:                 getEnv("QBT_UPNP", "false"),
			Locale:               getEnv("QBT_LOCALE", "en"),
			UseRandomPort:        getEnv("QBT_USE_RANDOM_PORT", "false"),
			WebUIAddress:         getEnv("QBT_WEBUI_ADDRESS", "*"),
			CSRFProtection:       getEnv("QBT_CSRF_PROTECTION", "false"),
			HostHeaderValidation: getEnv("QBT_HOST_HEADER_VALIDATION", "false"),
			LocalHostAuth:        getEnv("QBT_LOCALHOST_AUTH", "false"),
			ServerDomains:        getEnv("QBT_SERVER_DOMAINS", "*"),
			WebUIUseUPnP:         getEnv("QBT_WEBUI_USE_UPNP", "false"),
		}

		var rendered bytes.Buffer
		if err := tmpl.Execute(&rendered, data); err != nil {
			return fmt.Errorf("failed to render config template: %w", err)
		}

		if err := os.WriteFile(configPath, rendered.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write config file: %w", err)
		}

		logger.Info("Templated configuration written successfully")
	} else if err != nil {
		return fmt.Errorf("failed to check config file: %w", err)
	} else {
		logger.Info("Configuration file already exists, skipping write", "path", configPath)
	}
	return nil
}

func getEnv(key, fallback string) string {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}
	return val
}

func ensureLogSymlink(logPath string) error {
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		logger.Info("Initializing log symlink", "path", logPath)

		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}

		if err := os.Symlink("/dev/stdout", logPath); err != nil {
			return fmt.Errorf("failed to create log symlink: %w", err)
		}

		logger.Info("Log symlink created successfully")
	}
	return nil
}

func runQBittorrent(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/qbittorrent-nox", os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logger.Info("Starting qBittorrent process", "command", cmd.String())

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return fmt.Errorf("process exited unexpectedly: %w", err)
	case <-ctx.Done():
		logger.Info("Received termination signal, shutting down")
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

		select {
		case <-time.After(30 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return fmt.Errorf("forced shutdown after timeout")
		case err := <-done:
			return err
		}
	}
}
