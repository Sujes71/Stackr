package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/stackr/stackr/internal/crypto"
	"gopkg.in/yaml.v3"
)

// Root config lives at ~/.stackr/config.yaml
// Each profile lives at ~/.stackr/<profile-name>/
//   └── docker-compose.yml  (generated)
//   └── profile.yaml        (source of truth)

// Default port constants for supported database types.
const (
	DefaultPortPostgres = "5432"
	DefaultPortMongo    = "27017"
	DefaultPortMySQL    = "3306"
	DefaultPortMSSQL    = "1433"
)

type Config struct {
	Profiles []Profile `yaml:"profiles"`
}

type Profile struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description,omitempty"`
	CreatedAt   time.Time   `yaml:"created_at"`
	UpdatedAt   time.Time   `yaml:"updated_at"`
	Environments []Environment `yaml:"environments"`
	ActiveEnv            string      `yaml:"active_env"`
	LegacyDBOpsMigrated  bool        `yaml:"legacy_db_ops_migrated,omitempty"`
	ConfigVersion        int         `yaml:"config_version,omitempty"` // 0 = legacy/plaintext, 1 = encrypted passwords
}

type Environment struct {
	Name         string        `yaml:"name"` // local, dev, staging...
	Services     []Service     `yaml:"services"`
	PortForwards []PortForward `yaml:"port_forwards,omitempty"`
}

type Service struct {
	Name        string            `yaml:"name"`
	Image       string            `yaml:"image"`
	Tag         string            `yaml:"tag"`
	Ports       []PortMapping     `yaml:"ports,omitempty"`
	EnvVars     []EnvVar          `yaml:"env_vars,omitempty"`
	Volumes     []VolumeMapping   `yaml:"volumes,omitempty"`
	DependsOn   []string          `yaml:"depends_on,omitempty"`
	SyncEnvs    []SyncEnv         `yaml:"sync_envs,omitempty"`
	HealthCheck *HealthCheck      `yaml:"healthcheck,omitempty"`
	Restart     string            `yaml:"restart,omitempty"`
	Networks    []string          `yaml:"networks,omitempty"`
}

func (s Service) FullImage() string {
	if s.Tag == "" {
		return s.Image
	}
	return s.Image + ":" + s.Tag
}

var reInvalidCompose = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// ComposeName returns a sanitized version of the service name safe for use
// in docker-compose identifiers (service keys, container names, volume names).
// docker-compose only allows [a-zA-Z0-9][a-zA-Z0-9_.-]*.
func (s Service) ComposeName() string {
	return SanitizeComposeName(s.Name)
}

// SanitizeComposeName replaces characters invalid for docker-compose identifiers with "-".
// docker-compose only allows [a-zA-Z0-9][a-zA-Z0-9_.-]*.
func SanitizeComposeName(name string) string {
	sanitized := reInvalidCompose.ReplaceAllString(name, "-")
	// Ensure it doesn't start with a non-alphanumeric
	if len(sanitized) > 0 && !isAlphaNum(rune(sanitized[0])) {
		sanitized = "svc-" + sanitized
	}
	return sanitized
}

func isAlphaNum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// DBType returns the database type this service provides, or "" if not a DB.
// Supported: postgres, mysql, mongo, oracle, mssql.
func (s Service) DBType() string {
	img := s.Image
	switch {
	case containsFold(img, "postgres") || containsFold(img, "postgis"):
		return "postgres"
	case containsFold(img, "mysql") || containsFold(img, "mariadb"):
		return "mysql"
	case containsFold(img, "mongo"):
		return "mongo"
	case containsFold(img, "oracle"):
		return "oracle"
	case containsFold(img, "mssql") || containsFold(img, "sqlserver") || containsFold(img, "azuresql"):
		return "mssql"
	case containsFold(img, "cockroach"):
		return "postgres" // cockroachdb is postgres-compatible
	default:
		return ""
	}
}

func containsFold(s, substr string) bool {
	s, substr = strings.ToLower(s), strings.ToLower(substr)
	return strings.Contains(s, substr)
}

