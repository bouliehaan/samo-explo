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

# ------------------------------------------------------------------ samo server
echo
bold "1. Samo server"
ask SAMO_URL "Samo URL" "http://localhost:6969"
SAMO_URL="${SAMO_URL%/}"

# An unauthenticated 401 from a route that requires auth is the cleanest proof
# that samo is on the other end; 000 means nothing answered at all.
ping_status=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$SAMO_URL/api/v1/users/me" 2>/dev/null || true)
case "$ping_status" in
    000) die "cannot reach a samo server at $SAMO_URL" ;;
    401|200) info "reachable" ;;
    *) warn "unexpected response from $SAMO_URL (HTTP $ping_status) — continuing anyway" ;;
esac

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
    ask DOWNLOAD_PATH "Local path to that same folder" "$server_folder"
else
    warn "samo's explo folder integration is off (Settings -> Explo)."
    warn "samo-explo will create an ordinary playlist instead of the Explore queue."
    ask DOWNLOAD_PATH "Where should downloads go"
fi
mkdir -p "$DOWNLOAD_PATH" || die "cannot create $DOWNLOAD_PATH"

# ------------------------------------------------------------------- listenbrainz
echo
bold "3. ListenBrainz"
ask LB_USER "ListenBrainz username"
ask TZ_NAME "Timezone" "$(cat /etc/timezone 2>/dev/null || echo UTC)"
ask SCHEDULE "Weekly schedule (cron)" "15 00 * * 2"

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
SYSTEM_URL=$SAMO_URL
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

cat > "$COMPOSE_FILE" <<EOF
services:
  explo:
    # Published image if one is reachable; otherwise compose builds from this
    # checkout, so a fresh clone works with no registry access at all.
    image: ghcr.io/bouliehaan/samo-explo:latest
    build: $INSTALL_DIR
    container_name: samo-explo
    restart: unless-stopped
    env_file:
      - $ENV_FILE
    ports:
      - "7288:7288"
    volumes:
      - $ENV_FILE:/opt/explo/.env
      - $DOWNLOAD_PATH:/data/
    environment:
      - TZ=$TZ_NAME
      - WEEKLY_EXPLORATION_SCHEDULE=$SCHEDULE
      - WEEKLY_EXPLORATION_FLAGS=--persist=false
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

echo
bold "Done."
info "Web UI:    http://localhost:7288"
info "Logs:      docker compose -f $COMPOSE_FILE logs -f"
info "Run now:   docker exec samo-explo sh -c 'cd /opt/explo && ./explo --persist=false'"
echo
info "The container schedules itself from WEEKLY_EXPLORATION_SCHEDULE."
info "Do not also add a cron entry — 'docker compose run' starts a SECOND scheduler"
info "that a daemon restart silently removes."
