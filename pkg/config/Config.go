package config

import (
	log "github.com/sirupsen/logrus"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// GlobalOptions contains the list of global options
type GlobalOptions struct {
	Verbose                   bool
	ConfigLocation            string
	DBHost                    string
	DBPort                    int
	DBUser                    string
	DBPassword                string
	DBDatabase                string
	DiscordToken              string
	DiscordUsers              []string
	DiscordLogChannel         string
	DiscordPrivateInfoChannel string
	DiscordGuildID            string
	SwitchCOASecret           string
}

var (
	// CurrentOptions provides storage for global options
	CurrentOptions = GlobalOptions{}

	// The other arrays simply store setup references so that we can later store values from the config back into the
	// struct

	stringParams    []container[string]
	stringArrParams []container[[]string]
	intParams       []container[int]
	boolParams      []container[bool]
)

type container[T any] struct {
	name        string
	destination *T
}

func registerString(flags *pflag.FlagSet, name string, defaultVal string, description string, storage *string) {
	flags.StringVar(storage, name, defaultVal, description)
	_ = viper.BindPFlag(name, flags.Lookup(name))
	if stringParams == nil {
		stringParams = []container[string]{}
	}
	stringParams = append(stringParams, container[string]{
		name:        name,
		destination: storage,
	})
}

func registerStringArray(flags *pflag.FlagSet, name string, defaultVal []string, description string, storage *[]string) {
	flags.StringArrayVar(storage, name, defaultVal, description)
	_ = viper.BindPFlag(name, flags.Lookup(name))
	if stringArrParams == nil {
		stringArrParams = []container[[]string]{}
	}
	stringArrParams = append(stringArrParams, container[[]string]{
		name:        name,
		destination: storage,
	})
}

func registerInt(flags *pflag.FlagSet, name string, defaultVal int, description string, storage *int) {
	flags.IntVar(storage, name, defaultVal, description)
	_ = viper.BindPFlag(name, flags.Lookup(name))
	if intParams == nil {
		intParams = []container[int]{}
	}
	intParams = append(intParams, container[int]{
		name:        name,
		destination: storage,
	})
}

func registerBool(flags *pflag.FlagSet, name string, defaultVal bool, description string, storage *bool) {
	flags.BoolVar(storage, name, defaultVal, description)
	_ = viper.BindPFlag(name, flags.Lookup(name))
	if boolParams == nil {
		boolParams = []container[bool]{}
	}
	boolParams = append(boolParams, container[bool]{
		name:        name,
		destination: storage,
	})
}

// writebackConfig is used to copy viper's properties back into the struct - that way, the rest of the code does not
// need to be aware of viper
func writebackConfig() {
	for _, param := range stringParams {
		*param.destination = viper.GetString(param.name)
	}
	for _, param := range stringArrParams {
		*param.destination = viper.GetStringSlice(param.name)
	}
	for _, param := range intParams {
		*param.destination = viper.GetInt(param.name)
	}
	for _, param := range boolParams {
		*param.destination = viper.GetBool(param.name)
	}
}

func RegisterGlobalArguments(flags *pflag.FlagSet) {
	viper.AutomaticEnv()
	registerBool(flags, "verbose", true, "Output verbose log messages", &CurrentOptions.Verbose)
	registerString(flags, "database", "", "Database to use", &CurrentOptions.DBDatabase)
	registerString(flags, "host", "", "Database host", &CurrentOptions.DBHost)
	registerString(flags, "user", "", "Database user", &CurrentOptions.DBUser)
	registerString(flags, "password", "", "Database password", &CurrentOptions.DBPassword)
	registerInt(flags, "port", 3306, "Database port", &CurrentOptions.DBPort)
	registerString(flags, "dtoken", "", "Discord bot token", &CurrentOptions.DiscordToken)
	registerStringArray(flags, "dusers", []string{}, "Discord bot user whitelist", &CurrentOptions.DiscordUsers)
	registerString(flags, "dlogchan", "", "Discord log channel", &CurrentOptions.DiscordLogChannel)
	registerString(flags, "dprivatechan", "", "Discord private info channel", &CurrentOptions.DiscordPrivateInfoChannel)
	registerString(flags, "dguild", "", "Discord guild ID (aka Server ID)", &CurrentOptions.DiscordGuildID)
	registerString(flags, "config", "", "Extra config file location to check first", &CurrentOptions.ConfigLocation)
	registerString(flags, "switch-secret", "", "RADIUS CoA secret to use with switches", &CurrentOptions.SwitchCOASecret)
}

// LoadConfig tries to load the global configuration
func LoadConfig() {
	if CurrentOptions.Verbose {
		log.SetLevel(log.DebugLevel)
	}

	viper.SetConfigName("config")
	viper.AddConfigPath("/etc/bouncer")
	if CurrentOptions.ConfigLocation != "" {
		log.WithFields(log.Fields{
			"config": CurrentOptions.ConfigLocation,
		}).Info("Loading config from explicit location")
		viper.SetConfigFile(CurrentOptions.ConfigLocation)
	}
	err := viper.ReadInConfig()
	if err != nil {
		log.WithError(err).Warn("Config load failed!")
	} else {
		writebackConfig()
	}
}
