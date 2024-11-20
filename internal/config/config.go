package config

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"context"

	"gopkg.in/yaml.v2"
)

type ContextKey string

type Config struct {
	Plugins                []Plugin `yaml:"plugins"`
	Log                    Log      `yaml:"log"`
	PidFileDir             string   `yaml:"pid_file_dir"`
	LogFileDir             string   `yaml:"log_file_dir"`
	WorkDir                string   `yaml:"work_dir"`
	Metrics                Metrics  `yaml:"metrics"`
	GlobalPrivateKey       string   `yaml:"global_private_key"`
	PluginsPath            string   `yaml:"plugins_path"`
	GlobalDatabaseURL      string   `yaml:"global_database_url"`
	GlobalDatabasePort     int      `yaml:"global_database_port"`
	GlobalDatabaseUser     string   `yaml:"global_database_user"`
	GlobalDatabasePassword string   `yaml:"global_database_password"`
	GlobalDatabaseDatabase int      `yaml:"global_database_database"`
	GlobalReceiverSecret   string   `yaml:"global_receiver_secret"`
}

type Log struct {
	FileDir string `yaml:"file_dir"`
}

type Metrics struct {
	Aggregator    bool     `yaml:"aggregator"`
	Port          string   `yaml:"port"`
	MetricsURLs   []string `yaml:"metrics_urls"`
	SleepInterval int      `yaml:"sleep_interval"`
	Database      Database `yaml:"database"`
}

