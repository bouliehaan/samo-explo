#!/usr/bin/env bash
#
# samo-explo deploy — one command from nothing to a running weekly discovery feed.
#
# Asks for your samo URL and login, mints a dedicated API token, writes .env and
# docker-compose.yaml, and starts the container. Re-running it is safe: it reuses
# the existing token and only rewrites what changed.
#
#   ./deploy.sh                        interactive
#   SAMO_URL=... SAMO_USER=... SAMO_PASS=... LB_USER=... ./deploy.sh --yes
#
set -euo pipefail

INSTALL_DIR="${INSTALL_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)}"
ENV_FILE="$INSTALL_DIR/.env"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yaml"
ASSUME_YES=false
[[ "${1:-}" == "--yes" || "${1:-}" == "-y" ]] && ASSUME_YES=true

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '\033[33m  ! %s\033[0m\n' "$*"; }
die()  { printf '\033[31m  x %s\033[0m\n' "$*" >&2; exit 1; }

ask() { # ask VAR "prompt" [default]
    local var=$1 prompt=$2 default=${3:-} current=${!1:-} reply
    [[ -n "$current" ]] && { printf -v "$var" '%s' "$current"; return; }
    $ASSUME_YES && [[ -n "$default" ]] && { printf -v "$var" '%s' "$default"; return; }
    $ASSUME_YES && die "$var is required in --yes mode"
    if [[ -n "$default" ]]; then
        read -r -p "  $prompt [$default]: " reply
        reply=${reply:-$default}
    else
        read -r -p "  $prompt: " reply
    fi
    printf -v "$var" '%s' "$reply"
}

ask_secret() {
    local var=$1 prompt=$2 current=${!1:-} reply
    [[ -n "$current" ]] && return
    $ASSUME_YES && die "$var is required in --yes mode"
    read -r -s -p "  $prompt: " reply; echo
    printf -v "$var" '%s' "$reply"
}

# ---------------------------------------------------------------- prerequisites
bold "samo-explo"
command -v docker >/dev/null || die "docker is not installed"
docker compose version >/dev/null 2>&1 || die "the docker compose plugin is not installed"
command -v curl >/dev/null || die "curl is not installed"
docker info >/dev/null 2>&1 || die "cannot talk to the docker daemon (try: sudo usermod -aG docker \$USER, then log out and back in)"

# Seed DEFAULTS from a previous run so re-running is mostly pressing enter.
# Deliberately defaults, not answers: ask() skips a prompt whose variable is
# already set, and a re-run is usually someone fixing an answer they regret.
PREV_URL=""; PREV_LB=""; PREV_TZ=""; PREV_SCHEDULE=""; PREV_PATH=""
if [[ -f "$ENV_FILE" ]]; then
    PREV_URL=$(grep -E '^SYSTEM_URL=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)
    # .env holds the URL as the CONTAINER sees it; offer it back in host terms.
    PREV_URL=${PREV_URL//host.docker.internal/localhost}
    PREV_LB=$(grep -E '^LISTENBRAINZ_USER=' "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- || true)
fi
if [[ -f "$COMPOSE_FILE" ]]; then
    PREV_TZ=$(sed -n 's/^ *- TZ=//p' "$COMPOSE_FILE" 2>/dev/null | head -1 || true)
    PREV_SCHEDULE=$(sed -n 's/^ *- WEEKLY_EXPLORATION_SCHEDULE=//p' "$COMPOSE_FILE" 2>/dev/null | head -1 || true)
    PREV_PATH=$(sed -n 's|^ *- \(.*\):/data/$|\1|p' "$COMPOSE_FILE" 2>/dev/null | head -1 || true)
fi
# Never re-offer a timezone that was invalid to begin with.
[[ -n "$PREV_TZ" && -d /usr/share/zoneinfo && ! -f "/usr/share/zoneinfo/$PREV_TZ" ]] && PREV_TZ=""

# ------------------------------------------------------------------ samo server
echo
bold "1. Samo server"
ask SAMO_URL "Samo URL" "${PREV_URL:-http://localhost:6969}"
SAMO_URL="${SAMO_URL%/}"

# An unauthenticated 401 from a route that requires auth is the cleanest proof
# that samo is on the other end; 000 means nothing answered at all.
ping_status=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$SAMO_URL/api/v1/users/me" 2>/dev/null || true)
case "$ping_status" in
    000) die "cannot reach a samo server at $SAMO_URL" ;;
    401|200) info "reachable" ;;
    *) warn "unexpected response from $SAMO_URL (HTTP $ping_status) — continuing anyway" ;;
