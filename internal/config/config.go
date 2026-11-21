package config

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

type ContextKey string

const redactedPlaceholder = "[REDACTED]"

func redactSecret(value string) string {
	if value == "" {
		return ""
	}
	return redactedPlaceholder
}

type Config struct {
	Plugins                              []Plugin `yaml:"plugins"`
	Log                                  Log      `yaml:"log"`
	WorkDir                              string   `yaml:"work_dir"`
	Metrics                              Metrics  `yaml:"metrics"`
	GlobalPrivateKey                     string   `yaml:"global_private_key"`
	GlobalToken                          string   `yaml:"global_token"`
	PluginsPath                          string   `yaml:"plugins_path"`
	GlobalDatabaseURL                    string   `yaml:"global_database_url"`
	GlobalDatabasePort                   int      `yaml:"global_database_port"`
	GlobalDatabaseUser                   string   `yaml:"global_database_user"`
	GlobalDatabasePassword               string   `yaml:"global_database_password"`
	GlobalDatabaseDatabase               int      `yaml:"global_database_database"`
	GlobalDatabaseMaxRetries             int      `yaml:"global_database_max_retries"`
	GlobalDatabaseRetryDelay             int      `yaml:"global_database_retry_delay"`
	GlobalDatabaseRetryBackoffFactor     float64  `yaml:"global_database_retry_backoff_factor"`
	GlobalDatabaseClusterMode            bool     `yaml:"global_database_cluster_mode"`
	GlobalDatabaseTLSEnabled             bool     `yaml:"global_database_tls_enabled"`
	GlobalDatabaseTLSInsecure            bool     `yaml:"global_database_tls_insecure"`
	GlobalReceiverSecret                 string   `yaml:"global_receiver_secret"`
	GlobalTemplateDiskBuffer             float64  `yaml:"global_template_disk_buffer"`
	GlobalSkipCPUAndMemoryResourceChecks bool     `yaml:"global_skip_cpu_and_memory_resource_checks"`
	// GlobalHostToGuestFolderMounts lists host folders mounted into each guest VM (host_path[:guest_folder_name]); requires Anka >= 3.9.0 on Apple Silicon.
	GlobalHostToGuestFolderMounts []string `yaml:"global_host_to_guest_folder_mounts"`
}

type Log struct {
	FileDir       string `yaml:"file_dir"`
	SplitByPlugin bool   `yaml:"split_by_plugin"`
}

type Metrics struct {
	Aggregator    bool     `yaml:"aggregator"`
	BindAddress   string   `yaml:"bind_address"`
	Port          string   `yaml:"port"`
	SleepInterval int      `yaml:"sleep_interval"`
	Database      Database `yaml:"database"`
}

type Database struct {
	URL                string  `yaml:"url"`
	Port               int     `yaml:"port"`
	User               string  `yaml:"user"`
	Password           string  `yaml:"password"`
	Database           int     `yaml:"database"`
	MaxRetries         int     `yaml:"max_retries"`          // Maximum number of retry attempts (default: 5)
	RetryDelay         int     `yaml:"retry_delay"`          // Initial retry delay in milliseconds (default: 1000)
	RetryBackoffFactor float64 `yaml:"retry_backoff_factor"` // Backoff multiplier for retry delay (default: 2.0)
	ClusterMode        bool    `yaml:"cluster_mode"`         // Enable Redis cluster mode
	TLSEnabled         bool    `yaml:"tls_enabled"`          // Enable TLS connection
	TLSInsecure        bool    `yaml:"tls_insecure"`         // Skip TLS certificate verification
}

type Plugin struct {
	SleepInterval                  int      `yaml:"sleep_interval"`
	Name                           string   `yaml:"name"`
	Plugin                         string   `yaml:"plugin"`
	Token                          string   `yaml:"token"`
	Repo                           string   `yaml:"repo"`
	Owner                          string   `yaml:"owner"`
	Enterprise                     string   `yaml:"enterprise"` // Optional: GitHub Enterprise Cloud slug (enterprise > org > repo)
	QueueName                      string   `yaml:"queue_name"` // Optional: override queue namespace (defaults to Enterprise or Owner)
	Database                       Database `yaml:"database"`
	RegistryURL                    string   `yaml:"registry_url"`
	SkipPull                       bool     `yaml:"skip_pull"`
	PrivateKey                     string   `yaml:"private_key"`
	AppID                          int64    `yaml:"app_id"`
	InstallationID                 int64    `yaml:"installation_id"`
	Port                           string   `yaml:"port"`
	Secret                         string   `yaml:"secret"`
	HookID                         int64    `yaml:"hook_id"`
	SkipRedeliver                  bool     `yaml:"skip_redeliver"`
	RunnerGroup                    string   `yaml:"runner_group"`
	RedeliverHours                 int      `yaml:"redeliver_hours"`
	RegistrationTimeoutSeconds     int      `yaml:"registration_timeout_seconds"`
	TemplateDiskBuffer             float64  `yaml:"template_disk_buffer"`               // Plugin-specific disk buffer percentage (e.g., 10.0 for 10%)
	JobRetryAttempts               int      `yaml:"job_retry_attempts"`                 // Maximum number of retry attempts for failed jobs
	SkipCPUAndMemoryResourceChecks bool     `yaml:"skip_cpu_and_memory_resource_checks"` // Skip CPU/RAM resource admission checks for this plugin
	// HostToGuestFolderMounts adds host folders mounted into guests for this plugin’s VMs, merged with global_host_to_guest_folder_mounts; requires Anka >= 3.9.0 on Apple Silicon.
	HostToGuestFolderMounts []string `yaml:"host_to_guest_folder_mounts"`
}

