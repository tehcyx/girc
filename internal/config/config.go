package config

import (
	"fmt"
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v3"
)

// Values holds the active server configuration. Populated by Load() or init() for compatibility.
var Values *Config

// Config is the top-level configuration structure.
type Config struct {
	Server struct {
		Name                string `yaml:"name"`
		Host                string `yaml:"host"`
		Port                string `yaml:"port"`
		Motd                string `yaml:"motd"`
		Debug               bool   `yaml:"debug"`
		DefaultChannelModes string `yaml:"default_channel_modes"` // e.g. "+nt"
	}
	Auth struct {
		UsersFile string `yaml:"usersfile"`
	}
	Redis struct {
		Enabled bool   `yaml:"enabled"`
		URL     string `yaml:"url"`
		PodID   string `yaml:"pod_id"`
	}
}

// Load reads configuration from ~/.girc/conf.yaml if available, then applies
// environment variable overrides. It never panics: if the home directory is
// unavailable (e.g. FROM scratch containers) it silently falls back to
// defaults + env vars.
func Load() (*Config, error) {
	c := &Config{}

	// Apply built-in defaults first.
	c.Server.Name = "girc"
	c.Server.Host = "localhost"
	c.Server.Port = "6667"
	c.Server.Motd = "Find out more on github.com/tehcyx/girc"
	c.Server.Debug = false
	c.Server.DefaultChannelModes = "+nt"
	c.Redis.URL = "redis://localhost:6379"

	// Try to load from ~/.girc/conf.yaml; skip silently if unavailable.
	if cfgPath := configFilePath(); cfgPath != "" {
		if data, err := os.ReadFile(cfgPath); err == nil {
			if err2 := yaml.Unmarshal(data, c); err2 != nil {
				log.Warnf("config: failed to parse %s: %v", cfgPath, err2)
			}
		}
		// If the file doesn't exist that's fine — use defaults.
	}

	// Environment variables override yaml values.
	applyEnv(c)

	return c, nil
}

// configFilePath returns the path to the config file, or "" if the home dir
// cannot be determined (e.g. running as scratch container without /etc/passwd).
func configFilePath() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	dir := home + string(os.PathSeparator) + ".girc" + string(os.PathSeparator)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return ""
	}
	cfgPath := dir + "conf.yaml"
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		// Write default config.
		defaultConfig := `server:
    host: "localhost"
    port: "6667"
    name: "girc"
    motd: "Find out more on github.com/tehcyx/girc"
    debug: false
    default_channel_modes: "+nt"
redis:
    enabled: false
    url: "redis://localhost:6379"
    pod_id: ""
`
		_ = os.WriteFile(cfgPath, []byte(defaultConfig), 0600)
	}
	return cfgPath
}

// applyEnv overlays environment variable values on top of parsed config.
// Environment variables always win over yaml values.
func applyEnv(c *Config) {
	if v := os.Getenv("IRC_PORT"); v != "" {
		c.Server.Port = v
	}
	if v := os.Getenv("IRC_HOST"); v != "" {
		c.Server.Host = v
	}
	if v := os.Getenv("SERVER_NAME"); v != "" {
		c.Server.Name = v
	}
	if v := os.Getenv("MOTD_FILE"); v != "" {
		if data, err := os.ReadFile(v); err == nil {
			c.Server.Motd = strings.TrimSpace(string(data))
		} else {
			log.Warnf("config: MOTD_FILE %s: %v", v, err)
		}
	}
	if v := os.Getenv("REDIS_ENABLED"); v == "true" || v == "1" {
		c.Redis.Enabled = true
	}
	if v := os.Getenv("REDIS_URL"); v != "" {
		c.Redis.URL = v
	}
	if v := os.Getenv("REDIS_POD_ID"); v != "" {
		c.Redis.PodID = v
	}
	if v := os.Getenv("DEFAULT_CHANNEL_MODES"); v != "" {
		c.Server.DefaultChannelModes = v
	}
}

// init is kept so that packages that import config get a usable Values without
// calling Load(). It uses Load() internally so no duplication.
func init() {
	cfg, err := Load()
	if err != nil {
		// Should not happen; Load() never returns a hard error.
		log.Warnf("config: Load() failed: %v", err)
		cfg = &Config{}
	}
	Values = cfg
}

// User represents an authenticated user with permissions
type User struct {
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // In production, should be hashed
	IsOper   bool     `yaml:"isoper"`
	Channels []string `yaml:"channels"`
}

// Users holds the list of authenticated users
type Users struct {
	Users []User `yaml:"users"`
}