esac

# How the container should be networked, which is really the question "can it
# reach samo at all".
#
# A samo on THIS host is the common case and the one bridge networking handles
# worst. Two separate things break it: `localhost` inside a container is the
# container, and on a ufw host every packet from the docker bridge to the host
# is DROPPED — which surfaces as a connection timeout with no log anywhere,
# not as a refusal. Host networking removes the hop altogether, so an address
# that works in this shell works in the container. It is also exactly how
# samo-server itself runs.
#
# Docker Desktop has no real host networking, but does resolve
# host.docker.internal, so that is the fallback off Linux.
SAMO_HOST=$(printf '%s' "$SAMO_URL" | sed -E 's#^https?://##; s#/.*$##; s#:[0-9]+$##; s#^\[(.*)\]$#\1#')
CONTAINER_URL="$SAMO_URL"
USE_HOST_NET=false
SAMO_IS_LOCAL=false

if [[ "$SAMO_HOST" =~ ^(localhost|127\.0\.0\.1|::1)$ ]]; then
    SAMO_IS_LOCAL=true
elif ip -4 -o addr show 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | grep -qx "$SAMO_HOST"; then
    SAMO_IS_LOCAL=true
fi

if $SAMO_IS_LOCAL; then
    if [[ "$(uname -s)" == "Linux" ]]; then
        USE_HOST_NET=true
        info "samo is on this host — using host networking so the container reaches it the same way you do"
    else
        CONTAINER_URL=$(printf '%s' "$SAMO_URL" | sed -E 's#//(localhost|127\.0\.0\.1|\[::1\])#//host.docker.internal#')
        info "samo is on this host — the container will use $CONTAINER_URL"
    fi
fi

# An existing token keeps its name in samo's token list, so re-running deploy
# does not litter the account with one token per run.
SAMO_TOKEN="${SAMO_TOKEN:-}"
if [[ -z "$SAMO_TOKEN" && -f "$ENV_FILE" ]]; then
    SAMO_TOKEN=$(grep -E '^API_KEY=' "$ENV_FILE" 2>/dev/null | cut -d= -f2- || true)
    [[ -n "$SAMO_TOKEN" ]] && info "reusing the API token already in .env"
fi

if [[ -n "$SAMO_TOKEN" ]]; then
    code=$(curl -s -o /dev/null -w '%{http_code}' -m 10 -H "Authorization: Bearer $SAMO_TOKEN" "$SAMO_URL/api/v1/users/me" || true)
    [[ "$code" != "200" ]] && { warn "the saved token no longer works, minting a new one"; SAMO_TOKEN=""; }
fi