// LogValue redacts sensitive credentials when Config is logged via slog.
func (c Config) LogValue() slog.Value {
	type redactedConfig Config
	sanitized := redactedConfig(c)
	sanitized.GlobalPrivateKey = redactSecret(sanitized.GlobalPrivateKey)
	sanitized.GlobalToken = redactSecret(sanitized.GlobalToken)
	sanitized.GlobalDatabasePassword = redactSecret(sanitized.GlobalDatabasePassword)
	sanitized.GlobalReceiverSecret = redactSecret(sanitized.GlobalReceiverSecret)
	sanitized.Metrics.Database.Password = redactSecret(sanitized.Metrics.Database.Password)
	if sanitized.Plugins != nil {
		pluginsCopy := make([]Plugin, len(sanitized.Plugins))
		for i, p := range sanitized.Plugins {
			p.Token = redactSecret(p.Token)
			p.PrivateKey = redactSecret(p.PrivateKey)
			p.Secret = redactSecret(p.Secret)
			p.Database.Password = redactSecret(p.Database.Password)
			pluginsCopy[i] = p
		}
		sanitized.Plugins = pluginsCopy
	}
	return slog.AnyValue(sanitized)
}

// LogValue redacts sensitive credentials when Plugin is logged via slog.
func (p Plugin) LogValue() slog.Value {
	type redactedPlugin Plugin
	sanitized := redactedPlugin(p)
	sanitized.Token = redactSecret(sanitized.Token)
	sanitized.PrivateKey = redactSecret(sanitized.PrivateKey)
	sanitized.Secret = redactSecret(sanitized.Secret)
	sanitized.Database.Password = redactSecret(sanitized.Database.Password)
	return slog.AnyValue(sanitized)
}

// LogValue redacts the password when Database is logged via slog.
func (d Database) LogValue() slog.Value {
	type redactedDatabase Database
	sanitized := redactedDatabase(d)
	sanitized.Password = redactSecret(sanitized.Password)
	return slog.AnyValue(sanitized)
}

// IsReceiver reports whether this plugin entry represents a receiver plugin.
func (p Plugin) IsReceiver() bool {
	return strings.HasSuffix(p.Plugin, "_receiver")
}

