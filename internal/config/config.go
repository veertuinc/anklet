package config

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

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
	FileDir       string `yaml:"file_dir"`
	SplitByPlugin bool   `yaml:"split_by_plugin"`
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
	SleepInterval              int      `yaml:"sleep_interval"`
	Name                       string   `yaml:"name"`
	Plugin                     string   `yaml:"plugin"`
	Token                      string   `yaml:"token"`
	Repo                       string   `yaml:"repo"`
	Owner                      string   `yaml:"owner"`
	Database                   Database `yaml:"database"`
	RegistryURL                string   `yaml:"registry_url"`
	SkipPull                   bool     `yaml:"skip_pull"`
	PrivateKey                 string   `yaml:"private_key"`
	AppID                      int64    `yaml:"app_id"`
	InstallationID             int64    `yaml:"installation_id"`
	Workflows                  Workflow `yaml:"workflows"`
	Port                       string   `yaml:"port"`
	Secret                     string   `yaml:"secret"`
	HookID                     int64    `yaml:"hook_id"`
	SkipRedeliver              bool     `yaml:"skip_redeliver"`
	RunnerGroup                string   `yaml:"runner_group"`
	RedeliverHours             int      `yaml:"redeliver_hours"`
	RegistrationTimeoutSeconds int      `yaml:"registration_timeout_seconds"`
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
	// envMetricsURLs := os.Getenv("ANKLET_METRICS_URLS")
	// if envMetricsURLs != "" {
	// 	config.Metrics.MetricsURLs = strings.Split(envMetricsURLs, ",")
	// }
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

func GetPluginFromContext(ctx context.Context) (Plugin, error) {
	plugin, ok := ctx.Value(ContextKey("plugin")).(Plugin)
	if !ok {
		return Plugin{}, fmt.Errorf("GetPluginFromContext failed")
	}
	return plugin, nil
}

type Globals struct {
	RunPluginsOnce bool
	// block the second plugin until the first plugin is done
	FirstPluginStarted   chan bool
	ReturnAllToMainQueue chan bool
	PullLock             *sync.Mutex
	PluginsPath          string
	DebugEnabled         bool
	PluginsPaused        atomic.Bool
	// block other plugins from running until the currently running
	// plugin is at a place that's safe to let other run
	APluginIsPreparing atomic.Value
	HostCPUCount       int
	HostMemoryBytes    uint64
	QueueTargetIndex   int64
	// We want each plugin to run at least once so that any VMs/jobs that were orphaned
	// on this host get a chance to be cleaned or continue where they left off
	FinishedInitialRunOfEachPlugin []bool
	PluginList                     []string
	// Shared run count across all plugins
	PluginRunCount atomic.Uint64
}

// // Returns true if it's the given plugin's turn to acquire the prep lock
// func (g *Globals) IsMyTurnForPrepLock(pluginName string) bool {
// 	fmt.Println("IsMyTurnForPrepLock", pluginName, g.CurrentPluginIndex, g.PluginOrder, g.PluginOrder[g.CurrentPluginIndex] == pluginName)
// 	g.PrepLockMu.Lock()
// 	defer g.PrepLockMu.Unlock()
// 	if len(g.PluginOrder) == 0 {
// 		return true // fallback: allow anyone
// 	}
// 	return g.PluginOrder[g.CurrentPluginIndex] == pluginName
// }

// // Advances to the next plugin in the round-robin order
// func (g *Globals) NextPluginForPrepLock() {
// 	g.PrepLockMu.Lock()
// 	defer g.PrepLockMu.Unlock()
// 	if len(g.PluginOrder) == 0 {
// 		return
// 	}
// 	fmt.Println("NextPluginForPrepLock before", g.CurrentPluginIndex, g.PluginOrder)
// 	g.CurrentPluginIndex = (g.CurrentPluginIndex + 1) % len(g.PluginOrder)
// 	fmt.Println("NextPluginForPrepLock after", g.CurrentPluginIndex, g.PluginOrder)
// }

func GetWorkerGlobalsFromContext(ctx context.Context) (*Globals, error) {
	globals, ok := ctx.Value(ContextKey("globals")).(*Globals)
	if !ok {
		return nil, fmt.Errorf("GetGlobalsFromContext failed")
	}
	return globals, nil
}

func (g *Globals) PausePlugins() {
	g.PluginsPaused.Store(true)
}

func (g *Globals) UnPausePlugins() {
	g.PluginsPaused.Store(false)
}

func (g *Globals) ArePluginsPaused() bool {
	return g.PluginsPaused.Load()
}

func (g *Globals) SetAPluginIsPreparing(pluginName string) {
	fmt.Println("SetAPluginIsPreparing", pluginName)
	g.APluginIsPreparing.Store(pluginName)
}

func (g *Globals) UnsetAPluginIsPreparing() {
	fmt.Println("UnsetAPluginIsPreparing")
	g.APluginIsPreparing.Store("")
}

func (g *Globals) IsAPluginPreparingState() string {
	pluginName := g.APluginIsPreparing.Load()
	if pluginName == nil {
		return ""
	}
	return pluginName.(string)
}

func (g *Globals) IncrementQueueTargetIndex() {
	g.QueueTargetIndex++
}

func (g *Globals) DecrementQueueTargetIndex() {
	if g.QueueTargetIndex > 0 {
		g.QueueTargetIndex--
	}
}

func (g *Globals) ResetQueueTargetIndex() {
	g.QueueTargetIndex = 0
}

func GetLoadedConfigFromContext(ctx context.Context) (*Config, error) {
	config, ok := ctx.Value(ContextKey("config")).(*Config)
	if !ok {
		return nil, fmt.Errorf("GetLoadedConfigFromContext failed")
	}
	return config, nil
}

func GetIsRepoSetFromContext(ctx context.Context) (bool, error) {
	isRepoSet, ok := ctx.Value(ContextKey("isRepoSet")).(bool)
	if !ok {
		return false, fmt.Errorf("GetIsRepoSetFromContext failed")
	}
	return isRepoSet, nil
}

func GetConfigFileNameFromContext(ctx context.Context) (string, error) {
	configFileName, ok := ctx.Value(ContextKey("configFileName")).(string)
	if !ok {
		return "", fmt.Errorf("GetConfigFileNameFromContext failed")
	}
	return configFileName, nil
}

// GetPluginRunCount returns the current value of the shared plugin run counter
func (g *Globals) GetPluginRunCount() uint64 {
	return g.PluginRunCount.Load()
}

// IncrementPluginRunCount increments the shared plugin run counter and returns the new value
func (g *Globals) IncrementPluginRunCount() uint64 {
	return g.PluginRunCount.Add(1)
}

func FindIndex(slice []string, value string) int {
	for i, v := range slice {
		if v == value {
			return i
		}
	}
	return -1
}