if [[ -z "$SAMO_TOKEN" ]]; then
    ask SAMO_USER "Samo username"
    ask_secret SAMO_PASS "Samo password"

    login=$(curl -s -m 15 -X POST "$SAMO_URL/api/v1/auth/login" \
        -H 'Content-Type: application/json' \
        --data-binary "$(printf '{"username":%s,"password":%s}' \
            "$(printf '%s' "$SAMO_USER" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')" \
            "$(printf '%s' "$SAMO_PASS" | python3 -c 'import json,sys;print(json.dumps(sys.stdin.read()))')")" || true)
    session=$(printf '%s' "$login" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("token",""))' 2>/dev/null || true)
    [[ -z "$session" ]] && die "login failed — check the username and password"

    issued=$(curl -s -m 15 -X POST "$SAMO_URL/api/v1/users/me/tokens" \
        -H "Authorization: Bearer $session" -H 'Content-Type: application/json' \
        --data '{"label":"samo-explo"}' || true)
    SAMO_TOKEN=$(printf '%s' "$issued" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("secret",""))' 2>/dev/null || true)
    [[ -z "$SAMO_TOKEN" ]] && die "could not mint an API token"
    info "minted a 'samo-explo' API token"
    unset SAMO_PASS
fi

role=$(curl -s -m 10 -H "Authorization: Bearer $SAMO_TOKEN" "$SAMO_URL/api/v1/users/me" \
    | python3 -c 'import json,sys;print(json.load(sys.stdin).get("role",""))' 2>/dev/null || true)
[[ "$role" != "admin" ]] && warn "this account is not an admin — samo-explo cannot trigger library scans and will wait out SLEEP instead"

# --------------------------------------------------------------- the drop folder
echo
bold "2. Drop folder"
explo_cfg=$(curl -s -m 10 -H "Authorization: Bearer $SAMO_TOKEN" "$SAMO_URL/api/v1/explo/config" 2>/dev/null || true)
server_folder=$(printf '%s' "$explo_cfg" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("folder","") if d.get("enabled") and d.get("configured") else "")' 2>/dev/null || true)

if [[ -n "$server_folder" ]]; then
    info "samo is watching $server_folder"
    info "it will fingerprint each drop and build the Explore playlist itself"
    ask DOWNLOAD_PATH "Local path to that same folder" "${PREV_PATH:-$server_folder}"
else
    warn "samo's explo folder integration is off (Settings -> Explo)."
    warn "samo-explo will create an ordinary playlist instead of the Explore queue."
    ask DOWNLOAD_PATH "Where should downloads go" "$PREV_PATH"
fi
mkdir -p "$DOWNLOAD_PATH" || die "cannot create $DOWNLOAD_PATH"

# ------------------------------------------------------------------- listenbrainz
echo
bold "3. ListenBrainz"
ask LB_USER "ListenBrainz username" "$PREV_LB"
# An unrecognised TZ does not error anywhere — the container's crond just
# silently falls back to UTC, and the weekly job fires hours off from when you
# meant it to. Names are the full zoneinfo form ("America/Denver"), never an
# abbreviation ("MST", "DEN"), so check before writing it.
while :; do
    ask TZ_NAME "Timezone" "${PREV_TZ:-$(cat /etc/timezone 2>/dev/null || echo UTC)}"
    if [[ ! -d /usr/share/zoneinfo ]]; then
        break   # nothing to validate against; trust the answer
    fi
    if [[ -f "/usr/share/zoneinfo/$TZ_NAME" ]]; then
        break
    fi
    warn "'$TZ_NAME' is not a zoneinfo name — use the Region/City form, e.g. America/Denver"
    guess=$(find /usr/share/zoneinfo -type f -ipath "*${TZ_NAME}*" 2>/dev/null | head -3 | sed 's|/usr/share/zoneinfo/||')
    [[ -n "$guess" ]] && { info "did you mean:"; printf '      %s\n' $guess; }
    $ASSUME_YES && die "invalid timezone in --yes mode: $TZ_NAME"
    TZ_NAME=""
done
ask SCHEDULE "Weekly schedule (cron)" "${PREV_SCHEDULE:-15 00 * * 2}"

# -------------------------------------------------------------------- write files
echo
bold "4. Writing configuration"
[[ -f "$ENV_FILE" ]] && cp -a "$ENV_FILE" "$ENV_FILE.bak.$(date +%Y%m%d%H%M%S)" && info "backed up the previous .env"

cat > "$ENV_FILE" <<EOF
# --- Discovery ---
DISCOVERY_SERVICE=listenbrainz
LISTENBRAINZ_USER=$LB_USER
LISTENBRAINZ_DISCOVERY=playlist

# --- Music system ---
EXPLO_SYSTEM=samo
SYSTEM_URL=$CONTAINER_URL
API_KEY=$SAMO_TOKEN

# --- Downloader ---
DOWNLOAD_SERVICES=youtube
TRACK_EXTENSION=mp3
USE_SUBDIRECTORY=true

# --- Misc ---
SLEEP=2
LOG_LEVEL=INFO
PLAYLISTNAME_FORMAT=date
EOF
chmod 600 "$ENV_FILE"
info "wrote .env (mode 600 — it holds your API token)"

if $USE_HOST_NET; then
    NETWORK_BLOCK="    network_mode: host"     # binds the web UI on :7288 directly
elif [[ "$CONTAINER_URL" == *host.docker.internal* ]]; then
    NETWORK_BLOCK=$'    ports:\n      - "7288:7288"\n    extra_hosts:\n      - "host.docker.internal:host-gateway"'
else
    NETWORK_BLOCK=$'    ports:\n      - "7288:7288"'
fi

cat > "$COMPOSE_FILE" <<EOF
services:
  explo:
    # Published image if one is reachable; otherwise compose builds from this
    # checkout, so a fresh clone works with no registry access at all.
    image: ghcr.io/bouliehaan/samo-explo:latest
    build: $INSTALL_DIR
    container_name: samo-explo
    restart: unless-stopped
$NETWORK_BLOCK
    env_file:
      - $ENV_FILE
    volumes:
      - $ENV_FILE:/opt/explo/.env
      - $DOWNLOAD_PATH:/data/
    environment:
      - PUID=$(id -u)
      - PGID=$(id -g)
      - TZ=$TZ_NAME
      - WEEKLY_EXPLORATION_SCHEDULE=$SCHEDULE
      # --clean-downloads is what rotates the drop folder: it deletes last
      # week's tracks before fetching this week's, which is what keeps samo's
      # Explore playlist a "this week" queue instead of an ever-growing pile.
      # It defaults to OFF and requires USE_SUBDIRECTORY=true (set in .env).
      # Do not use the older --persist=false here — in this codebase that flag
      # is parsed and then never read, so it silently does nothing at all.
      - WEEKLY_EXPLORATION_FLAGS=--clean-downloads
EOF
info "wrote docker-compose.yaml"

# --------------------------------------------------------------------- start it
echo
bold "5. Starting"
cd "$INSTALL_DIR"
if docker compose pull 2>/dev/null; then
    info "pulled the published image"
else
    info "no published image reachable — building locally (a few minutes the first time)"
    docker compose build || die "image build failed"
fi
docker compose up -d
sleep 3
docker compose ps

# Verify from INSIDE the container, which is the only place that counts. The
# host-side probe earlier cannot catch a URL that only works out here, and a
# scheduled job that cannot authenticate fails at 00:15 on a Tuesday with
# nobody watching. --refresh-only exercises auth, library lookup and the scan
# trigger, then exits.
echo
bold "6. Verifying from inside the container"
if verify=$(docker exec samo-explo sh -c 'cd /opt/explo && ./explo --config /opt/explo/.env --refresh-only' 2>&1); then
    printf '%s\n' "$verify" | grep -E '\[samo\]|library refresh' | sed 's/^/  /'
    info "samo-explo can reach samo and authenticate"
else
    printf '%s\n' "$verify" | tail -5 | sed 's/^/  /'
    warn "samo-explo is running but could NOT talk to samo — the schedule will do nothing until this is fixed."
    if [[ "$verify" == *"deadline exceeded"* || "$verify" == *"timeout"* ]] && ! $USE_HOST_NET; then
        # A timeout rather than a refusal means the packets left and died. On a
        # ufw host that is almost always the bridge -> host path being dropped.
        warn "A timeout (not a refusal) usually means a host firewall is dropping traffic from the docker bridge."
        if systemctl is-active ufw >/dev/null 2>&1; then
            warn "ufw is active on this host, which is very likely the cause. Either:"
            warn "  - point samo-explo at samo over a network the container can use, or"
            warn "  - sudo ufw allow in on $(ip -4 -o addr show 2>/dev/null | awk '/172\./{print $2; exit}') to port ${SAMO_URL##*:}"
        fi
    fi
    exit 1
fi

echo
bold "Done."
info "Web UI:    http://localhost:7288"
info "Logs:      docker compose -f $COMPOSE_FILE logs -f"
info "Run now:   docker exec samo-explo sh -c 'cd /opt/explo && ./explo --clean-downloads'"
echo
info "The container schedules itself from WEEKLY_EXPLORATION_SCHEDULE."
info "Do not also add a cron entry — 'docker compose run' starts a SECOND scheduler"
info "that a daemon restart silently removes."
