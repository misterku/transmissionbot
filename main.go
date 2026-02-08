package main

import (
	"fmt"
	"os/exec"
	"os"
	"strconv"
	"net/url"
	"context"
	"strings"

	"github.com/caarlos0/env/v11"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	transmissionrpc "github.com/hekmon/transmissionrpc/v3"
)

func getTemperature() (string, error) {
	fmt.Println("Getting temperature...")
	cmd := exec.Command("cat", "/sys/class/thermal/thermal_zone0/temp")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(string(output))
	temperature, err := strconv.Atoi(out)
	if err != nil {
		return "", err
	}
	temperature = temperature / 1000
	return strconv.Itoa(temperature), nil
}

func sendTemperature(bot *tgbotapi.BotAPI, chatID int64) error {
	temperature, err := getTemperature()
	if err != nil {
		return err
	}
	fmt.Printf("Temperature: %s\n", temperature)

	msg := tgbotapi.NewMessage(chatID, temperature)
	_, err = bot.Send(msg)
	return err
}

func newTransmissionClient(username string, password string, host string) *transmissionrpc.Client {
	endpoint, err := url.Parse("http://" + username + ":" + password + "@" + host + ":9091/transmission/rpc")
	if err != nil {
		panic(err)
	}
	tbt, err := transmissionrpc.New(endpoint, nil)
	if err != nil {
		panic(err)
	}
	return tbt
}

type Config struct {
	Token string `env:"TELEGRAM_TOKEN,required"`
	Username string `env:"TRANSMISSION_USERNAME" default:"transmission"`
	Password string `env:"TRANSMISSION_PASSWORD,required"`
	Host string `env:"TRANSMISSION_HOST" default:"127.0.0.1"`
}

func parseConfig() Config {
	var cfg Config
	err := env.Parse(&cfg)
	if err != nil {
		panic(err)
	}
	return cfg
} 

func invalidUid(uid int64) bool {
	return uid != 68898121
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
	
	transmissionClient := newTransmissionClient(cfg.Username, cfg.Password, cfg.Host)
	ok, serverVersion, serverMinimumVersion, err := transmissionClient.RPCVersion(context.TODO())
	if err != nil {
		panic(err)
	}
	if !ok {
		panic(fmt.Sprintf("Remote transmission RPC version (v%d) is incompatible with the transmission library (v%d): remote needs at least v%d",
			serverVersion, transmissionrpc.RPCVersion, serverMinimumVersion))
	}

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

func sendFileToTransmission(client *transmissionrpc.Client, fileID string, chatID int64) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	filename:= dir + "/downloads/" + fileID + ".torrent"
	_, err = client.TorrentAddFile(context.TODO(), filename)
	if err != nil {
		return err
	}
	return nil
}


func main() {
	state := NewAppState()

	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60

	updates := state.Bot.GetUpdatesChan(updateConfig)

	for update := range updates {
		if update.Message == nil {
			fmt.Printf("Invalid update (w/o message): %v", update)
			continue
		}

		if invalidUid(update.Message.From.ID) {
			fmt.Errorf("Invalid UID: %v", update.Message.From.ID)
			continue
		}

		if update.Message.Document != nil {
			if update.Message.Document.MimeType == "application/x-bittorrent" {
				err := downloadFile(state.Bot, update.Message.Document.FileID, update.Message.Chat.ID, state.Cfg)
				if err != nil {
					fmt.Errorf("Error downloading file: %v", err)
					state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error downloading file"))
					continue
				}
				err = sendFileToTransmission(state.Client, update.Message.Document.FileID, update.Message.Chat.ID)
				if err != nil {
					fmt.Errorf("Error sending file to transmission: %v", err)
					state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error sending file to transmission"))
					continue
				}
				state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "File sent to transmission"))
			}
		}

		if update.Message.Text == "/temp" {
			err := sendTemperature(state.Bot, update.Message.Chat.ID)
			if err != nil {
				fmt.Errorf("Error sending temperature: %v", err)
				state.Bot.Send(tgbotapi.NewMessage(update.Message.Chat.ID, "Error sending temperature"))
			}
			continue
		}

		fmt.Println(update)
	}
}
