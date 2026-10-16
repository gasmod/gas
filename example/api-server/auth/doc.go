// Package auth implements the api-server example's accounts: user
// registration, login, which issues a JWT, and API key management. Its
// Service registers its own migrations, the public /api/auth/register and
// /api/auth/login routes, and the authenticated /api/auth/api-keys routes.
//
// Authentication tries a JWT first and then an API key, through an
// auth.Chain, and answers a failure with a JSON 401. Middleware returns that
// same middleware so the files and shares services can protect their own
// routes with it.
package auth
