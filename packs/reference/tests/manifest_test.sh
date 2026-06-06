#!/bin/sh
# Exercise the reference pack without hitting AWS.
#
# Validates everything we can validate without the resource engine
# (PR-10): JSON fixture is well-formed, deploy.sh's dry-run output
# matches the golden file, shellcheck is clean if installed.
#
# When PR-05/06/10 land, a Go integration test will additionally drive
# the runtime end-to-end against the same fixtures.

set -eu

script_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
pack_dir=$(CDPATH='' cd -- "${script_dir}/.." && pwd)
fixtures_dir="${script_dir}/fixtures"

params_json="${fixtures_dir}/alb-params.json"
golden_dryrun="${fixtures_dir}/alb-deploy.dryrun.txt"
deploy_sh="${pack_dir}/deploy.sh"

pass=0
fail=0

ok() { printf '  ok    %s\n' "$1"; pass=$((pass + 1)); }
bad() { printf '  FAIL  %s\n' "$1" >&2; fail=$((fail + 1)); }

require_cmd() {
	command -v "$1" >/dev/null 2>&1 || {
		printf '%s: required command %s not on PATH\n' "$(basename "$0")" "$1" >&2
		exit 2
	}
}

require_cmd jq
require_cmd diff

for f in \
	"${pack_dir}/pack.yaml" \
	"${pack_dir}/manifests/alb.yaml" \
	"${deploy_sh}" \
	"${params_json}" \
	"${golden_dryrun}"; do
	if [ -f "${f}" ]; then
		ok "exists: $(basename "${f}")"
	else
		bad "missing: ${f}"
	fi
done

if [ -x "${deploy_sh}" ]; then
	ok "deploy.sh is executable"
else
	bad "deploy.sh is not executable"
fi

if jq empty "${params_json}" >/dev/null 2>&1; then
	ok "alb-params.json is valid JSON"
else
	bad "alb-params.json is not valid JSON"
fi

actual_dryrun=$(
	STACK_NAME=alb-stack-demo-prd \
	AWS_PROFILE=demo \
	AWS_REGION=eu-west-1 \
	TEMPLATE_PATH=../../alb-template/alb-template.yaml \
	PARAMETERS_PATH="${params_json}" \
	DRY_RUN=1 \
	"${deploy_sh}"
)

if printf '%s\n' "${actual_dryrun}" | diff -u "${golden_dryrun}" - >/dev/null; then
	ok "deploy.sh DRY_RUN matches alb-deploy.dryrun.txt"
else
	bad "deploy.sh DRY_RUN drift — diff vs golden:"
	printf '%s\n' "${actual_dryrun}" | diff -u "${golden_dryrun}" - || true
fi

if command -v shellcheck >/dev/null 2>&1; then
	if shellcheck "${deploy_sh}" "$0"; then
		ok "shellcheck clean"
	else
		bad "shellcheck reported issues"
	fi
else
	printf '  skip  shellcheck not installed\n'
fi

printf '\n%d passed, %d failed\n' "${pass}" "${fail}"
[ "${fail}" -eq 0 ]