// ValidatePlugins validates plugin names across the loaded configuration.
func ValidatePlugins(plugins []Plugin) error {
	seen := make(map[string]struct{}, len(plugins))
	for _, p := range plugins {
		if p.Name == "" {
			return fmt.Errorf("name is required for plugins")
		}
		if strings.Contains(p.Name, " ") {
			return fmt.Errorf("plugin name cannot contain spaces")
		}
		if _, dup := seen[p.Name]; dup {
			return fmt.Errorf("duplicate plugin name %q: plugin names must be unique", p.Name)
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}

// ResolveDatabaseConfig merges global database settings with a per-plugin or metrics
// database block. Global values override plugin values when set, matching existing config precedence.
func ResolveDatabaseConfig(global Config, specific Database) Database {
	resolved := specific
	if global.GlobalDatabaseURL != "" {
		resolved.URL = global.GlobalDatabaseURL
	}
	if global.GlobalDatabasePort != 0 {
		resolved.Port = global.GlobalDatabasePort
	}
	if global.GlobalDatabaseUser != "" {
		resolved.User = global.GlobalDatabaseUser
	}
	if global.GlobalDatabasePassword != "" {
		resolved.Password = global.GlobalDatabasePassword
	}
	if global.GlobalDatabaseDatabase != 0 {
		resolved.Database = global.GlobalDatabaseDatabase
	}
	if global.GlobalDatabaseMaxRetries != 0 {
		resolved.MaxRetries = global.GlobalDatabaseMaxRetries
	}
	if global.GlobalDatabaseRetryDelay != 0 {
		resolved.RetryDelay = global.GlobalDatabaseRetryDelay
	}
	if global.GlobalDatabaseRetryBackoffFactor != 0 {
		resolved.RetryBackoffFactor = global.GlobalDatabaseRetryBackoffFactor
	}
	if global.GlobalDatabaseClusterMode {
		resolved.ClusterMode = true
	}
	if global.GlobalDatabaseTLSEnabled {
		resolved.TLSEnabled = true
	}
	if global.GlobalDatabaseTLSInsecure {
		resolved.TLSInsecure = true
	}
	return resolved
}

// GitHubScope is the registration/webhook level for a GitHub plugin.
type GitHubScope string

const (
	GitHubScopeEnterprise   GitHubScope = "enterprise"
	GitHubScopeOrganization GitHubScope = "organization"
	GitHubScopeRepository   GitHubScope = "repository"
	GitHubScopeUnspecified  GitHubScope = ""
)

// ValidateGitHubScope checks enterprise/owner/repo combinations.
// Rejects repo without owner, and enterprise combined with owner or repo.
func (p Plugin) ValidateGitHubScope() error {
	if p.Enterprise != "" && (p.Owner != "" || p.Repo != "") {
		return fmt.Errorf("enterprise cannot be combined with owner or repo; pick one scope level")
	}
	if p.Repo != "" && p.Owner == "" {
		return fmt.Errorf("repo requires owner to be set")
	}
	if p.Enterprise == "" && p.Owner == "" {
		return fmt.Errorf("enterprise or owner must be set")
	}
	return nil
}

// GitHubScope returns the resolved scope for this plugin.
// Call ValidateGitHubScope first for invalid combinations.
func (p Plugin) GitHubScope() GitHubScope {
	if p.Enterprise != "" {
		return GitHubScopeEnterprise
	}
	if p.Repo != "" {
		return GitHubScopeRepository
	}
	if p.Owner != "" {
		return GitHubScopeOrganization
	}
	return GitHubScopeUnspecified
}

// IsEnterpriseScope reports whether the plugin is enterprise-scoped.
func (p Plugin) IsEnterpriseScope() bool {
	return p.GitHubScope() == GitHubScopeEnterprise
}

// ValidateEnterpriseGitHubCredentials requires both a classic PAT and GitHub App
// credentials for enterprise-scoped handlers. Non-enterprise plugins always pass.
func (p Plugin) ValidateEnterpriseGitHubCredentials() error {
	if !p.IsEnterpriseScope() {
		return nil
	}
	if p.Token == "" {
		return fmt.Errorf("enterprise scope requires a classic PAT (token or global_token) with manage_runners:enterprise")
	}
	if p.PrivateKey == "" || p.AppID == 0 {
		return fmt.Errorf("enterprise scope requires GitHub App auth (private_key or global_private_key, plus app_id) for org Actions get/cancel")
	}
	return nil
}

// GitHubRegistrationURL returns the Actions runner registration URL for this plugin's scope.
func (p Plugin) GitHubRegistrationURL() string {
	switch p.GitHubScope() {
	case GitHubScopeEnterprise:
		return fmt.Sprintf("https://github.com/enterprises/%s", p.Enterprise)
	case GitHubScopeRepository:
		return fmt.Sprintf("https://github.com/%s/%s", p.Owner, p.Repo)
	case GitHubScopeOrganization:
		return fmt.Sprintf("https://github.com/%s", p.Owner)
	default:
		return ""
	}
}

// GetQueueOwner returns QueueName if set, otherwise Enterprise if set, otherwise Owner.
// This allows multiple organizations (or an enterprise) to share a single queue namespace.
func (p Plugin) GetQueueOwner() string {
	if p.QueueName != "" {
		return p.QueueName
	}
	if p.Enterprise != "" {
		return p.Enterprise
	}
	return p.Owner
}

// MetricsOwner returns the scope segment for Redis metrics keys: Owner, else Enterprise.
// Unlike GetQueueOwner, this ignores QueueName so shared queues still get distinct metrics keys.
func (p Plugin) MetricsOwner() string {
	if p.Owner != "" {
		return p.Owner
	}
	return p.Enterprise
}

// MetricsKeyEnding returns "{owner|enterprise}/{name}", or just "{name}" if neither scope is set.
// Used as the suffix for Redis keys: anklet/metrics/{MetricsKeyEnding}.
func (p Plugin) MetricsKeyEnding() string {
	if owner := p.MetricsOwner(); owner != "" {
		return owner + "/" + p.Name
	}
	return p.Name
}

// AnyHostToGuestFolderMountsConfigured reports whether any global or per-plugin host-to-guest folder mounts are set.
func AnyHostToGuestFolderMountsConfigured(cfg Config) bool {
	if len(cfg.GlobalHostToGuestFolderMounts) > 0 {
		return true
	}
	for i := range cfg.Plugins {
		if len(cfg.Plugins[i].HostToGuestFolderMounts) > 0 {
			return true
		}
	}
	return false
}

// dedupeMappingKeys keeps the last value when a YAML mapping contains duplicate keys,
// preserving compatibility with legacy yaml.v2 configs and credential-injection scripts.
func dedupeMappingKeys(node *yaml.Node) {
	if node == nil {
		return
	}
	if node.Kind == yaml.MappingNode && len(node.Content) >= 2 {
		lastIdx := make(map[string]int, len(node.Content)/2)
		for i := 0; i+1 < len(node.Content); i += 2 {
			lastIdx[node.Content[i].Value] = i
		}
		deduped := make([]*yaml.Node, 0, len(node.Content))
		for i := 0; i+1 < len(node.Content); i += 2 {
			if lastIdx[node.Content[i].Value] == i {
				dedupeMappingKeys(node.Content[i+1])
				deduped = append(deduped, node.Content[i], node.Content[i+1])
			}
		}
		node.Content = deduped
		return
	}
	for _, child := range node.Content {
		dedupeMappingKeys(child)
	}
}

func LoadConfig(configPath string) (cfg Config, err error) {
	file, err := os.Open(configPath)
	if err != nil {
		return cfg, err
	}
	defer func() {
		closeErr := file.Close()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	var doc yaml.Node
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&doc); err != nil {
		return Config{}, err
	}
	dedupeMappingKeys(&doc)
	if err := doc.Decode(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func LoadInEnvs(cfg Config) (Config, error) {
	/////////////////////////////////
	// AGGREGATOR ///////////////////
	if envAggregator := os.Getenv("ANKLET_METRICS_AGGREGATOR"); envAggregator != "" {
		cfg.Metrics.Aggregator = envAggregator == "true"
	}
	if envBindAddress := os.Getenv("ANKLET_METRICS_BIND_ADDRESS"); envBindAddress != "" {
		cfg.Metrics.BindAddress = envBindAddress
	}
	if envPort := os.Getenv("ANKLET_METRICS_PORT"); envPort != "" {
		cfg.Metrics.Port = envPort
	}
	if envSleepInterval := os.Getenv("ANKLET_METRICS_SLEEP_INTERVAL"); envSleepInterval != "" {
		value, err := strconv.Atoi(envSleepInterval)
		if err != nil {
			return Config{}, err
		}
		cfg.Metrics.SleepInterval = value
	}
	if envDBUser := os.Getenv("ANKLET_METRICS_DATABASE_USER"); envDBUser != "" {
		cfg.Metrics.Database.User = envDBUser
	}
	if envDBPassword := os.Getenv("ANKLET_METRICS_DATABASE_PASSWORD"); envDBPassword != "" {
		cfg.Metrics.Database.Password = envDBPassword
	}
	if envDBURL := os.Getenv("ANKLET_METRICS_DATABASE_URL"); envDBURL != "" {
		cfg.Metrics.Database.URL = envDBURL
	}
	if envDBPort := os.Getenv("ANKLET_METRICS_DATABASE_PORT"); envDBPort != "" {
		port, err := strconv.Atoi(envDBPort)
		if err != nil {
			return Config{}, err
		}
		cfg.Metrics.Database.Port = port
	}
	if envDBDatabase := os.Getenv("ANKLET_METRICS_DATABASE_DATABASE"); envDBDatabase != "" {
		dbNum, err := strconv.Atoi(envDBDatabase)
		if err != nil {
			return Config{}, err
		}
		cfg.Metrics.Database.Database = dbNum
	}

	///////////////////////////
	// Global / General ///////
	if workDir := os.Getenv("ANKLET_WORK_DIR"); workDir != "" {
		cfg.WorkDir = workDir
	}
	if logFileDir := os.Getenv("ANKLET_LOG_FILE_DIR"); logFileDir != "" {
		cfg.Log.FileDir = logFileDir
	}
	if pluginsPath := os.Getenv("ANKLET_PLUGINS_PATH"); pluginsPath != "" {
		cfg.PluginsPath = pluginsPath
	}
	if envGlobalPrivateKey := os.Getenv("ANKLET_GLOBAL_PRIVATE_KEY"); envGlobalPrivateKey != "" {
		cfg.GlobalPrivateKey = envGlobalPrivateKey
	}
	if envGlobalToken := os.Getenv("ANKLET_GLOBAL_TOKEN"); envGlobalToken != "" {
		cfg.GlobalToken = envGlobalToken
	}

	if envGlobalDatabaseURL := os.Getenv("ANKLET_GLOBAL_DATABASE_URL"); envGlobalDatabaseURL != "" {
		cfg.GlobalDatabaseURL = envGlobalDatabaseURL
	}
	if envGlobalDatabasePort := os.Getenv("ANKLET_GLOBAL_DATABASE_PORT"); envGlobalDatabasePort != "" {
		port, err := strconv.Atoi(envGlobalDatabasePort)
		if err != nil {
			return Config{}, err
		}
		cfg.GlobalDatabasePort = port
	}
	if envGlobalDatabaseUser := os.Getenv("ANKLET_GLOBAL_DATABASE_USER"); envGlobalDatabaseUser != "" {
		cfg.GlobalDatabaseUser = envGlobalDatabaseUser
	}
	if envGlobalDatabasePassword := os.Getenv("ANKLET_GLOBAL_DATABASE_PASSWORD"); envGlobalDatabasePassword != "" {
		cfg.GlobalDatabasePassword = envGlobalDatabasePassword
	}
	if envGlobalDatabaseDatabase := os.Getenv("ANKLET_GLOBAL_DATABASE_DATABASE"); envGlobalDatabaseDatabase != "" {
		dbNum, err := strconv.Atoi(envGlobalDatabaseDatabase)
		if err != nil {
			return Config{}, err
		}
		cfg.GlobalDatabaseDatabase = dbNum
	}
	if envGlobalDatabaseMaxRetries := os.Getenv("ANKLET_GLOBAL_DATABASE_MAX_RETRIES"); envGlobalDatabaseMaxRetries != "" {
		retries, err := strconv.Atoi(envGlobalDatabaseMaxRetries)
		if err != nil {
			return Config{}, err
		}
		cfg.GlobalDatabaseMaxRetries = retries
	}
	if envGlobalDatabaseRetryDelay := os.Getenv("ANKLET_GLOBAL_DATABASE_RETRY_DELAY"); envGlobalDatabaseRetryDelay != "" {
		delay, err := strconv.Atoi(envGlobalDatabaseRetryDelay)
		if err != nil {
			return Config{}, err
		}
		cfg.GlobalDatabaseRetryDelay = delay
	}
	if envGlobalDatabaseRetryBackoff := os.Getenv("ANKLET_GLOBAL_DATABASE_RETRY_BACKOFF_FACTOR"); envGlobalDatabaseRetryBackoff != "" {
		backoff, err := strconv.ParseFloat(envGlobalDatabaseRetryBackoff, 64)
		if err != nil {
			return Config{}, err
		}
		cfg.GlobalDatabaseRetryBackoffFactor = backoff
	}
	if envGlobalDatabaseClusterMode := os.Getenv("ANKLET_GLOBAL_DATABASE_CLUSTER_MODE"); envGlobalDatabaseClusterMode != "" {
		cfg.GlobalDatabaseClusterMode = envGlobalDatabaseClusterMode == "true"
	}
	if envGlobalDatabaseTLSEnabled := os.Getenv("ANKLET_GLOBAL_DATABASE_TLS_ENABLED"); envGlobalDatabaseTLSEnabled != "" {
		cfg.GlobalDatabaseTLSEnabled = envGlobalDatabaseTLSEnabled == "true"
	}
	if envGlobalDatabaseTLSInsecure := os.Getenv("ANKLET_GLOBAL_DATABASE_TLS_INSECURE"); envGlobalDatabaseTLSInsecure != "" {
		cfg.GlobalDatabaseTLSInsecure = envGlobalDatabaseTLSInsecure == "true"
	}

	if envGlobalReceiverSecret := os.Getenv("ANKLET_GLOBAL_RECEIVER_SECRET"); envGlobalReceiverSecret != "" {
		cfg.GlobalReceiverSecret = envGlobalReceiverSecret
	}

	if envGlobalTemplateDiskBuffer := os.Getenv("ANKLET_GLOBAL_TEMPLATE_DISK_BUFFER"); envGlobalTemplateDiskBuffer != "" {
		buffer, err := strconv.ParseFloat(envGlobalTemplateDiskBuffer, 64)
		if err != nil {
			return Config{}, err
		}
		cfg.GlobalTemplateDiskBuffer = buffer
	}
	if envGlobalSkip := os.Getenv("ANKLET_GLOBAL_SKIP_CPU_AND_MEMORY_RESOURCE_CHECKS"); envGlobalSkip != "" {
		cfg.GlobalSkipCPUAndMemoryResourceChecks = envGlobalSkip == "true"
	}

	if envGlobalMounts := os.Getenv("ANKLET_GLOBAL_HOST_TO_GUEST_FOLDER_MOUNTS"); envGlobalMounts != "" {
		var entries []string
		for _, part := range strings.Split(envGlobalMounts, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				entries = append(entries, trimmed)
			}
		}
		cfg.GlobalHostToGuestFolderMounts = entries
	}

	if envJobRetryAttempts := os.Getenv("ANKLET_JOB_RETRY_ATTEMPTS"); envJobRetryAttempts != "" {
		attempts, err := strconv.Atoi(envJobRetryAttempts)
		if err != nil {
			return Config{}, err
		}
		for i := range cfg.Plugins {
			cfg.Plugins[i].JobRetryAttempts = attempts
		}
	}

	for i := range cfg.Plugins {
		pluginEnvPrefix := normalizePluginEnvPrefix(cfg.Plugins[i].Name)
		if pluginEnvPrefix == "" {
			continue
		}
		if err := applyPluginEnvOverrides(&cfg.Plugins[i], pluginEnvPrefix); err != nil {
			return Config{}, err
		}
	}

	for i := range cfg.Plugins {
		if cfg.Plugins[i].PrivateKey == "" && cfg.GlobalPrivateKey != "" {
			cfg.Plugins[i].PrivateKey = cfg.GlobalPrivateKey
		}
		if cfg.Plugins[i].Token == "" && cfg.GlobalToken != "" {
			cfg.Plugins[i].Token = cfg.GlobalToken
		}
	}

	return cfg, nil
}

func normalizePluginEnvPrefix(name string) string {
	if name == "" {
		return ""
	}
	upper := strings.ToUpper(name)
	var builder strings.Builder
	builder.Grow(len(upper))
	for _, ch := range upper {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '_' {
			builder.WriteRune(ch)
		} else {
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

func applyPluginEnvOverrides(plugin *Plugin, pluginEnvPrefix string) error {
	pluginValue := reflect.ValueOf(plugin).Elem()
	pluginType := pluginValue.Type()

	for _, env := range os.Environ() {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		envKey := parts[0]
		envValue := parts[1]
		if !strings.HasPrefix(envKey, pluginEnvPrefix+"_") {
			continue
		}
		suffix := strings.TrimPrefix(envKey, pluginEnvPrefix+"_")

		if strings.HasPrefix(suffix, "DATABASE_") {
			databaseField, ok := pluginType.FieldByName("Database")
			if ok {
				if err := setStructFieldByEnvKey(
					pluginValue.FieldByName("Database"),
					databaseField.Type,
					strings.TrimPrefix(suffix, "DATABASE_"),
					envValue,
				); err != nil {
					return err
				}
			}
			continue
		}

		// Ignore empty values so unset env vars do not wipe YAML configuration.
		if strings.TrimSpace(envValue) == "" {
			continue
		}

		if err := setStructFieldByEnvKey(pluginValue, pluginType, suffix, envValue); err != nil {
			return err
		}
	}
	return nil
}

func setStructFieldByEnvKey(target reflect.Value, targetType reflect.Type, envKey string, envValue string) error {
	if !target.IsValid() {
		return nil
	}
	for i := 0; i < targetType.NumField(); i++ {
		field := targetType.Field(i)
		yamlTag := field.Tag.Get("yaml")
		if yamlTag == "" || yamlTag == "-" {
			continue
		}
		if normalizeEnvKey(yamlTag) != envKey {
			continue
		}
		fieldValue := target.Field(i)
		if !fieldValue.CanSet() {
			return nil
		}
		return setValueFromString(fieldValue, envValue)
	}
	return nil
}

func normalizeEnvKey(value string) string {
	return strings.ToUpper(strings.ReplaceAll(value, "-", "_"))
}

func setValueFromString(fieldValue reflect.Value, rawValue string) error {
	switch fieldValue.Kind() {
	case reflect.String:
		fieldValue.SetString(rawValue)
	case reflect.Bool:
		fieldValue.SetBool(rawValue == "true")
	case reflect.Int:
		parsed, err := strconv.Atoi(rawValue)
		if err != nil {
			return err
		}
		fieldValue.SetInt(int64(parsed))
	case reflect.Int64:
		parsed, err := strconv.ParseInt(rawValue, 10, 64)
		if err != nil {
			return err
		}
		fieldValue.SetInt(parsed)
	case reflect.Float64:
		parsed, err := strconv.ParseFloat(rawValue, 64)
		if err != nil {
			return err
		}
		fieldValue.SetFloat(parsed)
	case reflect.Slice:
		if fieldValue.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice field type for env override")
		}
		var parts []string
		for _, part := range strings.Split(rawValue, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		fieldValue.Set(reflect.ValueOf(parts))
	}
	return nil
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

// TemplateUsage tracks usage statistics for a template/tag combination.
type TemplateUsage struct {
	UUID         string    `json:"uuid"`
	Name         string    `json:"name"`
	Tag          string    `json:"tag"`
	ImageSize    uint64    `json:"image_size"`
	LastAccessed time.Time `json:"last_accessed"`
	UsageCount   uint64    `json:"usage_count"`
	InUse        bool      `json:"in_use"`
	InUseCount   int       `json:"in_use_count"`
	Pulling      bool      `json:"pulling"`
}

// TemplateTracker manages template usage across all plugins.
type TemplateTracker struct {
	Templates map[string]*TemplateUsage // key: templateUUID
	Mutex     *sync.RWMutex
}

type Globals struct {
	RunPluginsOnce                   bool
	ReturnAllToMainQueue             atomic.Bool
	GitHubAPIRateLimitResetUnix      atomic.Int64
	LastGitHubAPIRateLimitNoticeUnix atomic.Int64
	PluginsPath                      string
	DebugEnabled                     bool
	HostCPUCount                     int
	HostMemoryBytes                  uint64
	QueueTargetIndex                 atomic.Int64
	Plugins                          map[string]map[string]*PluginGlobal
	TemplateTracker                  *TemplateTracker
}

// GetPluginRunCount returns the current value of the shared plugin run counter.
func (g *Globals) GetPluginRunCount(pluginName string) (uint64, error) {
	for _, nameOfPlugin := range g.Plugins {
		if pg, ok := nameOfPlugin[pluginName]; ok {
			return pg.PluginRunCount.Load(), nil
		}
	}
	return 0, fmt.Errorf("GetPluginRunCount: plugin not found")
}

// IncrementPluginRunCount increments the shared plugin run counter.
func (g *Globals) IncrementPluginRunCount(pluginName string) {
	for _, nameOfPlugin := range g.Plugins {
		if pg, ok := nameOfPlugin[pluginName]; ok {
			pg.PluginRunCount.Add(1)
		}
	}
}

func GetWorkerGlobalsFromContext(ctx context.Context) (*Globals, error) {
	globals, ok := ctx.Value(ContextKey("globals")).(*Globals)
	if !ok || globals == nil {
		return nil, fmt.Errorf("GetGlobalsFromContext failed")
	}
	return globals, nil
}

func (g *Globals) GetPausedPlugin() string {
	for pluginName, pluginGroup := range g.Plugins {
		for _, pluginSettings := range pluginGroup {
			if pluginSettings.Paused.Load() {
				return pluginName
			}
		}
	}
	return ""
}

func (g *Globals) GetQueueTargetIndex() int64 {
	return g.QueueTargetIndex.Load()
}

func (g *Globals) IncrementQueueTargetIndex() {
	g.QueueTargetIndex.Add(1)
}

func (g *Globals) DecrementQueueTargetIndex() {
	for {
		cur := g.QueueTargetIndex.Load()
		if cur <= 0 {
			return
		}
		if g.QueueTargetIndex.CompareAndSwap(cur, cur-1) {
			return
		}
	}
}

func (g *Globals) ResetQueueTargetIndex() {
	g.QueueTargetIndex.Store(0)
}

// NewTemplateTracker creates a new template tracker.
func NewTemplateTracker() *TemplateTracker {
	return &TemplateTracker{
		Templates: make(map[string]*TemplateUsage),
		Mutex:     &sync.RWMutex{},
	}
}

// LogValue safely snapshots templates under RLock so logging never races with writers.
func (tt *TemplateTracker) LogValue() slog.Value {
	if tt == nil {
		return slog.AnyValue(nil)
	}
	return slog.AnyValue(tt.Snapshot())
}

// Len returns the number of tracked templates safely under RLock.
func (tt *TemplateTracker) Len() int {
	if tt == nil {
		return 0
	}
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()
	return len(tt.Templates)
}

// Snapshot returns a deep value copy of tracked templates safe for concurrent reads/logging.
func (tt *TemplateTracker) Snapshot() map[string]TemplateUsage {
	if tt == nil {
		return nil
	}
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()
	out := make(map[string]TemplateUsage, len(tt.Templates))
	for k, v := range tt.Templates {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// GetTemplateKey returns the key for a template UUID.
func (tt *TemplateTracker) GetTemplateKey(templateUUID string) string {
	return templateUUID
}

// RegisterDiscoveredTemplate adds or refreshes a locally discovered template under lock.
func (tt *TemplateTracker) RegisterDiscoveredTemplate(
	templateUUID, templateName, templateTag string,
	sizeBytes uint64,
	lastAccessed time.Time,
) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	key := tt.GetTemplateKey(templateUUID)
	if existing, ok := tt.Templates[key]; ok {
		existing.Name = templateName
		existing.Tag = templateTag
		if sizeBytes > 0 {
			existing.ImageSize = sizeBytes
		}
		if !lastAccessed.IsZero() {
			existing.LastAccessed = lastAccessed
		}
		return
	}
	tt.Templates[key] = &TemplateUsage{
		UUID:         templateUUID,
		Name:         templateName,
		Tag:          templateTag,
		ImageSize:    sizeBytes,
		LastAccessed: lastAccessed,
		UsageCount:   0,
		InUse:        false,
		InUseCount:   0,
		Pulling:      false,
	}
}

// UpdateTemplateUsage updates the usage statistics for a template.
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
		if templateName != "" {
			usage.Name = templateName
		}
		if templateTag != "" {
			usage.Tag = templateTag
		}
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
			InUseCount:   0,
			Pulling:      false,
		}
	}
}

// SetTemplateInUse reference-counts active users of a template so concurrent plugins
// do not evict a template while another plugin is cloning or using it.
func (tt *TemplateTracker) SetTemplateInUse(templateUUID, templateName, templateTag string, inUse bool) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	key := tt.GetTemplateKey(templateUUID)
	usage, exists := tt.Templates[key]
	if !exists {
		if !inUse {
			return
		}
		usage = &TemplateUsage{
			UUID:         templateUUID,
			Name:         templateName,
			Tag:          templateTag,
			LastAccessed: time.Now(),
		}
		tt.Templates[key] = usage
	}
	if inUse {
		usage.InUseCount++
	} else if usage.InUseCount > 0 {
		usage.InUseCount--
	}
	usage.InUse = usage.InUseCount > 0
}

// SetTemplatePulling marks a template as being pulled or not.
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
		tt.Templates[key] = &TemplateUsage{
			UUID:         templateUUID,
			Name:         templateName,
			Tag:          templateTag,
			ImageSize:    0,
			LastAccessed: lastAccessed,
			UsageCount:   0,
			InUse:        false,
			InUseCount:   0,
			Pulling:      true,
		}
	}
}

// GetLeastRecentlyUsedTemplates returns value copies of evictable templates sorted LRU first.
func (tt *TemplateTracker) GetLeastRecentlyUsedTemplates() []*TemplateUsage {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	templates := make([]*TemplateUsage, 0, len(tt.Templates))
	for _, usage := range tt.Templates {
		if !usage.InUse && usage.InUseCount == 0 && !usage.Pulling {
			copied := *usage
			templates = append(templates, &copied)
		}
	}

	slices.SortFunc(templates, func(a, b *TemplateUsage) int {
		if c := cmp.Compare(a.UsageCount, b.UsageCount); c != 0 {
			return c
		}
		if a.LastAccessed.Before(b.LastAccessed) {
			return -1
		}
		if a.LastAccessed.After(b.LastAccessed) {
			return 1
		}
		return cmp.Compare(a.UUID, b.UUID)
	})

	return templates
}

// RemoveTemplate removes a template from tracking.
func (tt *TemplateTracker) RemoveTemplate(templateUUID string) {
	tt.Mutex.Lock()
	defer tt.Mutex.Unlock()

	delete(tt.Templates, tt.GetTemplateKey(templateUUID))
}

// HasPullingTemplates returns true if any templates are currently being pulled.
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

// Contains checks if a template with the given UUID exists in the tracker.
func (tt *TemplateTracker) Contains(templateUUID string) bool {
	tt.Mutex.RLock()
	defer tt.Mutex.RUnlock()

	_, exists := tt.Templates[tt.GetTemplateKey(templateUUID)]
	return exists
}

func GetLoadedConfigFromContext(ctx context.Context) (*Config, error) {
	cfg, ok := ctx.Value(ContextKey("config")).(*Config)
	if !ok || cfg == nil {
		return nil, fmt.Errorf("GetLoadedConfigFromContext failed")
	}
	return cfg, nil
}

func GetConfigFileNameFromContext(ctx context.Context) (string, error) {
	configFileName, ok := ctx.Value(ContextKey("configFileName")).(string)
	if !ok {
		return "", fmt.Errorf("GetConfigFileNameFromContext failed")
	}
	return configFileName, nil
}
