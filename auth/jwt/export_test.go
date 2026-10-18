package jwt

// ValidateConfig exposes validateConfig to the external test package, which
// used to call the exported Config.Validate it replaced.
var ValidateConfig = validateConfig
