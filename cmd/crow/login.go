package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Nyxnix/crow/internal/auth"
	"github.com/Nyxnix/crow/internal/config"
	"github.com/Nyxnix/crow/internal/twitch"
	"github.com/Nyxnix/crow/internal/youtube"
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

// loginYouTube runs Google's device flow and stores the token, so single
// YouTube tabs get a send box. Google issues no shared client for this, so on
// first run it collects the user's own OAuth client credentials and saves them
// to the config.
func loginYouTube(ctx context.Context) error {
	cfg := config.Load()
	if cfg.YouTubeClientID == "" || cfg.YouTubeClientSecret == "" {
		fmt.Println()
		fmt.Println("  Sending to YouTube needs your own (free) Google OAuth client:")
		fmt.Println()
		fmt.Println("    1. console.cloud.google.com -> create a project")
		fmt.Println("    2. APIs & Services -> Library -> enable \"YouTube Data API v3\"")
		fmt.Println("    3. OAuth consent screen -> External -> add yourself as a test user")
		fmt.Println("    4. Credentials -> Create credentials -> OAuth client ID")
		fmt.Println("       -> type \"TVs and Limited Input devices\"")
		fmt.Println()
		in := bufio.NewReader(os.Stdin)
		fmt.Print("  client id: ")
		id, _ := in.ReadString('\n')
		fmt.Print("  client secret: ")
		secret, _ := in.ReadString('\n')
		cfg.YouTubeClientID = strings.TrimSpace(id)
		cfg.YouTubeClientSecret = strings.TrimSpace(secret)
		if cfg.YouTubeClientID == "" || cfg.YouTubeClientSecret == "" {
			return fmt.Errorf("both client id and secret are required")
		}
		if err := config.Save(cfg); err != nil {
			return fmt.Errorf("saving config: %w", err)
		}
	}

	ya := &youtube.Auth{ClientID: cfg.YouTubeClientID, ClientSecret: cfg.YouTubeClientSecret}
	dc, err := ya.RequestDeviceCode(ctx)
	if err != nil {
		return fmt.Errorf("requesting device code: %w", err)
	}
	fmt.Println()
	fmt.Println("  To authorize Crow, open this page and enter the code:")
	fmt.Println()
	fmt.Printf("    %s\n", dc.VerificationURL)
	fmt.Printf("    code: %s\n", dc.UserCode)
	fmt.Println()
	fmt.Println("  Waiting for you to approve in the browser...")

	tok, err := ya.PollToken(ctx, dc)
	if err != nil {
		return err
	}
	// Identify the account; this also proves the token works before saving.
	name, err := ya.WhoAmI(ctx, tok.AccessToken)
	if err != nil {
		return fmt.Errorf("verifying token: %w", err)
	}
	if err := youtube.SaveToken(tok); err != nil {
		return fmt.Errorf("saving token: %w", err)
	}
	fmt.Printf("\n  Logged in to YouTube as %s. Token saved to %s\n\n", name, youtube.TokenPath())
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
