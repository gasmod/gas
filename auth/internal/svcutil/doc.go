// Package svcutil holds the plumbing the auth module's services share:
// resolving and checking a service's configuration in Init, and running a
// background task, such as expiry cleanup, between Init and Close.
//
// # Usage
//
// A service keeps a [ConfigSource] and a [Loop]. Its WithConfig option
// records the configuration, its Init resolves it and starts the loop,
// and its Close stops the loop:
//
//	type Service struct {
//		cfgSource svcutil.ConfigSource[Config]
//		cleanup   svcutil.Loop
//		// ...
//	}
//
//	func WithConfig(cfg *Config) Option {
//		return func(s *Service) { s.cfgSource.Set(cfg) }
//	}
//
//	func (s *Service) Init(context.Context) error {
//		cfg, err := s.cfgSource.Resolve(s.cfgProvider, DefaultConfig, checkRules)
//		if err != nil {
//			return fmt.Errorf("%s: %w", serviceName, err)
//		}
//		s.cfg = cfg
//		s.cleanup.Start(svcutil.Task{
//			Name:     "session cleanup",
//			Interval: cfg.Session.CleanupInterval,
//			Timeout:  cfg.Session.CleanupTimeout,
//			Logger:   s.logger,
//			Run:      s.deleteExpired,
//		})
//		return nil
//	}
//
//	func (s *Service) Close(context.Context) error {
//		s.cleanup.Stop()
//		return nil
//	}
//
// # Configuration
//
// [ConfigSource.Set] records the configuration given to WithConfig, and
// only records it, so the option stays a plain assignment.
// [ConfigSource.Resolve] then returns a new *T, never one the caller
// holds:
//
//   - After Set with a non-nil cfg, a shallow copy of *cfg, checked
//     against its `validate` tags with a validator.New() validator. Later
//     changes to the caller's struct do not reach the copy.
//   - After Set(nil), the error [ErrNilConfig].
//   - Without Set, the result of the defaults function, bound from the
//     [Binder] (a gas.ConfigProvider), so a key the configuration does
//     not set keeps its default. Bind validates the tags itself, with the
//     configuration's own validator, so Resolve does not check them a
//     second time. A nil Binder is the error [ErrNoConfigProvider].
//
// Either way, the rules function then runs on the result, when it is not
// nil, for the checks a tag cannot express. Resolve returns the
// configuration only when every check passes. Each call resolves afresh,
// so a service that is initialized again picks up a changed
// configuration.
//
// [CheckCleanup] is the rule the services with a cleanup share, for their
// rules function: a zero interval turns the cleanup off, a negative one
// is [ErrNegativeCleanupInterval], and a positive one with a timeout that
// is not positive is [ErrCleanupTimeout]. Returned from the rules, either
// error reaches the caller of Resolve wrapped with ErrInvalidConfig.
//
// # Background tasks
//
// [Loop.Start] starts a goroutine that calls [Task].Run once every
// Task.Interval, the first time one interval after Start. Each run gets a
// context that ends after Task.Timeout, when it is positive, or when the
// Loop stops. Runs never overlap. When a run takes longer than the
// interval, the ticks that come due meanwhile collapse into at most one,
// so the next run may start as soon as it returns, and no backlog builds
// up. A run that returns an error is logged with
// Task.Logger at warn level, with the message `background task failed`,
// the task's name under `task` and the error under `error`, and the loop
// goes on. A run that fails because the Loop is stopping is not logged.
// A non-positive Interval starts nothing, which is how a service turns its
// cleanup off. Start checks the Interval first, so such a task may have a
// nil Run and Logger; any other task with either nil makes Start panic,
// before it stops a running task.
//
// [Loop.Stop] cancels the running task's context and waits for its
// goroutine to return, a run in progress included, so once Stop returns
// nothing of the task runs, and the service may close what the task used.
// Stop on a Loop that is not running does nothing, so Close may call it
// whether or not Init ran. Start on a running Loop stops it first, the
// same way, so a second Init never leaves two goroutines. A Loop may be
// started again after Stop, any number of times; each Start makes a new
// context. [Loop.Running] reports whether a goroutine runs.
//
// Task.Run must not call the Loop's Start or Stop, which wait for it. A
// panic in Task.Run is not recovered.
//
// # Concurrency
//
// A Loop is safe for concurrent use; its Start and Stop run one at a time.
// A ConfigSource is not: Set belongs in an option, before Init, and
// Resolve in Init, which the container does not run concurrently with
// itself.
//
// # Errors
//
// Resolve returns an error wrapping one of the package's sentinel errors,
// to be tested with [errors.Is]:
//
//   - [ErrNilConfig]: WithConfig was given nil.
//   - [ErrNoConfigProvider]: there is neither a given configuration nor a
//     Binder.
//   - [ErrInvalidConfig]: the configuration failed to bind, failed its
//     tags, or broke a rule. The Binder's, the validator's or the rule's
//     error is wrapped too, so [errors.As] finds
//     validator.ValidationErrors.
//
// [ErrNotInitialized] is for services to return from a method called
// before Init has succeeded, and [ErrNilLogger] from a constructor given a
// nil logger; nothing in this package returns them.
// [ErrNegativeCleanupInterval] and [ErrCleanupTimeout] come from
// CheckCleanup.
//
// None of these errors includes a configuration value, as long as the
// Binder's and the rules' errors do not: the validator's errors name the
// field and the tag, not the value. Services prefix their name:
//
//	config is nil
//	invalid config: validate config: Key: 'Config.JWT.Expiry' Error:Field validation for 'Expiry' failed on the 'gt' tag
//	invalid config: cleanup interval must not be negative
//	invalid config: cleanup timeout must be positive when cleanup is enabled
//	logger is nil
package svcutil
