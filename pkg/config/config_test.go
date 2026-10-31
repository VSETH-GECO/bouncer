package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/spf13/pflag"
)

// envKeys are the environment variables AutomaticEnv consults for bouncer's keys. They are cleared in every test so
// that the developer's environment (USER, for instance) does not leak in; viper ignores empty variables.
var envKeys = []string{
	"VERBOSE", "DATABASE", "HOST", "USER", "PASSWORD", "PORT", "DTOKEN", "DUSERS", "DLOGCHAN", "DPRIVATECHAN",
	"DGUILD", "CONFIG", "SWITCH-SECRET", "LEADER-ELECT", "USEPROMETHEUS", "PROMETHEUSPORT", "FORCE", "RECONCILE-DRY-RUN",
}

func clearEnv(t *testing.T) {
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
}

// equal reports a test error naming what when got differs from want.
func equal[T any](t *testing.T, what string, got, want T) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %#v, want %#v", what, got, want)
	}
}

// load registers all flags with a fresh Loader the way cmd/bouncer does (global flags on the root flag set, the
// subcommand flags on their own sets), parses the arguments into the root, run and migrate sets and loads.
func load(t *testing.T, rootArgs, runArgs, migrateArgs []string) Config {
	t.Helper()
	l := NewLoader()
	sets := []*pflag.FlagSet{
		pflag.NewFlagSet("bouncer", pflag.ContinueOnError),
		pflag.NewFlagSet("run", pflag.ContinueOnError),
		pflag.NewFlagSet("migrate", pflag.ContinueOnError),
	}
	l.RegisterGlobalFlags(sets[0])
	l.RegisterRunFlags(sets[1])
	l.RegisterMigrateFlags(sets[2])
	for i, args := range [][]string{rootArgs, runArgs, migrateArgs} {
		if err := sets[i].Parse(args); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := l.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// missingConfig returns a --config argument naming a file that does not exist
func missingConfig(t *testing.T) string {
	return "--config=" + filepath.Join(t.TempDir(), "missing.yml")
}

const testConfigFile = `host: db.example
port: 13306
user: bouncer-test
password: pw-test
database: radius-test
dtoken: tok-test
dusers:
  - user-a
  - user-b
dlogchan: log-test
dprivatechan: private-test
dguild: guild-test
switch-secret: secret-from-file
`

func writeConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bouncer.yml")
	if err := os.WriteFile(path, []byte(testConfigFile), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	clearEnv(t)
	cfg := load(t, []string{missingConfig(t)}, nil, nil)

	equal(t, "verbose", cfg.Verbose, true)
	equal(t, "port", cfg.DB.Port, 3306)
	equal(t, "host", cfg.DB.Host, "")
	equal(t, "dusers", len(cfg.Discord.Users), 0)
	equal(t, "switch-secret", cfg.SwitchCOASecret, "")
	equal(t, "run", cfg.Run, Run{LeaderElect: true, Prometheus: true, PrometheusPort: 2112, ReconcileDryRun: true})
	equal(t, "force", cfg.Migrate.Force, 0)
}

func TestConfigFile(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t)
	cfg := load(t, []string{"--config", path}, nil, nil)

	equal(t, "config", cfg.ConfigLocation, path)
	equal(t, "db", cfg.DB, DB{Host: "db.example", Port: 13306, User: "bouncer-test", Password: "pw-test", Database: "radius-test"})
	equal(t, "discord", cfg.Discord, Discord{
		Token:              "tok-test",
		Users:              []string{"user-a", "user-b"},
		LogChannel:         "log-test",
		PrivateInfoChannel: "private-test",
		GuildID:            "guild-test",
	})
	equal(t, "switch-secret", cfg.SwitchCOASecret, "secret-from-file")
	dbc := cfg.DB.Config()
	equal(t, "db.Config", [5]any{dbc.Host, dbc.Port, dbc.User, dbc.Password, dbc.Database},
		[5]any{"db.example", 13306, "bouncer-test", "pw-test", "radius-test"})
}

func TestFlagOverridesFile(t *testing.T) {
	clearEnv(t)
	cfg := load(t, []string{"--config", writeConfig(t), "--host", "10.20.0.1", "--dusers", "user-c", "--verbose=false"},
		[]string{"--prometheusPort", "9100"}, nil)

	equal(t, "host", cfg.DB.Host, "10.20.0.1")
	equal(t, "dusers", cfg.Discord.Users, []string{"user-c"})
	equal(t, "verbose", cfg.Verbose, false)
	equal(t, "prometheusPort", cfg.Run.PrometheusPort, 9100)
	equal(t, "password (not set by a flag, so from the file)", cfg.DB.Password, "pw-test")
}

func TestEnvironmentBetweenFileAndFlag(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t)
	t.Setenv("HOST", "10.20.0.2")
	t.Setenv("PORT", "23306")

	cfg := load(t, []string{"--config", path}, nil, nil)
	equal(t, "host (environment over file)", cfg.DB.Host, "10.20.0.2")
	equal(t, "port (environment over file)", cfg.DB.Port, 23306)

	cfg = load(t, []string{"--config", path, "--host", "10.20.0.1"}, nil, nil)
	equal(t, "host (flag over environment)", cfg.DB.Host, "10.20.0.1")
	equal(t, "port (environment over file)", cfg.DB.Port, 23306)
}

