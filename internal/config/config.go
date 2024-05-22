package config

import (
	"net/http"
	"os"
	"sync"

	"context"

	"gopkg.in/yaml.v2"
)

type ContextKey string

type Config struct {
	Services   []Service `yaml:"services"`
	Log        Log       `yaml:"log"`
	PidFileDir string    `yaml:"pid_file_dir" default:"/tmp/"`
	LogFileDir string    `yaml:"log_file_dir"`
	WorkDir    string    `yaml:"work_dir" default:"/tmp/"`
	Metrics    Metrics   `yaml:"metrics"`
}

type Log struct {
	FileDir string `yaml:"file_dir" default:"/dev/null"`
}

type Metrics struct {
	Port string `yaml:"port" default:"8080"` // default set in main.go
}

type Database struct {
	URL      string `yaml:"url" default:"localhost"`
	Port     int    `yaml:"port" default:"6379"`
	User     string `yaml:"user" default:""`
	Password string `yaml:"password" default:""`
	Database int    `yaml:"database" default:"0"`
	Enabled  bool   `yaml:"enabled" default:"true"`
}

type Service struct {
	SleepInterval  int      `yaml:"sleep_interval" default:"2"`
	Name           string   `yaml:"name"`
	Plugin         string   `yaml:"plugin"`
	Token          string   `yaml:"token"`
	Registration   string   `yaml:"registration" default:"repo"`
	Repo           string   `yaml:"repo"`
	Owner          string   `yaml:"owner"`
	Database       Database `yaml:"database"`
	RegistryURL    string   `yaml:"registry_url"`
	PrivateKey     string   `yaml:"private_key"`
	AppID          int      `yaml:"app_id"`
	InstallationID int64    `yaml:"installation_id"`
}

func LoadConfig(configPath string) (*Config, error) {
	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	var config Config
	err = decoder.Decode(&config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func GetServiceFromContext(ctx context.Context) Service {
	service, ok := ctx.Value(ContextKey("service")).(Service)
	if !ok {
		panic("GetServiceFromContext failed")
	}
	return service
}

type Globals struct {
	RunOnce     string
	PullLock    *sync.Mutex
	PluginsPath string
}

func GetGlobalsFromContext(ctx context.Context) Globals {
	globals, ok := ctx.Value(ContextKey("globals")).(Globals)
	if !ok {
		panic("GetGlobalsFromContext failed")
	}
	return globals
}

func GetHttpTransportFromContext(ctx context.Context) *http.Transport {
	httpTransport, ok := ctx.Value(ContextKey("httpTransport")).(*http.Transport)
	if !ok {
		panic("GetHttpTransportFromContext failed")
	}
	return httpTransport
}