// Ban represents a banned user or hostmask
type Ban struct {
	Mask      string `yaml:"mask"`
	Channel   string `yaml:"channel"`
	SetBy     string `yaml:"setby"`
	Reason    string `yaml:"reason"`
	Timestamp int64  `yaml:"timestamp"`
}

// Bans holds the list of bans
type Bans struct {
	Bans []Ban `yaml:"bans"`
}

// usersFilePath returns the path for the users file, or "" on error.
func usersFilePath(custom string) string {
	if custom != "" {
		return custom
	}
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return fmt.Sprintf("%s%s.girc%susers.yaml", home, string(os.PathSeparator), string(os.PathSeparator))
}

// LoadUsers loads the users from the configured users file
func LoadUsers() (*Users, error) {
	usersFile := usersFilePath(Values.Auth.UsersFile)
	if usersFile == "" {
		return &Users{Users: []User{}}, nil
	}

	if _, err := os.Stat(usersFile); os.IsNotExist(err) {
		defaultUsers := &Users{
			Users: []User{
				{
					Username: "admin",
					Password: "admin",
					IsOper:   true,
					Channels: []string{},
				},
			},
		}
		if err := SaveUsers(defaultUsers, usersFile); err != nil {
			return nil, fmt.Errorf("failed to create default users file: %w", err)
		}
	}

	data, err := os.ReadFile(usersFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read users file: %w", err)
	}

	users := &Users{}
	if err := yaml.Unmarshal(data, users); err != nil {
		return nil, fmt.Errorf("failed to parse users file: %w", err)
	}

	return users, nil
}

// SaveUsers saves users to the given filepath
func SaveUsers(users *Users, filepath string) error {
	data, err := yaml.Marshal(users)
	if err != nil {
		return fmt.Errorf("failed to marshal users: %w", err)
	}
	if err := os.WriteFile(filepath, data, 0600); err != nil {
		return fmt.Errorf("failed to write users file: %w", err)
	}
	return nil
}

// LoadBans loads the bans from the bans file
func LoadBans() (*Bans, error) {
	home := os.Getenv("HOME")
	if home == "" {
		return &Bans{Bans: []Ban{}}, nil
	}
	bansFile := fmt.Sprintf("%s%s.girc%sbans.yaml", home, string(os.PathSeparator), string(os.PathSeparator))

	if _, err := os.Stat(bansFile); os.IsNotExist(err) {
		defaultBans := &Bans{Bans: []Ban{}}
		if err := SaveBans(defaultBans, bansFile); err != nil {
			return nil, fmt.Errorf("failed to create default bans file: %w", err)
		}
	}

	data, err := os.ReadFile(bansFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read bans file: %w", err)
	}

	bans := &Bans{}
	if err := yaml.Unmarshal(data, bans); err != nil {
		return nil, fmt.Errorf("failed to parse bans file: %w", err)
	}

	return bans, nil
}

// SaveBans saves bans to the bans file
func SaveBans(bans *Bans, filepath string) error {
	data, err := yaml.Marshal(bans)
	if err != nil {
		return fmt.Errorf("failed to marshal bans: %w", err)
	}
	if err := os.WriteFile(filepath, data, 0600); err != nil {
		return fmt.Errorf("failed to write bans file: %w", err)
	}
	return nil
}

// AuthenticateUser checks if username/password combination is valid
func AuthenticateUser(username, password string, users *Users) (*User, bool) {
	for i := range users.Users {
		if users.Users[i].Username == username && users.Users[i].Password == password {
			return &users.Users[i], true
		}
	}
	return nil, false
}

// IsBanned checks if a user mask is banned
func IsBanned(mask, channel string, bans *Bans) (bool, *Ban) {
	for i := range bans.Bans {
		ban := &bans.Bans[i]
		if ban.Channel != "" && ban.Channel != channel {
			continue
		}
		if matchBanMask(mask, ban.Mask) {
			return true, ban
		}
	}
	return false, nil
}

func matchBanMask(userMask, banPattern string) bool {
	return wildcardMatch(userMask, banPattern)
}

func wildcardMatch(text, pattern string) bool {
	if pattern == "*" {
		return true
	}
	ti, pi := 0, 0
	star := -1
	match := 0
	for ti < len(text) {
		if pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == text[ti]) {
			ti++
			pi++
		} else if pi < len(pattern) && pattern[pi] == '*' {
			star = pi
			match = ti
			pi++
		} else if star != -1 {
			pi = star + 1
			match++
			ti = match
		} else {
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
