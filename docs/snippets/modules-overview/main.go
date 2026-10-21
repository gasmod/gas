// Package main shows how Gas modules meet through core's provider
// interfaces.
package main

// #region imports
import (
	"context"
	"log"
	"time"

	"github.com/gasmod/gas"
	cachemem "github.com/gasmod/gas/cache/memory"
	"github.com/gasmod/gas/config"
	"github.com/gasmod/gas/database"
	emailses "github.com/gasmod/gas/email/ses"
	gaslog "github.com/gasmod/gas/log"
	"github.com/gasmod/gas/migrate"
	templatememory "github.com/gasmod/gas/template/memory"
)

// #endregion imports

func main() {
	cfg := config.New()
	if err := cfg.Load(); err != nil {
		log.Fatal(err)
	}

	// #region wiring
	app := gas.NewApp(
		// Every module registers under a core interface. The container
		// hands each constructor the interfaces it asks for, whichever
		// implementation provides them.
		gas.WithServiceInstance[gas.ConfigProvider](cfg),
		gas.WithSingletonService[gas.Logger](gaslog.NewSlogLogger()),
		gas.WithSingletonService[gas.DatabaseProvider](database.New()),
		gas.WithSingletonService[gas.MigrationManager](migrate.New()),
		gas.WithSingletonService[gas.CacheProvider](cachemem.New()),
		gas.WithServiceInstance[gas.TemplateProvider](templatememory.NewStore()),
		gas.WithSingletonService[gas.EmailProvider](emailses.New()),

		gas.WithSingletonService[*Signups](NewSignups),
	)
	// #endregion wiring

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// #region service
// Signups knows only core interfaces. Swapping the cache backend, the email
// sender or the template store, or passing mocks in a test, changes nothing
// here.
type Signups struct {
	cache gas.CacheProvider
	email gas.EmailProvider
}

func NewSignups(cache gas.CacheProvider, email gas.EmailProvider) *Signups {
	return &Signups{cache: cache, email: email}
}

func (s *Signups) Welcome(ctx context.Context, to string) error {
	if err := s.email.Send(ctx, &gas.Email{
		To:       []string{to},
		Subject:  "Welcome",
		TextBody: "Thanks for signing up.",
	}); err != nil {
		return err
	}
	return s.cache.Set(ctx, "welcomed:"+to, []byte("1"), 24*time.Hour)
}

// #endregion service
