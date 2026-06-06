#!/bin/sh
# Generic CFN deploy wrapper.
# Env: STACK_NAME AWS_PROFILE AWS_REGION TEMPLATE_PATH PARAMETERS_PATH [DRY_RUN]

set -eu
# pipefail is bash/ksh-only; guard so the script stays POSIX-portable.
# shellcheck disable=SC3040
(set -o pipefail 2>/dev/null) && set -o pipefail

die() {
	code=$1
	shift
	printf 'deploy.sh: %s\n' "$*" >&2
	exit "$code"
}

require_env() {
	eval "val=\${$1:-}"
	[ -n "${val}" ] || die 2 "required env var ${1} is unset"
}

require_env STACK_NAME
require_env AWS_PROFILE
require_env AWS_REGION
require_env TEMPLATE_PATH
require_env PARAMETERS_PATH

[ -f "${TEMPLATE_PATH}" ] || [ "${DRY_RUN:-0}" = "1" ] \
	|| die 3 "TEMPLATE_PATH does not exist: ${TEMPLATE_PATH}"
[ -f "${PARAMETERS_PATH}" ] \
	|| die 3 "PARAMETERS_PATH does not exist: ${PARAMETERS_PATH}"

command -v jq >/dev/null 2>&1 || die 2 "jq is required but not on PATH"

# Translate {"Key":"Value", ...} -> [{"ParameterKey":"K","ParameterValue":"V"},...].
# Sorted by key for deterministic output (the golden-file diff in
# manifest_test.sh relies on it). The CFN-shaped JSON is passed to
# aws-cli via file:// so values containing spaces or shell-meta chars
# round-trip safely (a flat "Key=Value Key=Value" string would not).
cfn_params=$(jq '
	to_entries
	| sort_by(.key)
	| map({ParameterKey: .key, ParameterValue: (.value | tostring)})
' "${PARAMETERS_PATH}")

if [ "${DRY_RUN:-0}" = "1" ]; then
	printf 'PARAMETERS=%s\n' "${cfn_params}"
	printf 'COMMAND=aws --profile %s --region %s cloudformation deploy ' \
		"${AWS_PROFILE}" "${AWS_REGION}"
	printf -- '--stack-name %s --template-file %s ' \
		"${STACK_NAME}" "${TEMPLATE_PATH}"
	printf -- '--capabilities CAPABILITY_NAMED_IAM --no-fail-on-empty-changeset '
	printf -- '--parameter-overrides file://<PARAMETERS_FILE>\n'
	exit 0
fi

command -v aws >/dev/null 2>&1 || die 2 "aws CLI is required but not on PATH"

# aws-cli's --parameter-overrides accepts a file:// pointer to the
# CFN-shaped JSON above. Stage it in a temp file so we never mutate
# the engine-written PARAMETERS_PATH.
tmp_params=$(mktemp -t packwright-cfn-params.XXXXXX)
trap 'rm -f "${tmp_params}"' EXIT INT TERM
printf '%s\n' "${cfn_params}" >"${tmp_params}"

exec aws \
	--profile "${AWS_PROFILE}" \
	--region "${AWS_REGION}" \
	cloudformation deploy \
	--stack-name "${STACK_NAME}" \
	--template-file "${TEMPLATE_PATH}" \
	--capabilities CAPABILITY_NAMED_IAM \
	--no-fail-on-empty-changeset \
	--parameter-overrides "file://${tmp_params}"