// FindSyncEnv finds a SyncEnv by name, or returns nil if not found.
func (s Service) FindSyncEnv(name string) *SyncEnv {
	for i := range s.SyncEnvs {
		if s.SyncEnvs[i].Name == name {
			return &s.SyncEnvs[i]
		}
	}
	return nil
}

// HasDatabaseServices returns true if any service in this environment is a known database.
func (e Environment) HasDatabaseServices() bool {
	for _, s := range e.Services {
		if s.DBType() != "" {
			return true
		}
	}
	return false
}

// DatabaseTypes returns the set of distinct DB types in this environment.
func (e Environment) DatabaseTypes() []string {
	seen := map[string]bool{}
	var types []string
	for _, s := range e.Services {
		t := s.DBType()
		if t != "" && !seen[t] {
			seen[t] = true
			types = append(types, t)
		}
	}
	return types
}

// SyncEnv defines a sync-from environment's database credentials.
type SyncEnv struct {
	Name     string `yaml:"name"`
	DBType   string `yaml:"db_type"`
	Host     string `yaml:"host"`
	Port     string `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password,omitempty"`
	Database string `yaml:"database"`
	AuthDB   string `yaml:"auth_db,omitempty"`
}

// PortForward defines a K8s port-forward configuration.
type PortForward struct {
	Name         string `yaml:"name"`
	LocalPort    int    `yaml:"local_port"`
	RemotePort   int    `yaml:"remote_port"`
	K8sContext   string `yaml:"k8s_context"`
	K8sNamespace string `yaml:"k8s_namespace"`
	K8sService   string `yaml:"k8s_service"`
}

// GetPassword returns the decrypted password.
// If the password is encrypted, it is decrypted on-the-fly using the master key.
// Empty passwords are returned as-is.
func (s SyncEnv) GetPassword() (string, error) {
	if s.Password == "" {
		return "", nil
	}
	return crypto.Decrypt(crypto.GetKey(), s.Password)
}

// SetPassword encrypts the plaintext password and stores it in the Password field.
// Empty passwords are stored as empty (not encrypted).
func (s *SyncEnv) SetPassword(plaintext string) error {
	if plaintext == "" {
		s.Password = ""
		return nil
	}
	encrypted, err := crypto.Encrypt(crypto.GetKey(), plaintext)
	if err != nil {
		return fmt.Errorf("encrypt password: %w", err)
	}
	s.Password = encrypted
	return nil
}

// Validate checks that all required fields are set and valid.
// Returns a map of field name → error message for invalid fields.
func (s SyncEnv) Validate() map[string]string {
	errs := make(map[string]string)
	if strings.TrimSpace(s.Name) == "" {
		errs["name"] = "Name is required"
	}
	if strings.TrimSpace(s.Host) == "" {
		errs["host"] = "Host is required"
	}
	if s.Port != "" {
		if p, err := strconv.Atoi(s.Port); err != nil || p < 1 || p > 65535 {
			errs["port"] = "Port must be a number between 1 and 65535"
		}
	}
	if strings.TrimSpace(s.User) == "" {
		errs["user"] = "User is required"
	}
	if strings.TrimSpace(s.Database) == "" {
		errs["database"] = "Database is required"
	}
	return errs
}

// DumpDir returns the directory path for dumps of this sync environment.
func (s SyncEnv) DumpDir(profileName, envName, serviceName string) string {
	return filepath.Join(ProfileDir(profileName), envName, "dumps", serviceName, s.Name)
}

type PortMapping struct {
	Host      string `yaml:"host"`
	Container string `yaml:"container"`
}

func (p PortMapping) String() string {
	return fmt.Sprintf("%s:%s", p.Host, p.Container)
}

type EnvVar struct {
	Key   string `yaml:"key"`
	Value string `yaml:"value"`
}

type VolumeMapping struct {
	Source string `yaml:"source"`
	Target string `yaml:"target"`
	Named  bool   `yaml:"named"` // true = named volume, false = bind mount
}

type HealthCheck struct {
	Test     string `yaml:"test"`
	Interval string `yaml:"interval"`
	Timeout  string `yaml:"timeout"`
	Retries  int    `yaml:"retries"`
}

// ── Persistence ──────────────────────────────────────────────────────────────

func StackrDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".stackr")
}

func ProfileDir(profileName string) string {
	return filepath.Join(StackrDir(), profileName)
}

