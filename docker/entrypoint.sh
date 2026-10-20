#!/bin/sh
set -eu
umask 077
# Initialize only the volume root, never recursively change mounted repositories.
if [ "$(id -u)" = 0 ]; then
  mkdir -p "$STALEMATE_DATA_DIR"
  chown stalemate:stalemate "$STALEMATE_DATA_DIR"
  exec su-exec stalemate "$0" "$@"
fi
if [ -n "${STALEMATE_API_TOKEN_FILE:-}" ]; then
  STALEMATE_API_TOKEN=$(cat "$STALEMATE_API_TOKEN_FILE")
elif [ -z "${STALEMATE_API_TOKEN:-}" ]; then
  if [ ! -s "$STALEMATE_DATA_DIR/api-token" ]; then
    head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$STALEMATE_DATA_DIR/api-token.tmp"
    mv "$STALEMATE_DATA_DIR/api-token.tmp" "$STALEMATE_DATA_DIR/api-token"
  fi
  STALEMATE_API_TOKEN=$(cat "$STALEMATE_DATA_DIR/api-token")
fi
[ -n "$STALEMATE_API_TOKEN" ] || { echo 'API token must not be empty' >&2; exit 1; }
export STALEMATE_API_TOKEN
if [ -n "${OPENAI_API_KEY_FILE:-}" ]; then OPENAI_API_KEY=$(cat "$OPENAI_API_KEY_FILE"); export OPENAI_API_KEY; fi
if [ -n "${OPENAI_API_KEY:-}" ]; then export STALEMATE_AI_PROVIDER=${STALEMATE_AI_PROVIDER:-openai}; fi
# Explicit empty environment file prevents accidental loading of a mounted .env.
touch "$STALEMATE_DATA_DIR/environment"
exec stalemate --config "${STALEMATE_CONFIG:-/etc/stalemate/config.json}" --env-file "$STALEMATE_DATA_DIR/environment" "$@"
