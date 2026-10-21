// Package ses sends email through AWS SES for a Gas app, either as a message
// the caller writes or rendered from templates the app's
// gas.TemplateProvider supplies.
//
// # Role in the framework
//
// Service implements the core interfaces gas.Service, gas.EmailProvider and
// gas.ReadyReporter. It depends on core's gas.TemplateProvider for template
// content, so any template backend works. Client returns the underlying
// *ses.Client for calls beyond gas.EmailProvider; it returns nil when
// WithClient injected a client of another type.
//
// # Wiring
//
// New takes Options (WithConfig, WithClient) and returns a constructor that
// the DI container calls with gas.TemplateProvider, gas.ConfigProvider and
// gas.Logger. NewWithCustomProvider does the same but asks the container
// for a concrete template provider type T instead of gas.TemplateProvider.
// The app registers one of these constructors; the package registers
// nothing itself.
//
// # Configuration
//
// Unless WithConfig is given, Init binds Config from the gas.ConfigProvider.
// The keys sit under email: region (required), fromemail (required; the
// sender when a message has no From), endpoint (optional custom endpoint,
// for example LocalStack; empty means the AWS default), and accesskeyid and
// secretaccesskey (static credentials, used only when both are set;
// otherwise the default AWS credential chain applies). There are no
// defaults. WithConfig replaces the whole Config and skips the provider.
// When Config is bound from the provider, Bind validates it against its
// validate tags, which require region and fromemail, and a binding or
// validation failure fails Init, even when WithClient supplies the client. A
// Config given with WithConfig is used as given, without validation.
//
// # Lifecycle
//
// Init runs at startup. It binds and validates the configuration and,
// unless WithClient was given, builds the SES client from the AWS default
// configuration (environment and shared config files), loaded under Init's
// context, and then fetches the credentials under the same context, so a
// missing or broken credential setup fails startup. It does not contact
// SES, so an unreachable endpoint or rejected credentials surface on the
// first send, not at startup.
//
// Close runs at shutdown. It only marks the Service closed: Send,
// SendFromTemplate and CheckReady then return email.ErrClosed, so a
// readiness probe takes the instance out of rotation while it drains. The
// Service starts no goroutines.
//
// # Sending
//
// Send passes the message to SES SendEmail with a UTF-8 subject, text body
// and HTML body, and at most one reply-to address. SendFromTemplate
// fetches each template the message names from the gas.TemplateProvider,
// renders the subject and text templates with text/template and the HTML
// template with html/template, writes the results into the message's
// Subject, TextBody and HTMLBody fields (so the caller's message is
// modified), then sends it as Send does. Templates are fetched and parsed
// on every call; nothing is cached.
//
// # Errors
//
// email.ErrClosed is the only sentinel. Other errors start with the service
// name, "gas/email/ses", and wrap the SDK's or the template provider's error
// with %w. Validation errors from Bind also name the offending field, for
// example Email.Region.
//
// SPDX-License-Identifier: MIT
package ses
