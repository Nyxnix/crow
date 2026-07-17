package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Nyxnix/crow/internal/auth"
	"github.com/Nyxnix/crow/internal/twitch"
)

// login runs the device code flow interactively and stores the token, so mod
// actions work on the next `crow -channel ...` run.
func login(ctx context.Context) error {
	ac := &auth.Client{ClientID: clientID()}

	dc, err := ac.RequestDeviceCode(ctx)
	if err != nil {
		return fmt.Errorf("requesting device code: %w", err)
	}

	fmt.Println()
	fmt.Println("  To authorize Crow, open this page and enter the code:")
	fmt.Println()
	fmt.Printf("    %s\n", dc.VerificationURI)
	fmt.Printf("    code: %s\n", dc.UserCode)
	fmt.Println()
	fmt.Println("  Waiting for you to approve in the browser...")

	tok, err := ac.PollToken(ctx, dc)
	if err != nil {
		return err
	}

	// Identify who just logged in; this also proves the token works before we
	// save it, so a broken token never lands on disk.
	id, name, err := twitch.Whoami(ctx, clientID(), tok.AccessToken, nil)
	if err != nil {
		return fmt.Errorf("verifying token: %w", err)
	}

	if err := auth.Save(&auth.StoredToken{Token: *tok, UserID: id, Login: name}); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}

	fmt.Printf("\n  Logged in as %s. Token saved to %s\n\n", name, auth.Path())
	return nil
}

// logout deletes the stored token.
func logout() error {
	if err := auth.Clear(); err != nil {
		return err
	}
	fmt.Println("Logged out.")
	return nil
}

// whoami prints the current login, if any.
func whoami() error {
	st, err := auth.Load()
	if err != nil {
		return err
	}
	if st == nil {
		fmt.Println("Not logged in. Run: crow login")
		return nil
	}
	fmt.Printf("Logged in as %s (id %s)\n", st.Login, st.UserID)
	if st.Expired() {
		fmt.Fprintln(os.Stderr, "Access token expired; it will refresh on next run.")
	}
	return nil
}
