// Package main shows configuration loading, binding and environments with
// gas/config.
package main

// #region imports
import (
	"context"
	"embed"
	"fmt"
	"log"
	"time"

	"github.com/gasmod/gas"
	"github.com/gasmod/gas/config"
	gasenv "github.com/gasmod/gas/config/extensions/gasenv"
	"github.com/gasmod/gas/config/providers"
	"github.com/gasmod/gas/config/providers/secretsmanager"
	gaslog "github.com/gasmod/gas/log"
)

// #endregion imports

func main() {
	// #region wiring
	// Config is built and loaded before the app, so a bad configuration
	// fails before any service starts. Later providers override earlier ones.
	cfg := config.New(
		config.WithProvider(providers.NewJSONProvider(
			providers.WithJSONFilePath("config.json"),
		)),
		config.WithProvider(providers.NewEnvProvider()), // overrides the file
		config.WithExtension(gasenv.NewExtension()),
	)
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	app := gas.NewApp(
		// Registered as an instance under the interface every module binds
		// its own settings through.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[*Notes](NewNotes),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region env
// Only variables starting with APP_ are read, and the prefix is cut off
// literally, so APP_DATABASE__DSN becomes database.dsn.
func prefixedEnv() *providers.EnvProvider {
	return providers.NewEnvProvider(providers.WithEnvPrefix("APP_"))
}

// #endregion env

// #region dotenv
// Reads ./.env with the same naming rule as the environment. A missing file
// is an error unless WithDotEnvFileNotFoundPanic(false) is passed.
func localDotEnv() *providers.DotEnvProvider {
	return providers.NewDotEnvProvider(
		providers.WithDotEnvFileNotFoundPanic(false),
	)
}

// #endregion dotenv

// #region json-fs

//go:embed defaults.json
var defaultsFS embed.FS

// Reads defaults.json from the binary instead of the working directory.
func embeddedDefaults() *providers.JSONProvider {
	return providers.NewJSONProvider(
		providers.WithJSONFileFS(defaultsFS),
		providers.WithJSONFilePath("defaults.json"),
	)
}

// #endregion json-fs

// #region secrets
// Fetches secrets when the config loads. app/prod must hold a JSON object,
// merged at the root; app/db-password is placed at database.password.
func secrets() *secretsmanager.Provider {
	return secretsmanager.NewProvider(
		secretsmanager.WithRegion("eu-west-1"),
		secretsmanager.WithSecret("app/prod"),
		secretsmanager.WithSecretAtKey("app/db-password", "database.password"),
	)
}

// #endregion secrets

// #region bind
// NotesConfig is bound from the notes.* keys. Fields match their lowercased
// name: notes.pagesize, notes.timeout, notes.tags.
type NotesConfig struct {
	gas.WithGasEnv

	Notes struct {
		PageSize int `validate:"min=1,max=500"`
		Timeout  time.Duration
		Tags     []string
	}
}

// Notes binds its settings in Init, the way every Gas module does.
type Notes struct {
	cfgProvider gas.ConfigProvider
	cfg         NotesConfig
}

func NewNotes(cfgProvider gas.ConfigProvider) (*Notes, error) {
	return &Notes{cfgProvider: cfgProvider}, nil
}

func (n *Notes) Name() string { return "notes" }

func (n *Notes) Init(context.Context) error {
	// Defaults first: Bind sets only the fields whose keys are present.
	n.cfg.Notes.PageSize = 50
	n.cfg.Notes.Timeout = 5 * time.Second

	if err := n.cfgProvider.Bind(&n.cfg); err != nil {
		return fmt.Errorf("notes: %w", err)
	}
	if n.cfg.GasEnv.IsProduction() {
		n.cfg.Notes.Timeout = 2 * time.Second
	}
	return nil
}

func (n *Notes) Close(context.Context) error { return nil }

// #endregion bind

// #region gasenv
// Keep the extension to ask for the environment before the app exists.
func loadWithEnv() (*config.Config, *gasenv.Extension, error) {
	envExt := gasenv.NewExtension(
		gasenv.WithAllowedEnvs(gasenv.Development, gasenv.Production),
	)
	cfg := config.New(config.WithExtension(envExt))
	if err := cfg.Load(); err != nil {
		return nil, nil, err
	}
	if envExt.IsProduction() {
		cfg.Set("notes.timeout", "2s")
	}
	return cfg, envExt, nil
}

// #endregion gasenv

// #region extension
// stampExtension records when the configuration was loaded. PreLoad runs
// before any provider, PostLoad after all of them.
type stampExtension struct{}

func (stampExtension) Name() string { return "stamp" }

func (stampExtension) PreLoad(_ context.Context, cfg *config.Config) error {
	cfg.SetDefault("app.region", "eu-west-1")
	return nil
}

func (stampExtension) PostLoad(_ context.Context, cfg *config.Config) error {
	cfg.Set("app.loadedat", time.Now().UTC().Format(time.RFC3339))
	return nil
}

// #endregion extension

var _ config.Extension = stampExtension{}
