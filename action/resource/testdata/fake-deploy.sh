#!/bin/sh
# Fake deploy.sh used by runtime_test.go. Echos the env vars the engine is
# supposed to inject so the test can assert the renderer captured each line.
set -eu

echo "fake-deploy: STACK_NAME=${STACK_NAME:-}"
echo "fake-deploy: AWS_PROFILE=${AWS_PROFILE:-}"
echo "fake-deploy: AWS_REGION=${AWS_REGION:-}"
echo "fake-deploy: TEMPLATE_PATH=${TEMPLATE_PATH:-}"
echo "fake-deploy: PARAMETERS_PATH=${PARAMETERS_PATH:-}"
echo "warning on stderr" >&2
exit 0