func configPath() string {
	return filepath.Join(StackrDir(), "config.yaml")
}

func Load() (*Config, error) {
	if err := os.MkdirAll(StackrDir(), 0755); err != nil {
		return nil, err
	}
	path := configPath()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		cfg := &Config{}
		return cfg, cfg.Save()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Initialize encryption key before any crypto operations
	if err := crypto.Init(); err != nil {
		return nil, fmt.Errorf("init encryption: %w", err)
	}
	// Migrate legacy plaintext passwords to encrypted format if needed
	if err := cfg.maybeMigratePasswords(); err != nil {
		return nil, fmt.Errorf("password migration: %w", err)
	}
	return &cfg, nil
}

// maybeMigratePasswords encrypts any plaintext SyncEnv passwords in profiles
// that have ConfigVersion < 1, then saves the migrated config.
func (c *Config) maybeMigratePasswords() error {
	migrated := false
	for i := range c.Profiles {
		p := &c.Profiles[i]
		if p.ConfigVersion >= 1 {
			continue
		}
		for j := range p.Environments {
			for k := range p.Environments[j].Services {
				for l := range p.Environments[j].Services[k].SyncEnvs {
					se := &p.Environments[j].Services[k].SyncEnvs[l]
					if se.Password != "" && !crypto.IsEncrypted(se.Password) {
						if err := se.SetPassword(se.Password); err != nil {
							return err
						}
						migrated = true
					}
				}
			}
		}
		p.ConfigVersion = 1
	}
	if migrated {
		return c.Save()
	}
	return nil
}

func (c *Config) Save() error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0644)
}

func (c *Config) AddProfile(p Profile) error {
	for _, existing := range c.Profiles {
		if existing.Name == p.Name {
			return fmt.Errorf("profile %q already exists", p.Name)
		}
	}
	// Create profile directory
	if err := os.MkdirAll(ProfileDir(p.Name), 0755); err != nil {
		return err
	}
	c.Profiles = append(c.Profiles, p)
	return c.Save()
}

func (c *Config) UpdateProfile(p Profile) error {
	for i, existing := range c.Profiles {
		if existing.Name == p.Name {
			p.UpdatedAt = time.Now()
			c.Profiles[i] = p
			return c.Save()
		}
	}
	return fmt.Errorf("profile %q not found", p.Name)
}

func (c *Config) DeleteProfile(name string) error {
	for i, p := range c.Profiles {
		if p.Name == name {
			c.Profiles = append(c.Profiles[:i], c.Profiles[i+1:]...)
			_ = os.RemoveAll(ProfileDir(name))
			return c.Save()
		}
	}
	return fmt.Errorf("profile %q not found", name)
}

func (c *Config) GetProfile(name string) (*Profile, bool) {
	for i := range c.Profiles {
		if c.Profiles[i].Name == name {
			return &c.Profiles[i], true
		}
	}
	return nil, false
}

// DeleteService removes a service from the given environment in a profile.
// Returns the updated profile so the caller can refresh their reference.
func (c *Config) DeleteService(profileName, envName, svcName string) (*Profile, error) {
	for i := range c.Profiles {
		if c.Profiles[i].Name != profileName {
			continue
		}
		p := &c.Profiles[i]
		for j := range p.Environments {
			if p.Environments[j].Name != envName {
				continue
			}
			env := &p.Environments[j]
			for k, s := range env.Services {
				if s.Name == svcName {
					env.Services = append(env.Services[:k], env.Services[k+1:]...)
					p.UpdatedAt = time.Now()
					// Return a copy so the caller gets the updated data
					cp := *p
					return &cp, c.Save()
				}
			}
			return nil, fmt.Errorf("service %q not found in %s/%s", svcName, profileName, envName)
		}
		return nil, fmt.Errorf("environment %q not found in profile %q", envName, profileName)
	}
	return nil, fmt.Errorf("profile %q not found", profileName)
}