func TestMissingFileKeepsDefaultsAndAppliesEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("HOST", "10.20.0.3")
	t.Setenv("USEPROMETHEUS", "false")

	cfg := load(t, []string{missingConfig(t)}, nil, nil)
	equal(t, "host", cfg.DB.Host, "10.20.0.3")
	equal(t, "usePrometheus", cfg.Run.Prometheus, false)
	equal(t, "port", cfg.DB.Port, 3306)
	equal(t, "leader-elect", cfg.Run.LeaderElect, true)
}

func TestSwitchSecretFlags(t *testing.T) {
	clearEnv(t)
	path := writeConfig(t)

	cfg := load(t, []string{"--config", path}, []string{"-s", "secret-run-short"}, nil)
	equal(t, "switch-secret from run's shadowing -s flag", cfg.SwitchCOASecret, "secret-run-short")

	cfg = load(t, []string{"--config", path, "--switch-secret", "secret-root"}, nil, nil)
	equal(t, "switch-secret from the root flag", cfg.SwitchCOASecret, "secret-root")

	cfg = load(t, []string{missingConfig(t)}, []string{"--switch-secret", "secret-no-file"}, nil)
	equal(t, "switch-secret from run's flag without a config file", cfg.SwitchCOASecret, "secret-no-file")
}

func TestForceFlag(t *testing.T) {
	clearEnv(t)
	cfg := load(t, []string{missingConfig(t)}, nil, []string{"-f", "7"})
	equal(t, "force", cfg.Migrate.Force, 7)
}

// The deployment hands the Discord user list over as one space-separated environment variable (see the k8s chart's
// ConfigMap), which the old viper slice getter split on whitespace; Unmarshal must do the same.
func TestUsersFromEnvironmentSplitOnWhitespace(t *testing.T) {
	clearEnv(t)
	t.Setenv("DUSERS", "111 222  333")
	cfg := load(t, []string{missingConfig(t)}, nil, nil)
	equal(t, "dusers", cfg.Discord.Users, []string{"111", "222", "333"})
}

func TestReconcileDryRunFlag(t *testing.T) {
	clearEnv(t)
	cfg := load(t, []string{missingConfig(t)}, nil, nil)
	equal(t, "reconcile-dry-run default", cfg.Run.ReconcileDryRun, true)

	cfg = load(t, []string{missingConfig(t)}, []string{"--reconcile-dry-run=false"}, nil)
	equal(t, "reconcile-dry-run from the flag", cfg.Run.ReconcileDryRun, false)

	path := filepath.Join(t.TempDir(), "bouncer.yml")
	if err := os.WriteFile(path, []byte("reconcile-dry-run: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = load(t, []string{"--config", path}, nil, nil)
	equal(t, "reconcile-dry-run from the config file", cfg.Run.ReconcileDryRun, false)
}
