#!/usr/bin/env bash
# Build (via dockerBuild.sh) and run the container.
#
# There is deliberately no docker-compose here. This app is one container, so
# compose would add a second configuration format that hides the same flags
# behind YAML keys you have to look up. A single `docker run` you can read top
# to bottom is easier to audit, and every hardening choice below is visible on
# the line where it takes effect.
set -euo pipefail
cd "$( dirname "${BASH_SOURCE[0]}" )"

IMAGE_NAME="vocab-trainer"
CONTAINER_NAME="vocab-trainer"
HOST_PORT="${HOST_PORT:-8080}"

# The app directory: one place holding the database, app storage, and an
# optional config.yaml.
#
# Outside a container this defaults to ~/.config/vocab-trainer/ (see
# server/config), but a container has no meaningful home directory and the
# whole point of the mount is that state lives on the host. So APP_DIR is set
# explicitly to /app/data and bound to ./data next to this script -- one
# directory to back up, visible without entering the container.
APP_DIR_HOST="${APP_DIR_HOST:-$( pwd )/data}"
SECRET_FILE="$( pwd )/.secret_key"

mkdir -p "$APP_DIR_HOST"

# SECRET_KEY encrypts session cookies and, with ENCRYPT_AT_REST on, every
# value in the database. It must be identical on every restart: regenerate it
# and existing records become permanently unreadable.
#
# It is kept here rather than inside the mounted directory on purpose. The
# portable binary writes a secret.key into its app directory because there is
# nobody to set an environment variable for it; a container has this script,
# so the key can stay out of the data volume. That keeps at-rest encryption
# meaningful against a leaked backup of ./data.
if [ ! -f "$SECRET_FILE" ]; then
  echo "Generating $SECRET_FILE (keep this -- losing it means losing the database)"
  ( umask 077; openssl rand -hex 32 > "$SECRET_FILE" )
fi
chmod 600 "$SECRET_FILE"
SECRET_KEY="$( cat "$SECRET_FILE" )"

./dockerBuild.sh

# Make sure the container's unprivileged user can write the app directory.
#
# A bind mount ignores the image's own ownership: /app/data is chowned to
# "app" at build time, but mounting a host directory over it replaces that
# with whatever the host says. Get this wrong and the container starts, fails
# to create app.db, and crash-loops on "permission denied" -- a slow thing to
# diagnose from the outside, so it is worth settling here.
#
# The uid is read back from the image rather than hardcoded, so this keeps
# working if the base image ever numbers its users differently.
APP_UID="$( docker run --rm --entrypoint sh "$IMAGE_NAME" -c 'id -u app' )"
APP_GID="$( docker run --rm --entrypoint sh "$IMAGE_NAME" -c 'id -g app' )"

# Best effort, because the right answer differs by platform. On Linux the host
# directory's ownership is real and the container user needs to be granted it.
# On macOS (Docker Desktop, colima) the file-sharing layer ignores guest
# ownership entirely -- every uid can already write, and chown fails. Neither
# outcome is an error on its own, so the result is checked rather than trusted.
docker run --rm --user 0:0 -v "${APP_DIR_HOST}:/data" \
  --entrypoint sh "$IMAGE_NAME" -c "chown -R ${APP_UID}:${APP_GID} /data" >/dev/null 2>&1 || true

if ! docker run --rm --user "${APP_UID}:${APP_GID}" -v "${APP_DIR_HOST}:/data" \
     --entrypoint sh "$IMAGE_NAME" -c 'touch /data/.probe && rm -f /data/.probe' >/dev/null 2>&1; then
  echo "" >&2
  echo "ERROR: the container runs as uid ${APP_UID}, which cannot write to" >&2
  echo "       ${APP_DIR_HOST}" >&2
  echo "" >&2
  echo "       Without this the container will crash-loop on 'permission denied'." >&2
  echo "       Grant it on the host and re-run:" >&2
  echo "         sudo chown -R ${APP_UID}:${APP_GID} \"${APP_DIR_HOST}\"" >&2
  echo "" >&2
  exit 1
fi

if docker ps -a --format '{{.Names}}' | grep -qx "$CONTAINER_NAME"; then
  echo "Replacing existing container $CONTAINER_NAME..."
  docker rm -f "$CONTAINER_NAME" >/dev/null
fi

echo "Starting $CONTAINER_NAME on 127.0.0.1:$HOST_PORT..."
docker run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
  -p "127.0.0.1:${HOST_PORT}:8080" \
  -v "${APP_DIR_HOST}:/app/data" \
  -e "APP_DIR=/app/data" \
  -e "SECRET_KEY=${SECRET_KEY}" \
  -e "SECURE_COOKIES=${SECURE_COOKIES:-true}" \
  -e "TRUST_PROXY=${TRUST_PROXY:-true}" \
  -e "ENCRYPT_AT_REST=${ENCRYPT_AT_REST:-true}" \
  --read-only \
  --tmpfs /tmp:rw,noexec,nosuid,size=16m \
  --cap-drop ALL \
  --security-opt no-new-privileges:true \
  --pids-limit 128 \
  --memory 256m \
  --cpus 1 \
  "$IMAGE_NAME"

cat <<NOTE

Running. State lives in ${APP_DIR_HOST} (mounted at /app/data):

  app.db      the database
  storage/    application files
  config.yaml optional; overrides defaults, overridden by the -e flags above

Back up that directory together with .secret_key -- either one alone is
useless.

What the flags above are doing:

  -p 127.0.0.1:...   Reachable only from this host. This app does NOT
                     terminate TLS -- put nginx/Caddy/Traefik in front and let
                     it hold the certificate. SECURE_COOKIES stays true
                     because that proxy is serving https; set it to false ONLY
                     for a plain-http local run, or the browser will drop the
                     session cookie and login will appear to do nothing.
  --read-only        The container cannot write to its own filesystem. Only
                     the mounted app directory and the tmpfs are writable.
  --cap-drop ALL     No Linux capabilities at all.
  --no-new-privileges  A process in here can never gain more rights than it
                     started with, even via a setuid binary.
  (non-root)         Runs as the image's unprivileged "app" user.
  --pids-limit/--memory/--cpus  Blast radius limits. Raise them deliberately
                     for real load rather than leaving these as-is and finding
                     out under traffic.

On a fresh database the server creates the first admin and prints a one-time
login link. Read it with:

  docker logs $CONTAINER_NAME

Then open https://your-host/login/<the-link> to sign in. Day to day, mint
further logins from the admin panel in the running app -- that goes through
the server and needs no downtime.

The CLI is for when you cannot get in at all. It works against the running
container -- the server listens on a control socket inside the data directory,
so the CLI never has to open the database itself:

  docker exec -it $CONTAINER_NAME /app/server manage reissue-login -user-id 1

If the container is stopped, run a one-off against the same data directory:

  docker run --rm -v "${APP_DIR_HOST}:/app/data" \\
    -e "APP_DIR=/app/data" -e "SECRET_KEY=\$( cat .secret_key )" \\
    $IMAGE_NAME manage reissue-login -user-id 1

SECRET_KEY must be passed, and must match, or the stored records cannot be
decrypted.

NOTE
