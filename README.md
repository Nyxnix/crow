# TypeType

A terminal chat client for live streaming, with a built-in OBS overlay.

Two things in one binary:

- **A TUI** for reading and moderating your chat. Click a username to open a
  card showing that user's recent messages and the moderation actions you have
  permission to take against them.
- **An overlay server** at `http://127.0.0.1:7788` to point an OBS browser
  source at, rendering chat on stream with a transparent background.

Emotes from 7TV, BetterTTV and FrankerFaceZ render in both places — as images
in the overlay, and inline in the TUI on terminals that support graphics.

## Status

Early. Twitch chat reading works. See the roadmap below.

- [x] Twitch IRC client (tags, badges, emotes, reconnect)
- [ ] Overlay server
- [ ] TUI
- [ ] Clickable usernames and the user card
- [ ] Moderation actions
- [ ] Third-party emotes
- [ ] YouTube

## Install

Requires Go 1.24+.

```sh
go install github.com/Nyxnix/typetype/cmd/typetype@latest
```

## License

MIT
