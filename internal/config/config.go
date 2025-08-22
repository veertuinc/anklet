package config

import (
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"context"

	"gopkg.in/yaml.v2"
)

type ContextKey string

type Config struct {
	Plugins                          []Plugin `yaml:"plugins"`
	Log                              Log      `yaml:"log"`
	PidFileDir                       string   `yaml:"pid_file_dir"`
	LogFileDir                       string   `yaml:"log_file_dir"`
	WorkDir                          string   `yaml:"work_dir"`
	Metrics                          Metrics  `yaml:"metrics"`
	GlobalPrivateKey                 string   `yaml:"global_private_key"`
	PluginsPath                      string   `yaml:"plugins_path"`
	GlobalDatabaseURL                string   `yaml:"global_database_url"`
	GlobalDatabasePort               int      `yaml:"global_database_port"`
	GlobalDatabaseUser               string   `yaml:"global_database_user"`
	GlobalDatabasePassword           string   `yaml:"global_database_password"`
	GlobalDatabaseDatabase           int      `yaml:"global_database_database"`
	GlobalDatabaseMaxRetries         int      `yaml:"global_database_max_retries"`
	GlobalDatabaseRetryDelay         int      `yaml:"global_database_retry_delay"`
	GlobalDatabaseRetryBackoffFactor float64  `yaml:"global_database_retry_backoff_factor"`
	GlobalReceiverSecret             string   `yaml:"global_receiver_secret"`
	GlobalTemplateDiskBuffer         float64  `yaml:"global_template_disk_buffer"` // Global disk buffer percentage (e.g., 10.0 for 10%)
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
	URL                string  `yaml:"url"`
	Port               int     `yaml:"port"`
	User               string  `yaml:"user"`
	Password           string  `yaml:"password"`
	Database           int     `yaml:"database"`
	MaxRetries         int     `yaml:"max_retries"`          // Maximum number of retry attempts (default: 3)
	RetryDelay         int     `yaml:"retry_delay"`          // Initial retry delay in milliseconds (default: 1000)
	RetryBackoffFactor float64 `yaml:"retry_backoff_factor"` // Backoff multiplier for retry delay (default: 2.0)
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
	TemplateDiskBuffer         float64  `yaml:"template_disk_buffer"` // Plugin-specific disk buffer percentage (e.g., 10.0 for 10%)
}