// ClearServices removes ALL services from the given environment and saves.
func (c *Config) ClearServices(profileName, envName string) (*Profile, error) {
	for i := range c.Profiles {
		if c.Profiles[i].Name != profileName {
			continue
		}
		p := &c.Profiles[i]
		for j := range p.Environments {
			if p.Environments[j].Name != envName {
				continue
			}
			env := &p.Environments[j]
			if len(env.Services) == 0 {
				return nil, fmt.Errorf("no services to clear in %s/%s", profileName, envName)
			}
			env.Services = nil
			p.UpdatedAt = time.Now()
			cp := *p
			return &cp, c.Save()
		}
		return nil, fmt.Errorf("environment %q not found in profile %q", envName, profileName)
	}
	return nil, fmt.Errorf("profile %q not found", profileName)
}

// AddPortForward appends a port-forward to the given environment and saves.
// Returns an error if a port-forward with the same name already exists.
func (c *Config) AddPortForward(profileName, envName string, pf PortForward) (*Profile, error) {
	for i := range c.Profiles {
		if c.Profiles[i].Name != profileName {
			continue
		}
		p := &c.Profiles[i]
		for j := range p.Environments {
			if p.Environments[j].Name != envName {
				continue
			}
			env := &p.Environments[j]
			// Check for duplicate name
			for _, existing := range env.PortForwards {
				if existing.Name == pf.Name {
					return nil, fmt.Errorf("port-forward %q already exists in %s/%s", pf.Name, profileName, envName)
				}
			}
			env.PortForwards = append(env.PortForwards, pf)
			p.UpdatedAt = time.Now()
			cp := *p
			return &cp, c.Save()
		}
		return nil, fmt.Errorf("environment %q not found in profile %q", envName, profileName)
	}
	return nil, fmt.Errorf("profile %q not found", profileName)
}

// DeletePortForward removes a port-forward by name from the given environment and saves.
func (c *Config) DeletePortForward(profileName, envName, pfName string) (*Profile, error) {
	for i := range c.Profiles {
		if c.Profiles[i].Name != profileName {
			continue
		}
		p := &c.Profiles[i]
		for j := range p.Environments {
			if p.Environments[j].Name != envName {
				continue
			}
			env := &p.Environments[j]
			for k, pf := range env.PortForwards {
				if pf.Name == pfName {
					env.PortForwards = append(env.PortForwards[:k], env.PortForwards[k+1:]...)
					p.UpdatedAt = time.Now()
					cp := *p
					return &cp, c.Save()
				}
			}
			return nil, fmt.Errorf("port-forward %q not found in %s/%s", pfName, profileName, envName)
		}
		return nil, fmt.Errorf("environment %q not found in profile %q", envName, profileName)
	}
	return nil, fmt.Errorf("profile %q not found", profileName)
}

// UpdatePortForward replaces an existing port-forward in-place by name and saves.
func (c *Config) UpdatePortForward(profileName, envName, pfName string, updated PortForward) (*Profile, error) {
	for i := range c.Profiles {
		if c.Profiles[i].Name != profileName {
			continue
		}
		p := &c.Profiles[i]
		for j := range p.Environments {
			if p.Environments[j].Name != envName {
				continue
			}
			env := &p.Environments[j]
			for k, pf := range env.PortForwards {
				if pf.Name == pfName {
					env.PortForwards[k] = updated
					p.UpdatedAt = time.Now()
					cp := *p
					return &cp, c.Save()
				}
			}
			return nil, fmt.Errorf("port-forward %q not found in %s/%s", pfName, profileName, envName)
		}
		return nil, fmt.Errorf("environment %q not found in profile %q", envName, profileName)
	}
	return nil, fmt.Errorf("profile %q not found", profileName)
}

// ClearPortForwards removes ALL port-forwards from the given environment and saves.
func (c *Config) ClearPortForwards(profileName, envName string) (*Profile, error) {
	for i := range c.Profiles {
		if c.Profiles[i].Name != profileName {
			continue
		}
		p := &c.Profiles[i]
		for j := range p.Environments {
			if p.Environments[j].Name != envName {
				continue
			}
			env := &p.Environments[j]
			env.PortForwards = nil
			p.UpdatedAt = time.Now()
			cp := *p
			return &cp, c.Save()
		}
		return nil, fmt.Errorf("environment %q not found in profile %q", envName, profileName)
	}
	return nil, fmt.Errorf("profile %q not found", profileName)
}
