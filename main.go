package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/hekmon/transmissionrpc/v3"
	"github.com/spf13/viper"
)

type Config struct {
    Token       string `mapstructure:"telegram_token"`
    AllowedUIDs []int64 `mapstructure:"allowed_uids"`
    Transmission struct {
        Username string `mapstructure:"username"`
        Password string `mapstructure:"password"`
        Host     string `mapstructure:"host"`
        Port     int    `mapstructure:"port"`
        Scheme   string `mapstructure:"scheme"`
    } `mapstructure:"transmission"`
}


func parseConfig() Config {
	v := viper.New()

	// Set defaults
	v.SetDefault("telegram_token", "")
	v.SetDefault("allowed_uids", []int64{})
	v.SetDefault("transmission.username", "transmission")
	v.SetDefault("transmission.password", "")
	v.SetDefault("transmission.host", "127.0.0.1")
	v.SetDefault("transmission.port", 9091)

	// Config file
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/transmissionbot/")

	// Environment variables
	v.AutomaticEnv()

	// Read config
	if err := v.ReadInConfig(); err != nil {
		if !strings.Contains(err.Error(), "Config File Not Found") {
			panic(err)
		}
		// Config file not found; ignore, we'll rely on defaults/env
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		panic(err)
	}

	// Validate required fields
	if cfg.Token == "" {
		panic("telegram_token is required")
	}
	if cfg.Transmission.Password == "" {
		panic("transmission_password is required")
	}
	if cfg.Transmission.Scheme == "" {
		panic("transmission_scheme is required")
	}

	return cfg
}

func invalidUid(uid int64, allowed []int64) bool {
	for _, allowedUID := range allowed {
		if uid == allowedUID {
			return false
		}
	}
	return true
}

type appState struct {
	Bot *tgbotapi.BotAPI
	Client *transmissionrpc.Client
	Cfg Config
}

func NewAppState() *appState {
	os.MkdirAll("./downloads", 0755)
	cfg := parseConfig()
	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		panic(err)
	}
	bot.Debug = true
	
	transmissionClient := NewTransmissionClient(cfg.Transmission.Username, cfg.Transmission.Password, cfg.Transmission.Host, cfg.Transmission.Scheme, cfg.Transmission.Port)
	return &appState{bot, transmissionClient, cfg}
}

func downloadFile(bot *tgbotapi.BotAPI, fileID string, chatID int64, cfg Config) error {
	file, err := bot.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return err
	}

	url := file.Link(cfg.Token)

	cmd := exec.Command("curl", url, "-o", "./downloads/" + fileID + ".torrent")
	err = cmd.Run()
	if err != nil {
		return err
	}
	return nil
}

func main() {
	// Setup logger
	logLevel := slog.LevelDebug
	if level := os.Getenv("LOG_LEVEL"); level == "info" {
		logLevel = slog.LevelInfo
	} else if level == "warn" {
		logLevel = slog.LevelWarn
	} else if level == "error" {
		logLevel = slog.LevelError
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})
	slog.SetDefault(slog.New(handler))

	state := NewAppState()

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60

	updates := state.Bot.GetUpdatesChan(updateConfig)

	for update := range updates {
		if update.Message == nil {
			slog.Debug("Invalid update (w/o message)", "update", update)
			continue
		}

		if invalidUid(update.Message.From.ID, state.Cfg.AllowedUIDs) {
			slog.Warn("Invalid UID", "uid", update.Message.From.ID)
			continue
		}

		if update.Message.Document != nil {
			if update.Message.Document.MimeType == "application/x-bittorrent" {
				err := downloadFile(state.Bot, update.Message.Document.FileID, update.Message.Chat.ID, state.Cfg)
				if err != nil {
					slog.Error("Error downloading file", "error", err)
					state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error downloading file"))
					continue
				}
				err = SendFileToTransmission(state.Client, update.Message.Document.FileID, update.Message.Chat.ID)
				if err != nil {
					slog.Error("Error sending file to transmission", "error", err)
					state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error sending file to transmission"))
					continue
				}
				state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "File sent to transmission"))
			}
		}

		if update.Message.Text == "/cleanup" {
			cnt, err := Cleanup(context.TODO(), state.Client)
			if err != nil {
				slog.Error("Error sending cleanup", "error", err)
				state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Error sending cleanup: %v", err)))
			}
			var message string
			if cnt == 0 {
				message = "No active torrents"
			} else {
				message = fmt.Sprintf("Active torrents removed: %d", cnt)
			}
			state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, message))
			continue
		}

		if update.Message.Text == "/status" {
			active, downloaded, err := StatusOfActiveTorrents(context.TODO(), state.Client)
			if err != nil {
				slog.Error("Error sending status", "error", err)
				state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, fmt.Sprintf("Error sending status: %v", err)))
				continue
			}
			var message string
			if active == 0 {
				message = "No active torrents."
			} else {
				message = fmt.Sprintf("Number of active torrents: %d.", active)
			}
			if downloaded == 0 {
				message += " No finished torrents."
			} else {
				message += fmt.Sprintf(" Number of finished torrents: %d.", downloaded)
			}
			state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, message))
			continue
		}

		slog.Debug("Update", "update", update)
	}
}
