#!/bin/sh

PUID="${PUID:-0}"
PGID="${PGID:-0}"

if [ "$PUID" != "0" ] || [ "$PGID" != "0" ]; then
    groupmod -o -g "$PGID" explo
    usermod -o -u "$PUID" explo
    RUN_USER="explo"
    RUNNER="su-exec explo"
else
    echo "[setup] WARN: running as root. Consider defining PUID & PGID in docker-compose to run as a non-root user"
    RUN_USER="root"
    RUNNER=""
    
fi

mkdir -p /opt/explo
if [ "$RUN_USER" != "root" ]; then 
  chown -R explo:explo /opt/explo
fi

echo "[setup] Starting web UI..."
# If user incorectly mounts the config path as a directory, we'll try to automatically append it to .env inside it instead of failing.
WEB_ENV_PATH="${WEB_ENV_PATH:-/opt/explo/.env}"
if [ -d "$WEB_ENV_PATH" ]; then
    WEB_ENV_PATH="$WEB_ENV_PATH/.env"
    echo "[setup] Config path is a directory, using $WEB_ENV_PATH"
fi
WEB_UI=true WEB_ENV_PATH="$WEB_ENV_PATH" WEB_ADDR="${WEB_ADDR:-:7288}" $RUNNER ./explo --config "$WEB_ENV_PATH" &
echo "[setup] Web UI available at http://localhost:${WEB_ADDR##*:}"

echo "[setup] Initializing cron jobs..."

# Load *_SCHEDULE and *_FLAGS from .env if not already set in the environment.
# This allows the web UI to configure schedules by writing to the .env file.
_cfg="${WEB_ENV_PATH:-/opt/explo/.env}"
if [ -f "$_cfg" ]; then
  while IFS= read -r _line; do
    case "$_line" in \#*|'') continue ;; esac
    _key="${_line%%=*}"
    case "$_key" in
      *_SCHEDULE|*_FLAGS)
        if [ -z "$(printenv "$_key" 2>/dev/null)" ]; then
          export "$_key=${_line#*=}"
        fi
        ;;
    esac
  done < "$_cfg"
fi


# $CRON_SHCEDULE was deprecated in v0.11.0, keeping this block for backwards compatibility
if [ -n "$CRON_SCHEDULE" ]; then
    echo "$CRON_SCHEDULE { apk add --no-cache --upgrade yt-dlp || echo '[warn] yt-dlp refresh failed, running with the installed version'; }; cd /opt/explo && $RUNNER ./explo --config \"$_cfg\" >> /proc/1/fd/1 2>&1" > /etc/crontabs/root
    chmod 600 /etc/crontabs/root
    echo "[setup] Registered single CRON_SCHEDULE job: $CRON_SCHEDULE"
    crond -f -l 2
fi

# /etc/crontabs/root lives in the container's writable layer, so it survives a
# restart — and the loop below appends to it. Without clearing our own entries
# first, every restart registers the jobs again: `restart: unless-stopped` plus a
# few reboots is enough to stack four identical schedules, which then fire four
# concurrent runs on the same minute, racing each other over the same downloads,
# the same drop folder and the same apk lock. Drop only the lines we generate;
# the base image's own periodic entries have to stay.
if [ -f /etc/crontabs/root ]; then
  grep -v "cd /opt/explo && " /etc/crontabs/root > /etc/crontabs/root.new
  mv /etc/crontabs/root.new /etc/crontabs/root
fi

# Loop over all *_SCHEDULE environment variables
for var in $(env | grep "_SCHEDULE=" | cut -d= -f1); do
  job="${var%_SCHEDULE}"                     # Job name (e.g WEEKLY_EXPLORATION)
  schedule="$(printenv "$var")"              # Cron schedule
  flags_var="${job}_FLAGS"
  flags="$(printenv "$flags_var")"           # e.g. --playlist weekly-exploration

  if [ -z "$schedule" ]; then
    echo "[setup] Skipping $job: schedule is empty"
    continue
  fi

  # Default: just run explo if flags are empty.
  #
  # The yt-dlp refresh is deliberately NOT chained with && — a transient alpine
  # mirror failure must not stop the run. yt-dlp is already in the image; an
  # older yt-dlp is worth far more than a week that silently downloads nothing.
  cmd="{ apk add --no-cache --upgrade yt-dlp || echo '[warn] yt-dlp refresh failed, running with the installed version'; }; cd /opt/explo && $RUNNER ./explo --config \"$_cfg\" $flags >> /proc/1/fd/1 2>&1"

  echo "$schedule $cmd" >> /etc/crontabs/root
  echo "[setup] Registered job: $job"
  echo "        Schedule: $schedule"
  echo "        Command : ./explo --config $_cfg $flags"
done

chmod 600 /etc/crontabs/root

echo "[setup] Starting cron..."

if [ "$EXECUTE_ON_START" = "true" ]; then
    echo "[setup] Executing startup task..."  
    { apk add --no-cache --upgrade yt-dlp || echo '[warn] yt-dlp refresh failed, running with the installed version'; }; cd /opt/explo && $RUNNER ./explo --config "$_cfg" $START_FLAGS
    
fi
crond -f -l 2