func LoadConfig(configPath string) (Config, error) {
	config := Config{}
	file, err := os.Open(configPath)
	if err != nil {
		return config, err
	}
	defer func() {
		err := file.Close()
		if err != nil {
			panic(err)
		}
	}()

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

	envGlobalTemplateDiskBuffer := os.Getenv("ANKLET_GLOBAL_TEMPLATE_DISK_BUFFER")
	if envGlobalTemplateDiskBuffer != "" {
		buffer, err := strconv.ParseFloat(envGlobalTemplateDiskBuffer, 64)
		if err != nil {
			return Config{}, err
		}
		config.GlobalTemplateDiskBuffer = buffer
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

type PluginGlobal struct {
	PluginRunCount     atomic.Uint64
	Preparing          atomic.Bool
	FinishedInitialRun atomic.Bool
	Paused             atomic.Bool
}

// TemplateUsage tracks usage statistics for a template/tag combination
type TemplateUsage struct {
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	Tag          string    `json:"tag"`
	ImageSize    uint64    `json:"image_size"` // Template actual disk usage
	LastAccessed time.Time `json:"last_accessed"`
	UsageCount   uint64    `json:"usage_count"`
	InUse        bool      `json:"in_use"`  // Currently being used by a running VM
	Pulling      bool      `json:"pulling"` // Currently being pulled
}

// TemplateTracker manages template usage across all plugins
type TemplateTracker struct {
	Templates map[string]*TemplateUsage // key: templateUUID
	Mutex     *sync.RWMutex
}

type Globals struct {
	RunPluginsOnce bool
	// block the second plugin until the first plugin is done
	ReturnAllToMainQueue atomic.Bool
	PluginsPath          string
	DebugEnabled         bool
	// block other plugins from running until the currently running
	// plugin is at a place that's safe to let other run
	HostCPUCount     int
	HostMemoryBytes  uint64
	QueueTargetIndex *int64
	// We want each plugin to run at least once so that any VMs/jobs that were orphaned
	// on this host get a chance to be cleaned or continue where they left off
	Plugins         map[string]map[string]*PluginGlobal
	TemplateTracker *TemplateTracker // Track template usage for LRU cleanup
}

// GetPluginRunCount returns the current value of the shared plugin run counter
func (g *Globals) GetPluginRunCount(pluginName string) (uint64, error) {
	for _, nameOfPlugin := range g.Plugins {
		if _, ok := nameOfPlugin[pluginName]; ok {
			return nameOfPlugin[pluginName].PluginRunCount.Load(), nil
		}
	}
	return 0, fmt.Errorf("GetPluginRunCount: plugin not found")
}

// IncrementPluginRunCount increments the shared plugin run counter and returns the new value
func (g *Globals) IncrementPluginRunCount(pluginName string) {
	for _, nameOfPlugin := range g.Plugins {
		if _, ok := nameOfPlugin[pluginName]; ok {
			nameOfPlugin[pluginName].PluginRunCount.Add(1)
		}
	}
}

func GetWorkerGlobalsFromContext(ctx context.Context) (*Globals, error) {
	globals, ok := ctx.Value(ContextKey("globals")).(*Globals)
	if !ok {
		return nil, fmt.Errorf("GetGlobalsFromContext failed")
	}
	return globals, nil
}

func (g *Globals) GetPausedPlugin() string {
	for pluginName, plugin := range g.Plugins {
		for _, pluginSettings := range plugin {
			if pluginSettings.Paused.Load() {
				return pluginName
			}
		}
	}
	return ""
}

func (g *Globals) IncrementQueueTargetIndex() {
	*g.QueueTargetIndex++
}

func (g *Globals) DecrementQueueTargetIndex() {
	if *g.QueueTargetIndex > 0 {
		*g.QueueTargetIndex--
	}
}

func (g *Globals) ResetQueueTargetIndex() {
	*g.QueueTargetIndex = 0
}

// NewTemplateTracker creates a new template tracker
func NewTemplateTracker() *TemplateTracker {
	return &TemplateTracker{
		Templates: make(map[string]*TemplateUsage),
		Mutex:     &sync.RWMutex{},
	}
}

// GetTemplateKey returns the key for a template UUID
func (tt *TemplateTracker) GetTemplateKey(templateUUID string) string {
	return templateUUID
}

// UpdateTemplateUsage updates the usage statistics for a template
func (tt *TemplateTracker) UpdateTemplateUsage(
	templateUUID, templateName, templateTag string,
	sizeBytes uint64,
	lastAccessed time.Time,
) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	key := tt.GetTemplateKey(templateUUID)
	if usage, exists := tt.Templates[key]; exists {
		usage.UUID = templateUUID
		usage.LastAccessed = lastAccessed
		usage.UsageCount++
		if sizeBytes > 0 {
			usage.ImageSize = sizeBytes
		}
	} else {
		tt.Templates[key] = &TemplateUsage{
			UUID:         templateUUID,
			Name:         templateName,
			Tag:          templateTag,
			ImageSize:    sizeBytes,
			LastAccessed: lastAccessed,
			UsageCount:   1,
			InUse:        false,
			Pulling:      false,
		}
	}
}

// SetTemplateInUse marks a template as in use or not in use
func (tt *TemplateTracker) SetTemplateInUse(templateUUID, templateName, templateTag string, inUse bool) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	key := tt.GetTemplateKey(templateUUID)
	if usage, exists := tt.Templates[key]; exists {
		usage.InUse = inUse
	}
}

