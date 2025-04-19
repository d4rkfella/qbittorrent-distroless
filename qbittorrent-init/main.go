package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultConfigPath = "/config/qBittorrent/qBittorrent.conf"
	defaultLogPath    = "/config/qBittorrent/logs/qbittorrent.log"
)

type allowedOption struct {
	expectsValue  bool
	allowedValues map[string]bool
	validator     func(string) bool
}

var allowedOptions = map[string]allowedOption{
	"-h":                     {expectsValue: false},
	"--help":                 {expectsValue: false},
	"-v":                     {expectsValue: false},
	"--version":              {expectsValue: false},
	"--confirm-legal-notice": {expectsValue: false},

	"--webui-port": {
		expectsValue: true,
		validator:    isValidPort,
	},
	"--torrenting-port": {
		expectsValue: true,
		validator:    isValidPort,
	},

	"-d":       {expectsValue: false},
	"--daemon": {expectsValue: false},

	"--profile":             {expectsValue: true, validator: isValidPath},
	"--configuration":       {expectsValue: true},
	"--relative-fastresume": {expectsValue: false},

	"--save-path": {expectsValue: true, validator: isValidPath},
	"--add-stopped": {
		expectsValue:  true,
		allowedValues: map[string]bool{"true": true, "false": true},
	},
	"--skip-hash-check": {expectsValue: false},
	"--category":        {expectsValue: true},
	"--sequential":      {expectsValue: false},
	"--first-and-last":  {expectsValue: false},
	"--skip-dialog": {
		expectsValue:  true,
		allowedValues: map[string]bool{"true": true, "false": true},
	},
}

var (
	version = "dev"
	commit  = ""
	date    = ""
	log     *slog.Logger
)

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
Session\Port=50413
Session\ResumeDataStorageType=SQLite
Session\UseOSCache=true

[Preferences]
Connection\PortRangeMin=6881
Connection\UPnP=false
General\Locale=en
General\UseRandomPort=false
WebUI\Address=*
WebUI\CSRFProtection=false
WebUI\HostHeaderValidation=false
WebUI\LocalHostAuth=false
WebUI\Port=8080
WebUI\ServerDomains=*
WebUI\UseUPnP=false
`

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

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("Starting qBittorrent initializer",
		"version", version,
		"commit", commit,
		"date", date)

	if err := initializeConfig(); err != nil {
		log.Error("Configuration initialization failed", "error", err)
		os.Exit(1)
	}

	if err := runQBittorrent(ctx); err != nil {
		log.Error("qBittorrent process failed", "error", err)
		os.Exit(1)
	}

	log.Info("qBittorrent initializer completed successfully")
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
		slog.String("service", "qbittorrent-init"),
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
		log.Info("Configuration file does not exist, writing default configuration", "path", configPath)

		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}

		if err := os.WriteFile(configPath, []byte(defaultConfigTemplate), 0644); err != nil {
			return fmt.Errorf("failed to write config file: %w", err)
		}

		log.Info("Default configuration written successfully")
	} else if err != nil {
		return fmt.Errorf("failed to check config file: %w", err)
	} else {
		log.Info("Configuration file already exists, skipping write", "path", configPath)
	}
	return nil
}

func ensureLogSymlink(logPath string) error {
	err := os.Symlink("/dev/stdout", logPath)
	switch {
	case err == nil:
		log.Info("Log symlink created successfully", "path", logPath)
		return nil
	case os.IsExist(err):
		log.Debug("Log symlink already exists", "path", logPath)
		return nil
	default:
		if os.IsNotExist(err) {
			log.Info("Creating log directory", "path", filepath.Dir(logPath))
			if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
				return fmt.Errorf("failed to create log directory: %w", err)
			}
			if err := os.Symlink("/dev/stdout", logPath); err != nil {
				return fmt.Errorf("failed to create log symlink after retry: %w", err)
			}
			log.Info("Log symlink created after directory creation", "path", logPath)
			return nil
		}
		return fmt.Errorf("unexpected error creating symlink: %w", err)
	}
}

func sanitizeArgs(args []string) []string {
	var sanitized []string
	i := 0

	for i < len(args) {
		arg := args[i]
		originalArg := arg

		switch arg {
		case "-h":
			arg = "--help"
		case "-v":
			arg = "--version"
		case "-d":
			arg = "--daemon"
		}

		if arg == "--" {
			sanitized = append(sanitized, args[i:]...)
			break
		}

		opt, exists := allowedOptions[arg]
		if !exists || !strings.HasPrefix(arg, "-") {
			sanitized = append(sanitized, originalArg)
			i++
			continue
		}

		if !opt.expectsValue {
			sanitized = append(sanitized, arg)
			i++
			continue
		}

		var value string
		if strings.Contains(arg, "=") {
			parts := strings.SplitN(arg, "=", 2)
			arg = parts[0]
			value = parts[1]
		} else if i+1 < len(args) {
			value = args[i+1]
			i++
		}

		valid := true
		switch {
		case opt.validator != nil:
			valid = opt.validator(value)
		case len(opt.allowedValues) > 0:
			valid = opt.allowedValues[strings.ToLower(value)]
		}

		if valid {
			sanitized = append(sanitized, arg)
			if value != "" {
				sanitized = append(sanitized, value)
			}
		} else {
			log.Warn("Skipping invalid value for option",
				"option", arg, "value", value)
		}

		i++
	}

	return sanitized
}

func isValidPort(port string) bool {
	p, err := strconv.Atoi(port)
	return err == nil && p > 0 && p <= 65535
}

func isValidPath(path string) bool {
	return !strings.Contains(path, "..") &&
		!strings.HasPrefix(path, "/") &&
		!strings.Contains(path, "$")
}

func runQBittorrent(ctx context.Context) error {
	safeArgs := sanitizeArgs(os.Args[1:])
	cmd := exec.CommandContext(ctx, "/usr/bin/qbittorrent-nox", safeArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	log.Info("Starting qBittorrent process", "command", cmd.String())

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
		log.Info("Received termination signal, shutting down")
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
