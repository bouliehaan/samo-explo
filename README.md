# samo-explo — weekly music discovery for [samo](https://github.com/bouliehaan/samo)

A fork of [LumePart/Explo](https://github.com/LumePart/Explo) with a native
[samo-server](https://github.com/bouliehaan/samo-server) client.

Every week it pulls your ListenBrainz recommendations, downloads what your
library is missing, and drops it where samo can find it. samo does the rest.

```sh
git clone https://github.com/bouliehaan/samo-explo.git
cd samo-explo
./deploy.sh
```

`deploy.sh` asks for your samo URL and login, mints a dedicated API token,
writes `.env` and `docker-compose.yaml`, and starts the container. Re-running it
is safe — it reuses the token it already made.

---

## Why the fork

samo speaks Subsonic, so upstream Explo *appears* to work against it. It does
not. samo's Subsonic surface is deliberately read-only for playlists: it
implements `getPlaylists` and `getPlaylist`, but not `createPlaylist`,
`updatePlaylist`, or `startScan`. Explo authenticates, downloads, and then fails
on the last step of every single run.

The `samo` client here talks to the native REST API instead, which has all
three. It also:

- **fails loudly on a bad token.** A wrong URL or an expired token stops the run
  at startup rather than degrading into a week that quietly downloads nothing.
- **skips what you already own.** `--download-mode=normal` searches samo's
  catalog, so a recommendation you already have is never re-downloaded.
- **scans only the drop folder.** It reads the server's own explo folder path
  and passes it as a scan subpath, so a drop is visible in seconds instead of
  after a full library walk.

## The two modes

**samo's explo folder is on** (Settings → Explo). This is the good one. samo
fingerprints every dropped file with AcoustID, applies real metadata and cover
art, keeps the drop out of Recently Added, and re-derives its system **Explore**
playlist from the folder on every pass. samo-explo detects this and does *not*
create a playlist of its own — a second one would sit next to samo's, going
stale while samo rewrites the real one. Downloading is the whole integration.

**samo's explo folder is off.** samo-explo falls back to creating an ordinary
samo playlist from the tracks it resolved. No fingerprinting, no cover art, and
the drops show up in Recently Added like any other import.

`deploy.sh` detects which one you are in and tells you.

## Configuration

Beyond the upstream variables, the samo client uses:

| Variable | Meaning |
| --- | --- |
| `EXPLO_SYSTEM=samo` | Selects this client |
| `SYSTEM_URL` | Your samo server, e.g. `http://192.168.1.10:6969` |
| `API_KEY` | A samo bearer token — Settings → API tokens |
| `LIBRARY_NAME` | Optional; picks a specific library when you have several |

An **admin** token is worth using. A non-admin one downloads and builds
playlists fine, but cannot trigger a scan or read explo status, so it waits out
`SLEEP` instead and reports less.

## Two things that will bite you

**Do not put `docker compose run` in cron.** The image is a long-running daemon
— `start.sh` registers cron jobs from the `*_SCHEDULE` variables and ends in
`crond -f`. `docker compose run --rm explo` starts a *second* scheduler that
never exits, ignores any CLI flags you pass it (`start.sh` does not read `$@`),
and vanishes the next time the Docker daemon restarts. Set the schedule in
`docker-compose.yaml` and let the container do it.

**`--clean-downloads` is what rotates the folder, and it defaults to off.** It
deletes last week's drop before fetching this week's, which is what keeps samo's
Explore playlist a "this week" queue rather than an ever-growing pile. It also
requires `USE_SUBDIRECTORY=true`.

Older guides say `--persist=false` does this. It used to. In this codebase that
flag is parsed and then **never read** — it prints a deprecation warning and
changes nothing — so a config carried over from an older image looks correct,
runs without error, and quietly stops rotating. If your Explore playlist is
growing instead of turning over, this is why.

## Manual runs

```sh
docker exec samo-explo sh -c 'cd /opt/explo && ./explo --clean-downloads'
```

The web UI on `http://localhost:7288` has a run button and a log view.

---

## Everything else

The rest of Explo is unchanged: ListenBrainz discovery, YouTube and Soulseek
downloading, custom playlist imports from Apple Music / ListenBrainz / Spotify,
metadata tagging, and the other music systems (Emby, Jellyfin, MPD, Plex,
Subsonic). Upstream's documentation applies as written:

- [Quick Start](https://github.com/LumePart/Explo/wiki/2.-Quick-Start)
- [Getting Started](https://github.com/LumePart/Explo/wiki/3.-Getting-Started)
- [Configuration Parameters](https://github.com/LumePart/Explo/wiki/5.-Configuration-Parameters)
- [System Notes](https://github.com/LumePart/Explo/wiki/6.-System-Notes)
- [FAQ](https://github.com/LumePart/Explo/wiki/8.-FAQ)

## Credits

Explo is written by [Markus Kuuse](https://github.com/LumePart) and its
contributors, and is MIT licensed — see `LICENSE`. This fork adds one client and
a deploy script; everything that makes it work is theirs. If you do not run
samo, use [upstream](https://github.com/LumePart/Explo) directly.

Third-party libraries: [ffmpeg-go](https://github.com/u2takey/ffmpeg-go),
[goutubedl](https://github.com/wader/goutubedl),
[godotenv](https://github.com/joho/godotenv),
[ytmusicapi](https://github.com/sigma67/ytmusicapi),
[notify](https://github.com/nikoksr/notify),
[gocron](https://github.com/go-co-op/gocron). See `NOTICE`.