// SetTemplatePulling marks a template as being pulled or not
func (tt *TemplateTracker) SetTemplatePulling(
	templateUUID, templateName, templateTag string,
	pulling bool,
	lastAccessed time.Time,
) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	key := tt.GetTemplateKey(templateUUID)
	if usage, exists := tt.Templates[key]; exists {
		usage.Pulling = pulling
	} else if pulling {
		// Create entry for template being pulled
		tt.Templates[key] = &TemplateUsage{
			UUID:         templateUUID,
			Name:         templateName,
			Tag:          templateTag,
			ImageSize:    0, // Will be updated after pull
			LastAccessed: lastAccessed,
			UsageCount:   0,
			InUse:        false,
			Pulling:      true,
		}
	}
}

// GetLeastRecentlyUsedTemplates returns templates sorted by usage (LRU first)
func (tt *TemplateTracker) GetLeastRecentlyUsedTemplates() []*TemplateUsage {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	var templates []*TemplateUsage
	for _, usage := range tt.Templates {
		// Don't include templates that are currently in use or being pulled
		if !usage.InUse && !usage.Pulling {
			templates = append(templates, usage)
		}
	}

	// Sort by usage count (ascending), then by last used time (ascending)
	// This prioritizes templates that are used less frequently and haven't been used recently
	for i := 0; i < len(templates)-1; i++ {
		for j := i + 1; j < len(templates); j++ {
			// First sort by usage count
			if templates[i].UsageCount > templates[j].UsageCount {
				templates[i], templates[j] = templates[j], templates[i]
			} else if templates[i].UsageCount == templates[j].UsageCount {
				// If usage count is the same, sort by last used time
				if templates[i].LastAccessed.After(templates[j].LastAccessed) {
					templates[i], templates[j] = templates[j], templates[i]
				}
			}
		}
	}

	return templates
}

// GetTotalTemplateSize returns the total size of all templates
func (tt *TemplateTracker) GetTotalTemplateSize() uint64 {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	var totalSize uint64
	for _, usage := range tt.Templates {
		totalSize += usage.ImageSize
	}
	return totalSize
}

// RemoveTemplate removes a template from tracking
func (tt *TemplateTracker) RemoveTemplate(templateUUID string) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	key := tt.GetTemplateKey(templateUUID)
	delete(tt.Templates, key)
}

// GetTemplateUsage returns the usage info for a specific template
func (tt *TemplateTracker) GetTemplateUsage(templateUUID string) (*TemplateUsage, bool) {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	key := tt.GetTemplateKey(templateUUID)
	usage, exists := tt.Templates[key]
	return usage, exists
}

// HasPullingTemplates returns true if any templates are currently being pulled
func (tt *TemplateTracker) HasPullingTemplates() bool {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	for _, usage := range tt.Templates {
		if usage.Pulling {
			return true
		}
	}
	return false
}

// Contains checks if a template with the given UUID exists in the tracker
func (tt *TemplateTracker) Contains(templateUUID string) bool {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	key := tt.GetTemplateKey(templateUUID)
	_, exists := tt.Templates[key]
	return exists
}

func GetLoadedConfigFromContext(ctx context.Context) (*Config, error) {
	config, ok := ctx.Value(ContextKey("config")).(*Config)
	if !ok {
		return nil, fmt.Errorf("GetLoadedConfigFromContext failed")
	}
	return config, nil
}

func GetConfigFileNameFromContext(ctx context.Context) (string, error) {
	configFileName, ok := ctx.Value(ContextKey("configFileName")).(string)
	if !ok {
		return "", fmt.Errorf("GetConfigFileNameFromContext failed")
	}
	return configFileName, nil
}

func FindIndexByName(slice []Plugin, name string) int {
	for i, v := range slice {
		if v.Name == name {
			return i
		}
	}
	return -1
}