type Database struct {
	URL      string `yaml:"url"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database int    `yaml:"database"`
}

type Workflow struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

type Plugin struct {
	SleepInterval  int      `yaml:"sleep_interval"`
	Name           string   `yaml:"name"`
	Plugin         string   `yaml:"plugin"`
	Token          string   `yaml:"token"`
	Repo           string   `yaml:"repo"`
	Owner          string   `yaml:"owner"`
	Database       Database `yaml:"database"`
	RegistryURL    string   `yaml:"registry_url"`
	PrivateKey     string   `yaml:"private_key"`
	AppID          int      `yaml:"app_id"`
	InstallationID int64    `yaml:"installation_id"`
	Workflows      Workflow `yaml:"workflows"`
	Port           string   `yaml:"port"`
	Secret         string   `yaml:"secret"`
	HookID         int64    `yaml:"hook_id"`
	SkipRedeliver  bool     `yaml:"skip_redeliver"`
	RunnerGroup    string   `yaml:"runner_group"`
	RedeliverHours int      `yaml:"redeliver_hours"`
}

func LoadConfig(configPath string) (Config, error) {
	config := Config{}
	file, err := os.Open(configPath)
	if err != nil {
		return config, err
	}
	defer file.Close()

	decoder := yaml.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		return config, err
	}
	return config, nil
}

func LoadInEnvs(config Config) (Config, error) {
	/////////////////////////////////
	// AGGREGATOR ///////////////////
	envAggregator := os.Getenv("ANKLET_METRICS_AGGREGATOR")
	if envAggregator != "" {
		config.Metrics.Aggregator = envAggregator == "true"
	}
	envPort := os.Getenv("ANKLET_METRICS_PORT")
	if envPort != "" {
		config.Metrics.Port = envPort
	}
	envMetricsURLs := os.Getenv("ANKLET_METRICS_URLS")
	if envMetricsURLs != "" {
		config.Metrics.MetricsURLs = strings.Split(envMetricsURLs, ",")
	}
	envSleepInterval := os.Getenv("ANKLET_METRICS_SLEEP_INTERVAL")
	if envSleepInterval != "" {
		value, err := strconv.Atoi(envSleepInterval)
		if err != nil {
			return Config{}, err
		}
		config.Metrics.SleepInterval = value
	}
	envDBUser := os.Getenv("ANKLET_METRICS_DATABASE_USER")
	if envDBUser != "" {
		config.Metrics.Database.User = envDBUser
	}
	envDBPassword := os.Getenv("ANKLET_METRICS_DATABASE_PASSWORD")
	if envDBPassword != "" {
		config.Metrics.Database.Password = envDBPassword
	}
	envDBURL := os.Getenv("ANKLET_METRICS_DATABASE_URL")
	if envDBURL != "" {
		config.Metrics.Database.URL = envDBURL
	}
	envDBPort := os.Getenv("ANKLET_METRICS_DATABASE_PORT")
	if envDBPort != "" {
		port, err := strconv.Atoi(envDBPort)
		if err != nil {
			return Config{}, err
		}
		config.Metrics.Database.Port = port
	}
	envDBDatabase := os.Getenv("ANKLET_METRICS_DATABASE_DATABASE")
	if envDBDatabase != "" {
		database, err := strconv.Atoi(envDBDatabase)
		if err != nil {
			return Config{}, err
		}
		config.Metrics.Database.Database = database
	}
	///////////////////////////
	// Other //////////////////
	workDir := os.Getenv("ANKLET_WORK_DIR")
	if workDir != "" {
		config.WorkDir = workDir
	}
	envGlobalPrivateKey := os.Getenv("ANKLET_GLOBAL_PRIVATE_KEY")
	if envGlobalPrivateKey != "" {
		config.GlobalPrivateKey = envGlobalPrivateKey
	}

	envGlobalDatabaseURL := os.Getenv("ANKLET_GLOBAL_DATABASE_URL")
	if envGlobalDatabaseURL != "" {
		config.GlobalDatabaseURL = envGlobalDatabaseURL
	}
	envGlobalDatabasePort := os.Getenv("ANKLET_GLOBAL_DATABASE_PORT")
	if envGlobalDatabasePort != "" {
		port, err := strconv.Atoi(envGlobalDatabasePort)
		if err != nil {
			return Config{}, err
		}
		config.GlobalDatabasePort = port
	}
	envGlobalDatabaseUser := os.Getenv("ANKLET_GLOBAL_DATABASE_USER")
	if envGlobalDatabaseUser != "" {
		config.GlobalDatabaseUser = envGlobalDatabaseUser
	}
	envGlobalDatabasePassword := os.Getenv("ANKLET_GLOBAL_DATABASE_PASSWORD")
	if envGlobalDatabasePassword != "" {
		config.GlobalDatabasePassword = envGlobalDatabasePassword
	}

	envGlobalReceiverSecret := os.Getenv("ANKLET_GLOBAL_RECEIVER_SECRET")
	if envGlobalReceiverSecret != "" {
		config.GlobalReceiverSecret = envGlobalReceiverSecret
	}

	// pidFileDir := os.Getenv("ANKLET_PID_FILE_DIR")
	// if pidFileDir != "" {
	// 	config.PidFileDir = pidFileDir
	// }
	// logFileDir := os.Getenv("ANKLET_LOG_FILE_DIR")
	// if logFileDir != "" {
	// 	config.Log.FileDir = logFileDir
	// }
	return config, nil
}

func GetPluginFromContext(ctx context.Context) Plugin {
	plugin, ok := ctx.Value(ContextKey("plugin")).(Plugin)
	if !ok {
		panic("GetPluginFromContext failed")
	}
	return plugin
}

type Globals struct {
	RunOnce      string
	PullLock     *sync.Mutex
	PluginsPath  string
	DebugEnabled bool
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

func GetLoadedConfigFromContext(ctx context.Context) *Config {
	config, ok := ctx.Value(ContextKey("config")).(*Config)
	if !ok {
		panic("GetLoadedConfigFromContext failed")
	}
	return config
}

func GetIsRepoSetFromContext(ctx context.Context) bool {
	isRepoSet, ok := ctx.Value(ContextKey("isRepoSet")).(bool)
	if !ok {
		panic("GetIsRepoSetFromContext failed")
	}
	return isRepoSet
}

func GetConfigFileNameFromContext(ctx context.Context) string {
	configFileName, ok := ctx.Value(ContextKey("configFileName")).(string)
	if !ok {
		panic("GetConfigFileNameFromContext failed")
	}
	return configFileName
}
