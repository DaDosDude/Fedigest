#!/usr/bin/env bash
# Fedigest updater, like PieFed's deploy script: pull, rebuild, restart.
#
#   ./deploy.sh                 update: git pull in a git checkout, otherwise
#                               unpack ../fedigest.tar.gz
#   ./deploy.sh FILE.tar.gz     update from that release archive
#   ./deploy.sh --check         show installed and available versions only
#   ./deploy.sh --force         rebuild and recreate the container even if
#                               nothing changed (e.g. after editing .env)
#   ./deploy.sh --rollback      go back to the code before the last update
#
# Before updating it backs up the database, uploads and session secret to
# backups/ (last 5 kept) and remembers the old code. If the site doesn't
# come back up, the old code is restored automatically. Your .env,
# backups/ and docker-compose.override.yml are never touched.

set -euo pipefail

main() {
	DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
	cd "$DIR"
	COMPOSE=(docker compose -f "$DIR/docker-compose.yml")
	[ -f "$DIR/docker-compose.override.yml" ] && COMPOSE+=(-f "$DIR/docker-compose.override.yml")
	KEEP_BACKUPS=5

	local mode=update force=0 source=""
	for arg in "$@"; do
		case "$arg" in
			--check) mode=check ;;
			--rollback) mode=rollback ;;
			--force) force=1; FORCE=1 ;;
			-h|--help) sed -n '2,15p' "$0"; exit 0 ;;
			-*) die "unknown option $arg" ;;
			*) source="$arg" ;;
		esac
	done

	command -v docker >/dev/null || die "docker isn't installed"
	# Plain progress output: the interactive view redraws ten times a second,
	# which many terminals turn into a new line each time (build_steps
	# condenses the plain output instead).
	export BUILDKIT_PROGRESS=plain
	if docker compose --progress plain version >/dev/null 2>&1; then
		COMPOSE+=(--progress plain)
	fi
	[ -f .env ] || die "no .env in $DIR (copy .env.example to .env first)"

	if [ -d .git ] && [ -z "$source" ]; then
		GIT=1
	else
		GIT=0
	fi

	if [ "$mode" = rollback ]; then
		rollback
		return
	fi
	if [ "$GIT" = 1 ]; then
		git_update "$mode" "$force"
		return
	fi

	local installed available
	installed="$(cat VERSION 2>/dev/null || echo unknown)"
	tmp="$(mktemp -d)"
	trap 'rm -rf "$tmp"' EXIT

	fetch_release "$source" "$tmp/release.tar.gz"
	mkdir "$tmp/new"
	tar xzf "$tmp/release.tar.gz" -C "$tmp/new" --strip-components=1
	[ -f "$tmp/new/docker-compose.yml" ] && [ -d "$tmp/new/cmd" ] || die "that archive doesn't look like a Fedigest release"
	available="$(cat "$tmp/new/VERSION" 2>/dev/null || echo unknown)"

	echo "Installed: $installed"
	echo "Available: $available"
	[ "$mode" = check ] && return
	if [ "$installed" = "$available" ] && [ "$installed" != unknown ] && [ "$force" = 0 ]; then
		echo "Already up to date (use --force to reinstall)."
		return
	fi

	backup_data
	save_previous
	replace_code "$tmp/new"
	build_or_rollback
}

die() { echo "error: $*" >&2; exit 1; }

# git_update pulls the current branch, like PieFed's deploy script.
git_update() {
	local mode="$1" force="$2" before after
	echo "==> Checking for updates"
	git fetch --quiet
	before="$(git rev-parse HEAD)"
	after="$(git rev-parse '@{u}')"
	echo "Installed: $(cat VERSION 2>/dev/null || echo unknown) (${before:0:7})"
	echo "Available: $(git show "$after:VERSION" 2>/dev/null || echo unknown) (${after:0:7})"
	if [ "$before" != "$after" ]; then
		git --no-pager log --oneline "$before..$after" | head -20
	fi
	[ "$mode" = check ] && return
	if [ "$before" = "$after" ] && [ "$force" = 0 ]; then
		echo "Already up to date (use --force to rebuild anyway)."
		return
	fi
	if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
		die "local changes to tracked files; commit or undo them first (git status)"
	fi
	backup_data
	echo "$before" > .previous-commit
	git merge --ff-only --quiet "$after" || die "can't fast-forward; the checkout has diverged from upstream"
	build_or_rollback
}

# build_or_rollback rebuilds and restarts, and restores the old code if the
# site doesn't come back.
build_or_rollback() {
	echo "==> Building"
	# --force also recreates the container, so .env changes take effect.
	local up=(up -d)
	[ "${FORCE:-0}" = 1 ] && up+=(--force-recreate)
	if ! build_image || ! "${COMPOSE[@]}" "${up[@]}" || ! wait_healthy; then
		echo "!!  Update failed; restoring the previous version." >&2
		"${COMPOSE[@]}" logs --tail 30 app >&2 || true
		rollback
		exit 1
	fi
	echo "==> Updated to $(cat VERSION 2>/dev/null || git rev-parse --short HEAD)"
}

