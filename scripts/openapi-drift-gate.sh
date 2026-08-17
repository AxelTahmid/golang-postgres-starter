#!/usr/bin/env bash
# Regenerate the contract twice, proving both reproducibility and that the
# reviewed artifact is current. Historical baselines are not correctness
# inputs: an intentional contract change is accepted by committing the newly
# generated document and its ordinary code-review diff.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

SPEC=internal/server/openapi.json

# The generated contract is a reviewed repository artifact. `git diff` does
# not report an untracked file, so a change that deletes the tracked spec and
# lets generation recreate it could otherwise pass this gate.
if ! git ls-files --error-unmatch "$SPEC" >/dev/null 2>&1; then
    echo "FAIL: $SPEC is not tracked by git." >&2
    echo "      Restore and commit the generated contract artifact." >&2
    exit 1
fi

sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

echo "==> generating $SPEC (pass 1)"
GOEXPERIMENT=jsonv2 go run ./cmd/openapi
first=$(sha256 "$SPEC")

echo "==> generating $SPEC (pass 2)"
GOEXPERIMENT=jsonv2 go run ./cmd/openapi
second=$(sha256 "$SPEC")

if [ "$first" != "$second" ]; then
    echo "FAIL: OpenAPI generation is nondeterministic ($first != $second)." >&2
    exit 1
fi

if ! git diff --exit-code -- "$SPEC"; then
    echo "FAIL: committed $SPEC is stale." >&2
    echo "      Run 'make openapi', review the contract diff, and commit it." >&2
    exit 1
fi

echo "OK: deterministic and current ($first)"
