#!/usr/bin/env bash
# pack-for-move.sh — make one archive that actually contains a whole CRN.
#
# Copying ~/fitt-coderunner to another machine gets the code, the .env and the
# workspaces, and silently leaves the database behind: Postgres and Mongo live in
# Docker named volumes, which are not inside this directory. A fresh CRN then
# comes up looking fine, with no projects, no build history, and only the
# built-in skill.
#
# This script pulls those volumes into the directory as tarballs first, then
# archives the whole thing minus what should be rebuilt (node_modules, .next,
# compiled binaries, workspaces).
#
# It only reads. Restoring is deliberately NOT automated — it overwrites a
# database — and is documented in docs/moving-machines.md.
set -euo pipefail

cd "$(dirname "$0")/.."
out="../crn-move-$(date +%Y%m%d-%H%M).tar.gz"

# The volume names in docker-compose.yml are NOT the real ones: Compose prefixes
# them with the project name, so `crn_pgdata` is really `fitt-coderunner_crn_pgdata`
# (and a rename of this directory changes it again). Mounting the short name would
# quietly create an EMPTY volume and archive that instead — a backup that looks
# like it worked. Ask the running container what it is actually mounting.
volume_of() { # volume_of <container> <mountpoint>
  docker inspect "$1" \
    --format '{{range .Mounts}}{{if eq .Destination "'"$2"'"}}{{.Name}}{{end}}{{end}}' 2>/dev/null
}

snapshot() { # snapshot <container> <mountpoint> <output.tgz>
  local vol
  vol=$(volume_of "$1" "$2")
  if [ -z "$vol" ]; then
    echo "  ! $1 is not running — cannot find its volume. Start it (docker compose up -d) and retry." >&2
    return 1
  fi
  echo "  $1 → $3  (volume: $vol)"
  docker run --rm -v "$vol":/data:ro -v "$PWD":/backup alpine \
    tar czf "/backup/$3" -C /data .
}

echo "==> Stopping datastores so the files hold still"
# A tar of a live PGDATA is a torn copy: correct-looking, occasionally corrupt.
docker compose stop postgres mongo

echo "==> Snapshotting volumes into this directory"
snapshot crn-postgres /var/lib/postgresql/data pgdata.tgz
snapshot crn-mongo    /data/db                 mongodata.tgz

echo "==> Restarting datastores"
docker compose start postgres mongo

echo "==> Building $out"
# Excluded on purpose: node_modules and .next carry native binaries built for
# this machine (esbuild, next-swc, the Prisma engine) — copying them across is
# the classic "looks copied, will not run". The workspaces re-clone from their
# remotes, and bin/ is compiled by make.
tar czf "$out" \
  --exclude='./node_modules' \
  --exclude='./frontend/node_modules' \
  --exclude='./frontend/.next' \
  --exclude='./.crn-workspaces' \
  --exclude='./bin' \
  --exclude='./server' \
  --exclude='.DS_Store' \
  -C .. "$(basename "$PWD")"

rm -f pgdata.tgz mongodata.tgz

echo
echo "Done: $out ($(du -h "$out" | cut -f1))"
echo
echo "This archive contains .env — the database password, the callback token and"
echo "whatever else is in there. Move it by AirDrop or a cable, not through chat"
echo "or a cloud drive, and delete it once the new machine is up."
echo
echo "Unpacking instructions: docs/moving-machines.md"
