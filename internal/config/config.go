package config

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/user"

	yaml "gopkg.in/yaml.v3"
)

var Values *Config

type Config struct {
	Server struct {
		Name  string `yaml:"name"`
		Host  string `yaml:"host"`
		Port  string `yaml:"port"`
		Motd  string `yaml:"motd"`
		Debug bool   `yaml:"debug"` // Enable debug logging
	}
	Auth struct {
		UsersFile string `yaml:"usersfile"`
	}
	Redis struct {
		Enabled bool   `yaml:"enabled"` // Enable distributed mode with Redis
		URL     string `yaml:"url"`     // Redis connection URL (e.g., redis://localhost:6379)
		PodID   string `yaml:"pod_id"`  // Unique identifier for this pod (auto-generated if empty)
	}
}

// User represents an authenticated user with permissions
type User struct {
	Username string   `yaml:"username"`
	Password string   `yaml:"password"` // In production, should be hashed
	IsOper   bool     `yaml:"isoper"`
	Channels []string `yaml:"channels"` // Channels where user is operator
}

// Users holds the list of authenticated users
type Users struct {
	Users []User `yaml:"users"`
}

// Ban represents a banned user or hostmask
type Ban struct {
	Mask      string `yaml:"mask"`      // Ban mask (nick!user@host pattern)
	Channel   string `yaml:"channel"`   // Channel where ban applies (empty for server-wide)
	SetBy     string `yaml:"setby"`     // Who set the ban
	Reason    string `yaml:"reason"`    // Ban reason
	Timestamp int64  `yaml:"timestamp"` // Unix timestamp when ban was set
}

// Bans holds the list of bans
type Bans struct {
	Bans []Ban `yaml:"bans"`
}

func getConf() *Config {
	c := &Config{}
	osUser, err := user.Current()
	if err != nil {
		panic(err)
	}
	userHome := osUser.HomeDir

	configDir := fmt.Sprintf("%s%s.girc%s", userHome, string(os.PathSeparator), string(os.PathSeparator))
	if _, err := os.Stat(configDir); os.IsNotExist(err) {
		os.Mkdir(configDir, os.ModeDir)
	}
	confPath := fmt.Sprintf("%sconf.yaml", configDir)
	if _, err := os.Stat(confPath); os.IsNotExist(err) {
		defaultConfig := `server:
    host: "localhost"
    port: "6665"
    name: "daniels server"
    motd: "Find out more on github.com/tehcyx/girc"
    debug: false  # Enable debug logging (shows detailed protocol messages)
redis:
    enabled: false
    url: "redis://localhost:6379"
    pod_id: ""  # Auto-generated if empty
`
		ioutil.WriteFile(confPath, []byte(defaultConfig), os.ModeAppend)
	}

	yamlFile, err := ioutil.ReadFile(confPath)
	if err != nil {
		log.Printf("yamlFile.Get err   #%v ", err)
	}
	err = yaml.Unmarshal(yamlFile, c)
	if err != nil {
		log.Fatalf("Unmarshal: %v", err)
	}
	return c
}

func init() {
	Values = getConf()
}

// LoadUsers loads the users from the configured users file
func LoadUsers() (*Users, error) {
	osUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}
	userHome := osUser.HomeDir

	// Default to users.yaml in .girc directory
	usersFile := Values.Auth.UsersFile
	if usersFile == "" {
		usersFile = fmt.Sprintf("%s%s.girc%susers.yaml", userHome, string(os.PathSeparator), string(os.PathSeparator))
	}

	// Create default users file if it doesn't exist
	if _, err := os.Stat(usersFile); os.IsNotExist(err) {
		defaultUsers := &Users{
			Users: []User{
				{
					Username: "admin",
					Password: "admin", // In production, use hashed passwords
					IsOper:   true,
					Channels: []string{},
				},
			},
		}
		if err := SaveUsers(defaultUsers, usersFile); err != nil {
			return nil, fmt.Errorf("failed to create default users file: %w", err)
		}
	}

	// Read users file
	data, err := ioutil.ReadFile(usersFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read users file: %w", err)
	}

	users := &Users{}
	if err := yaml.Unmarshal(data, users); err != nil {
		return nil, fmt.Errorf("failed to parse users file: %w", err)
	}

	return users, nil
}

// SaveUsers saves users to the configured users file
func SaveUsers(users *Users, filepath string) error {
	data, err := yaml.Marshal(users)
	if err != nil {
		return fmt.Errorf("failed to marshal users: %w", err)
	}

	if err := ioutil.WriteFile(filepath, data, 0600); err != nil {
		return fmt.Errorf("failed to write users file: %w", err)
	}

	return nil
}

// LoadBans loads the bans from the bans file
func LoadBans() (*Bans, error) {
	osUser, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("failed to get current user: %w", err)
	}
	userHome := osUser.HomeDir

	bansFile := fmt.Sprintf("%s%s.girc%sbans.yaml", userHome, string(os.PathSeparator), string(os.PathSeparator))

	// Create empty bans file if it doesn't exist
	if _, err := os.Stat(bansFile); os.IsNotExist(err) {
		defaultBans := &Bans{Bans: []Ban{}}
		if err := SaveBans(defaultBans, bansFile); err != nil {
			return nil, fmt.Errorf("failed to create default bans file: %w", err)
		}
	}

	// Read bans file
	data, err := ioutil.ReadFile(bansFile)
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

	if err := ioutil.WriteFile(filepath, data, 0600); err != nil {
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

// IsBanned checks if a user mask is banned (either server-wide or in a specific channel)
// mask should be in format: nick!user@host
// channel should be the channel name (or empty string to check only server-wide bans)
func IsBanned(mask, channel string, bans *Bans) (bool, *Ban) {
	for i := range bans.Bans {
		ban := &bans.Bans[i]

		// Check if this ban applies (server-wide or channel-specific)
		if ban.Channel != "" && ban.Channel != channel {
			continue // This ban is for a different channel
		}

		// Check if mask matches the ban pattern
		if matchBanMask(mask, ban.Mask) {
			return true, ban
		}
	}
	return false, nil
}

// matchBanMask checks if a user mask matches a ban mask pattern
// Supports wildcards: * (any characters) and ? (single character)
// Example patterns: *!*@badhost.com, baduser!*@*, nick!user@host
func matchBanMask(userMask, banPattern string) bool {
	return wildcardMatch(userMask, banPattern)
}

// wildcardMatch performs wildcard pattern matching
func wildcardMatch(text, pattern string) bool {
	// Simple implementation - in production, use a more robust glob matcher
	if pattern == "*" {
		return true
	}

	// Convert pattern to simple matching
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

	// Handle remaining wildcards in pattern
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}

	return pi == len(pattern)
}
