package main

import "os"

// defaultClientID is Crow's registered Twitch application. It is a public
// client ID, not a secret: it appears in every OAuth request Twitch users
// already see, which is exactly why the device flow is safe to embed it in.
const defaultClientID = "dhvsh14mmhs15k1kpb15ede7cslbhy"

// clientID returns the Twitch client ID, letting a fork or self-hoster override
// the built-in one without recompiling.
func clientID() string {
	if id := os.Getenv("CROW_CLIENT_ID"); id != "" {
		return id
	}
	return defaultClientID
}