# fetch_release gets the archive from an argument or a file next to this
# folder.
fetch_release() {
	local source="$1" out="$2"
	if [ -n "$source" ]; then
		[ -f "$source" ] || die "$source not found"
		cp "$source" "$out"
	elif [ -f "$DIR/../fedigest.tar.gz" ]; then
		cp "$DIR/../fedigest.tar.gz" "$out"
	else
		die "no release archive: pass one, or put fedigest.tar.gz next to $DIR"
	fi
}

backup_data() {
	if ! "${COMPOSE[@]}" ps --status running --services 2>/dev/null | grep -qx app; then
		echo "==> App isn't running; skipping the data backup"
		return
	fi
	mkdir -p backups
	local name
	name="fedigest-$(date +%Y%m%d-%H%M%S).tar.gz"
	echo "==> Backing up data to backups/$name"
	quiet "${COMPOSE[@]}" exec -T app ./fedigest backup "/app/data/$name" || die "the backup failed"
	quiet "${COMPOSE[@]}" cp "app:/app/data/$name" "backups/$name" || die "copying the backup out failed"
	quiet "${COMPOSE[@]}" exec -T app rm -f "/app/data/$name" || true
	ls -1t backups/fedigest-*.tar.gz | tail -n +$((KEEP_BACKUPS + 1)) | xargs -r rm -f
}

# quiet runs a command, showing its output only if it fails.
quiet() {
	local out
	if ! out="$("$@" 2>&1)"; then
		printf '%s\n' "$out" >&2
		return 1
	fi
}

# managed lists the top-level entries that belong to the code, i.e.
# everything except local configuration and data kept next to it.
managed() {
	find . -mindepth 1 -maxdepth 1 \
		! -name .env ! -name backups ! -name .previous.tar.gz ! -name .previous-commit ! -name .git \
		! -name docker-compose.override.yml ! -name '*.tar.gz' -printf '%P\n'
}

save_previous() {
	echo "==> Keeping the current code in .previous.tar.gz"
	managed | tar czf .previous.tar.gz -T -
}

replace_code() {
	local new="$1"
	echo "==> Installing the new code"
	managed | xargs -r rm -rf
	(cd "$new" && find . -mindepth 1 -maxdepth 1 ! -name .env -printf '%P\n') | while read -r entry; do
		cp -a "$new/$entry" "$DIR/$entry"
	done
	chmod +x "$DIR/deploy.sh" 2>/dev/null || true
}

rollback() {
	if [ "$GIT" = 1 ]; then
		[ -f .previous-commit ] || die "nothing to roll back to"
		echo "==> Restoring the previous code ($(cut -c1-7 .previous-commit))"
		git reset --hard --quiet "$(cat .previous-commit)"
	else
		[ -f .previous.tar.gz ] || die "nothing to roll back to"
		echo "==> Restoring the previous code"
		managed | xargs -r rm -rf
		tar xzf .previous.tar.gz
	fi
	build_image && "${COMPOSE[@]}" up -d && wait_healthy \
		&& echo "==> Back on $(cat VERSION 2>/dev/null || echo 'the previous version')" \
		|| die "the previous version didn't start either; see: docker compose logs app"
	echo "    The database isn't rolled back. If the failed update changed it, restore"
	echo "    the newest file in backups/ (see \"Restoring a backup\" in README.md)."
}

# build_image builds the image, printing each step once, when it finishes.
build_image() {
	local start=$SECONDS
	"${COMPOSE[@]}" build 2>&1 | build_steps || return 1
	echo "    Built in $((SECONDS - start))s"
}

# build_steps condenses BuildKit's plain output ("#5 [build 2/6] WORKDIR
# /src" ... "#5 DONE 0.1s") to one line per step with its time, like the
# interactive view's final list. Other lines (Compose's own, and the error
# summary with the failing step's output) are passed through.
build_steps() {
	local line id rest
	local -A title=()
	while IFS= read -r line; do
		if [[ $line =~ ^#([0-9]+)\ (.*)$ ]]; then
			id=${BASH_REMATCH[1]} rest=${BASH_REMATCH[2]}
			if [[ -z ${title[$id]+set} ]]; then
				title[$id]=$rest
			elif [[ $rest =~ ^DONE\ ([0-9.]+s)$ ]]; then
				printf ' => %-62.62s %7s\n' "${title[$id]}" "${BASH_REMATCH[1]}"
			elif [[ $rest == CACHED ]]; then
				printf ' => %-62.62s %7s\n' "CACHED ${title[$id]}" "0.0s"
			elif [[ $rest == ERROR* ]]; then
				printf ' => %s\n    %s\n' "${title[$id]}" "$rest"
			fi
		elif [[ $line =~ [^[:space:]] ]]; then
			printf '%s\n' "$line"
		fi
	done
}

# wait_healthy waits a minute or two for the site to answer on the port
# Compose publishes (docker-compose.override.yml included).
wait_healthy() {
	local port
	port="$("${COMPOSE[@]}" port app 8080 2>/dev/null | head -1 | sed 's/.*://')"
	port="${port:-8080}"
	echo "==> Waiting for the site on port $port"
	for _ in $(seq "${HEALTH_TRIES:-30}"); do
		if curl -fsS --max-time 5 -o /dev/null "http://127.0.0.1:$port/robots.txt" 2>/dev/null; then
			return 0
		fi
		sleep 2
	done
	return 1
}

main "$@"
