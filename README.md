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

Working for Twitch. See the roadmap below.

- [x] Twitch IRC client (tags, badges, emotes, reconnect)
- [x] Overlay server (jChat-style, SSE)
- [x] TUI (colors, role markers, CJK-aware wrapping, scrollback)
- [x] Clickable usernames and the user card
- [x] Moderation actions (login, ban/timeout/unban/delete)
- [x] Sending messages (type in the TUI, echoes to chat and overlay)
- [x] Timestamps in the TUI
- [x] Third-party emotes (7TV, BTTV, FFZ) in the overlay
- [ ] Third-party emotes rendered inline in the TUI
- [ ] Settings / config file
- [ ] YouTube

## Login

Moderation needs a Twitch login. Reading chat and the overlay do not.

```sh
typetype login     # opens a device-code flow: approve in your browser
typetype whoami    # show the current login
typetype logout
```

The token is stored owner-only under your OS config dir
(`~/Library/Application Support/typetype` on macOS,
`~/.config/typetype` on Linux). Twitch expires the refresh token after
30 days idle; if that happens, run `login` again.

## Keys

When logged in, the input line at the bottom is focused, so letters type.

| Key | Action |
|-----|--------|
| type + `Enter` | send a message |
| mouse wheel / `PgUp` / `PgDn` / `↑` / `↓` | scroll chat |
| click a username | open the moderation card |
| `1`–`5` | timeout presets (in the card) |
| `b` / `u` / `d` | ban / unban / delete message (in the card) |
| `Esc` | close the card, or jump back to live |
| `Ctrl+C` | quit |

Not logged in, chat is read-only: `j`/`k`/`g`/`G`/`PgUp`/`PgDn` scroll and
`q` quits.

## Install

Requires Go 1.24+.

```sh
go install github.com/Nyxnix/typetype/cmd/typetype@latest
typetype -channel <name>
```

Then add a browser source in OBS pointing at `http://127.0.0.1:7788`.

## License

MIT
