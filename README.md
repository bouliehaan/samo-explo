# samo-explo

Weekly music discovery for [samo](https://github.com/bouliehaan/samo). Every
week it pulls your ListenBrainz recommendations, downloads what your library is
missing, and drops it where samo can find it. samo does the rest.

A fork of [LumePart/Explo](https://github.com/LumePart/Explo) with a native
[samo-server](https://github.com/bouliehaan/samo-server) client. If you do not
run samo, use [upstream](https://github.com/LumePart/Explo) instead.

## Install

```bash
git clone https://github.com/bouliehaan/samo-explo.git
cd samo-explo
./deploy.sh
```

`deploy.sh` asks for your samo URL and login, mints a dedicated API token,
writes `.env` and `docker-compose.yaml`, pulls
[`ghcr.io/bouliehaan/samo-explo:latest`](https://github.com/bouliehaan/samo-explo/pkgs/container/samo-explo)
and starts it. Re-running is safe and is how you update — it reuses the token it
already made.

It also asks which downloader you want, and configures
[slskd](https://github.com/slskd/slskd) for you if you pick Soulseek. Take that
option — see [Downloads](#downloads-youtube-will-block-you).

Wiring it by hand instead: [docker-compose.example.yaml](docker-compose.example.yaml).
The web UI is on `http://<host>:7288`, with a run button and a log view.

Use an **admin** token. A non-admin one downloads and builds playlists fine, but
cannot trigger a scan or read explo status, so it waits out `SLEEP` instead and
reports less.

## The two modes

**samo's explo folder is on** (Settings → Explo). This is the good one. samo
fingerprints every dropped file with AcoustID, applies real metadata and cover
art, keeps the drop out of Recently Added, and re-derives its system **Explore**
playlist from the folder on every pass. samo-explo detects this and does *not*
create a playlist of its own — a second one would sit next to samo's going stale
while samo rewrites the real one. Downloading is the whole integration.

**samo's explo folder is off.** samo-explo falls back to creating an ordinary
samo playlist. No fingerprinting, no cover art, and the drops show up in
Recently Added like any other import.

`deploy.sh` detects which one you are in and tells you.

## Downloads: YouTube will block you

YouTube answers most server IPs with *"Sign in to confirm you're not a bot"*,
and every download in the run fails with it. Nothing in samo or samo-explo can
work around that. Two ways out:

**Soulseek — the recommended route.** `deploy.sh` configures slskd for you: it
verifies the API key, reads slskd's own download directory out of its API, works
out which host directory is mounted there, and mounts that into the container.
No bot checks, no cookies to keep fresh, better audio than a YouTube rip.

One trap it handles for you: slskd API keys carry a `cidr:` restriction, and the
usual `192.168.x.0/24` does **not** contain `127.0.0.1`. With host networking a
request to `localhost:5030` arrives from loopback and is refused with a flat
`401` that looks exactly like a wrong key. Use a LAN address, which is what the
default offers.

**A cookies file.** yt-dlp's own remedy. Export your YouTube cookies in Netscape
format, drop the file at `cookies.txt` next to `deploy.sh`, and re-run — it gets
mounted for you. Worth using a throwaway Google account: this is bulk automated
fetching, and accounts do get flagged for it.

A run that downloads nothing is not silent — samo-explo reports `inFolder=0` and
points at the failures above it.

## Configuration

Beyond the upstream variables, the samo client uses:

| Variable | Meaning |
| --- | --- |
| `EXPLO_SYSTEM=samo` | Selects this client |
| `SYSTEM_URL` | Your samo server, e.g. `http://192.168.1.10:6969` |
| `API_KEY` | A samo bearer token — Settings → API tokens |
| `LIBRARY_NAME` | Optional; picks a specific library when you have several |

Everything else is upstream's, documented in its wiki:
[Quick Start](https://github.com/LumePart/Explo/wiki/2.-Quick-Start) ·
[Configuration](https://github.com/LumePart/Explo/wiki/5.-Configuration-Parameters) ·
[FAQ](https://github.com/LumePart/Explo/wiki/8.-FAQ).

## Two things that will bite you

**Do not put `docker compose run` in cron.** The image is a long-running daemon
— `start.sh` registers cron jobs from the `*_SCHEDULE` variables and ends in
`crond -f`. `docker compose run --rm explo` starts a *second* scheduler that
never exits, ignores any CLI flags you pass it, and vanishes the next time the
Docker daemon restarts. Set the schedule in compose and let the container do it.

**`--clean-downloads` is what rotates the drop folder, and it defaults to off.**
With the samo client it is a reconcile, not a wipe: this week's list is resolved
first, anything still recommended keeps its file, and only drops that have
fallen off the list are deleted. Cleaning *before* the local check — which is
what upstream does — destroys the evidence the check needs, so every still-
relevant track gets re-downloaded every week. It requires `USE_SUBDIRECTORY=true`.

Do not reach for `--persist=false`, which older guides name for this. In this
codebase that flag is parsed and then **never read** — it prints a deprecation
warning and changes nothing. If your Explore playlist is growing instead of
turning over, that is why.

## Why the fork

samo speaks Subsonic, so upstream Explo *appears* to work against it. It does
not: samo's Subsonic surface is deliberately read-only for playlists, so Explo
authenticates, downloads, and then fails on the last step of every single run.

The `samo` client here talks to the native REST API instead. It also fails
loudly on a bad token rather than degrading into a week that quietly downloads
nothing, skips what you already own, and scans only the drop folder so a drop is
visible in seconds instead of after a full library walk.

It also checks the explo **ledger** (`/api/v1/explo/tracks`) before searching,
because samo keeps explo content out of its search index on purpose — the "explo
silo". The obvious implementation of "skip what I already own" is therefore
silently wrong: the drop folder is invisible to search, so every track in
Explore would be re-downloaded every week, forever.

Only tracks **new to your library** land in Explore. A 50-track weekly
recommendation where you already own 22 produces an Explore of 28. That is
usually what you want — Explore is "what arrived", not "what was recommended" —
but a well-stocked library yields a smaller one.

## Credits

Explo is written by [Markus Kuuse](https://github.com/LumePart) and its
contributors, and is MIT licensed — see [LICENSE](LICENSE). This fork adds one
client and a deploy script; everything that makes it work is theirs. Third-party
libraries are listed in [NOTICE](NOTICE).
