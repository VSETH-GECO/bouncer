// Package config loads bouncer's configuration from flags, environment variables and the config file.
package config

import (
	"fmt"

	"github.com/VSETH-GECO/bouncer/internal/db"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Config is everything bouncer reads from flags, environment variables and the config file. The mapstructure keys
// are the flag names and config keys; they are part of the deployment interface and must not change.
type Config struct {
	Verbose         bool   `mapstructure:"verbose"`
	ConfigLocation  string `mapstructure:"config"`
	SwitchCOASecret string `mapstructure:"switch-secret"`

	DB      DB      `mapstructure:",squash"`
	Discord Discord `mapstructure:",squash"`
	Run     Run     `mapstructure:",squash"`
	Migrate Migrate `mapstructure:",squash"`
}

// DB holds the database connection settings.
type DB struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	Database string `mapstructure:"database"`
}

// Config returns the settings in the form internal/db expects.
func (c DB) Config() db.Config {
	return db.Config{Host: c.Host, Port: c.Port, User: c.User, Password: c.Password, Database: c.Database}
}

// Discord holds the Discord bot settings. The bot is only started when all of them except Users are set.
type Discord struct {
	Token              string   `mapstructure:"dtoken"`
	Users              []string `mapstructure:"dusers"`
	LogChannel         string   `mapstructure:"dlogchan"`
	PrivateInfoChannel string   `mapstructure:"dprivatechan"`
	GuildID            string   `mapstructure:"dguild"`
}

// Run holds the settings of the run command.
type Run struct {
	LeaderElect    bool `mapstructure:"leader-elect"`
	Prometheus     bool `mapstructure:"usePrometheus"`
	PrometheusPort int  `mapstructure:"prometheusPort"`
}

// Migrate holds the settings of the migrate command.
type Migrate struct {
	Force int `mapstructure:"force"`
}

// Loader registers bouncer's flags and loads a Config from them, the environment and the config file. Precedence is
// viper's: explicitly set flag > environment variable > config file > flag default.
type Loader struct {
	v *viper.Viper
	// flags lists every flag registered per key. A key normally has one flag; switch-secret has two (root and run).
	flags map[string][]*pflag.Flag
}

// NewLoader returns a Loader with its own viper instance that consults environment variables.
func NewLoader() *Loader {
	v := viper.New()
	v.AutomaticEnv()
	return &Loader{v: v, flags: map[string][]*pflag.Flag{}}
}

func (l *Loader) bind(fs *pflag.FlagSet, name string) {
	f := fs.Lookup(name)
	l.flags[name] = append(l.flags[name], f)
	_ = l.v.BindPFlag(name, f)
}

// RegisterGlobalFlags registers the root command's flags on fs.
func (l *Loader) RegisterGlobalFlags(fs *pflag.FlagSet) {
	fs.Bool("verbose", true, "Output verbose log messages")
	l.bind(fs, "verbose")
	fs.String("database", "", "Database to use")
	l.bind(fs, "database")
	fs.String("host", "", "Database host")
	l.bind(fs, "host")
	fs.String("user", "", "Database user")
	l.bind(fs, "user")
	fs.String("password", "", "Database password")
	l.bind(fs, "password")
	fs.Int("port", 3306, "Database port")
	l.bind(fs, "port")
	fs.String("dtoken", "", "Discord bot token")
	l.bind(fs, "dtoken")
	fs.StringArray("dusers", []string{}, "Discord bot user whitelist")
	l.bind(fs, "dusers")
	fs.String("dlogchan", "", "Discord log channel")
	l.bind(fs, "dlogchan")
	fs.String("dprivatechan", "", "Discord private info channel")
	l.bind(fs, "dprivatechan")
	fs.String("dguild", "", "Discord guild ID (aka Server ID)")
	l.bind(fs, "dguild")
	fs.String("config", "", "Extra config file location to check first")
	l.bind(fs, "config")
	fs.String("switch-secret", "", "RADIUS CoA secret to use with switches")
	l.bind(fs, "switch-secret")
}

// RegisterRunFlags registers the run command's flags on fs.
func (l *Loader) RegisterRunFlags(fs *pflag.FlagSet) {
	fs.BoolP("leader-elect", "l", true, "Use leader-election via SQL database")
	l.bind(fs, "leader-elect")
	// Shadows the root --switch-secret on run (keeping its -s shorthand and help text); both feed the same key.
	fs.StringP("switch-secret", "s", "", "Secret to use when authenticating to switches")
	l.bind(fs, "switch-secret")
	fs.Bool("usePrometheus", true, "Whether to enable usePrometheus exporter")
	l.bind(fs, "usePrometheus")
	fs.Int("prometheusPort", 2112, "Which port to bind the prometheus server on")
	l.bind(fs, "prometheusPort")
}

// RegisterMigrateFlags registers the migrate command's flags on fs.
func (l *Loader) RegisterMigrateFlags(fs *pflag.FlagSet) {
	fs.IntP("force", "f", 0, "Forcibly set version of DB to this version")
	l.bind(fs, "force")
}

// Load reads the config file and returns the merged configuration. The config file is "config" in /etc/bouncer, or
// the file given by the config key. A missing or unreadable config file is logged as a warning, not an error.
func (l *Loader) Load() (Config, error) {
	// A key with several flags (switch-secret) takes its value from the one that was set on the command line
	for name, flags := range l.flags {
		for _, f := range flags {
			if f.Changed {
				_ = l.v.BindPFlag(name, f)
				break
			}
		}
	}

	l.v.SetConfigName("config")
	l.v.AddConfigPath("/etc/bouncer")
	if location := l.v.GetString("config"); location != "" {
		log.WithField("config", location).Info("Loading config from explicit location")
		l.v.SetConfigFile(location)
	}
	if err := l.v.ReadInConfig(); err != nil {
		log.WithError(err).Warn("Config load failed!")
	}

	var cfg Config
	if err := l.v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("decoding configuration: %w", err)
	}
	return cfg, nil
}
