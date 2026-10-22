#!/bin/sh
# Builds the landing page's agent prompts, site/prompts/{new-app,migrate}.md,
# from the templates in scripts/prompts/. A template line "@file <path>"
# becomes that file of templates/starter, so the prompts always carry the
# starter's real contract and code.
#
#   scripts/prompts.sh           write site/prompts/
#   scripts/prompts.sh --check   fail if site/prompts/ is stale (make test)
set -eu
cd "$(dirname "$0")/.."

starter=templates/starter

render() {
	while IFS= read -r line || [ -n "$line" ]; do
		case "$line" in
		"@file "*)
			path=${line#@file }
			[ -f "$starter/$path" ] || { echo "$1: no such starter file: $path" >&2 && exit 1; }
			case "$path" in
			*.go) lang=go ;;
			*.ts) lang=ts ;;
			*.tsx) lang=tsx ;;
			*.json) lang=json ;;
			*.yml) lang=yaml ;;
			*.sh) lang=sh ;;
			*.md) lang=markdown ;;
			*.css) lang=css ;;
			*.html) lang=html ;;
			Makefile) lang=make ;;
			*) lang= ;;
			esac
			# A longer fence when the file has fences of its own.
			fence='```'
			if grep -q '```' "$starter/$path"; then fence='````'; fi
			printf '### `%s`\n\n%s%s\n' "$path" "$fence" "$lang"
			cat "$starter/$path"
			printf '%s\n' "$fence"
			;;
		*) printf '%s\n' "$line" ;;
		esac
	done <"$1"
}

out=site/prompts
if [ "${1:-}" = --check ]; then
	out=$(mktemp -d)
	trap 'rm -rf "$out"' EXIT
fi
mkdir -p "$out"

for name in new-app migrate; do
	render "scripts/prompts/$name.md" >"$out/$name.md"
	if [ "$out" != site/prompts ] && ! diff -u "site/prompts/$name.md" "$out/$name.md"; then
		echo "site/prompts/$name.md is out of date with templates/starter or scripts/prompts: run make site" >&2
		exit 1
	fi
done
