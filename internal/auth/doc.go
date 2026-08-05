// Package auth resolves credentials for a site.
//
// It covers the credential providers — keyring, netrc, environment, OAuth,
// mTLS — behind one interface. A credential never leaves this package as a
// loggable value.
package auth
