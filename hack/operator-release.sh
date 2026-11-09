#!/usr/bin/env bash
# Version the operator release (images and Helm charts) from conventional
# commits.
#
# Operator releases are the v<major>.<minor>.<patch> tags that build-push.yml
# and release-chart.yml publish from. Terraform modules have their own tag
# series (hack/terraform-module-release.sh) and do not count here.
#
# Only commits since the latest v* tag that touch operator sources count:
# everything except docs, Markdown, Terraform, CI, the devcontainer, and e2e
# tests. Among those commits:
#   feat!: or a BREAKING CHANGE footer -> major (minor while the major is 0)
#   feat:                               -> minor
#   anything else                       -> patch
#
# Requires a full (non-shallow) clone so every tag is visible.
#
#   hack/operator-release.sh version [auto|patch|minor|major] [rev]
#   hack/operator-release.sh notes <version> [rev]
#
# version prints nothing when "auto" finds no operator changes to release.

set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "${root}"

if [ "$(git rev-parse --is-shallow-repository)" = "true" ]; then
  echo "full git history is required to version the operator" >&2
  exit 1
fi

# Paths that change what the images or charts contain.
operator_paths=(
  .
  ':(exclude)docs'
  ':(exclude)*.md'
  ':(exclude)LICENSE'
  ':(exclude)terraform'
  ':(exclude)hack/terraform-module-release.sh'
  ':(exclude).github'
  ':(exclude).devcontainer'
  ':(exclude)test'
)

latest_version() {
  local rev="$1"
  # Only tags reachable from rev, so a release never skips versions on main.
  git tag -l 'v*' --merged "${rev}" \
    | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' \
    | sed 's/^v//' \
    | sort -t. -k1,1n -k2,2n -k3,3n \
    | tail -n 1 || true
}

commit_range() {
  local current="$1" rev="$2"
  if [ -z "${current}" ]; then
    printf '%s\n' "${rev}"
  else
    printf 'v%s..%s\n' "${current}" "${rev}"
  fi
}

detect_bump() {
  local range="$1" log
  log="$(git log "${range}" --format='%s%n%b' -- "${operator_paths[@]}")"
  if [ -z "${log}" ]; then
    return 0
  fi
  if printf '%s\n' "${log}" | grep -Eq 'BREAKING CHANGE|^[[:alnum:]]+(\([^)]*\))?!:'; then
    echo major
  elif printf '%s\n' "${log}" | grep -Eq '^feat(\([^)]*\))?:'; then
    echo minor
  else
    echo patch
  fi
}

cmd_version() {
  local bump="${1:-auto}" rev="${2:-HEAD}"
  local current major minor patch
  case "${bump}" in
    auto | patch | minor | major) ;;
    *)
      echo "bump must be auto, patch, minor, or major: ${bump}" >&2
      exit 2
      ;;
  esac
  current="$(latest_version "${rev}")"
  if [ "${bump}" = "auto" ]; then
    bump="$(detect_bump "$(commit_range "${current}" "${rev}")")"
    if [ -z "${bump}" ]; then
      return 0
    fi
    # Before 1.0.0, breaking changes bump the minor version. Reaching 1.0.0
    # is a deliberate choice: run the workflow by hand with bump=major.
    if [ "${bump}" = "major" ] && [ "${current%%.*}" = "0" ]; then
      bump=minor
    fi
  fi
  if [ -z "${current}" ]; then
    current="0.0.0"
  fi
  IFS=. read -r major minor patch <<<"${current}"
  case "${bump}" in
    major) printf '%s.0.0\n' "$((major + 1))" ;;
    minor) printf '%s.%s.0\n' "${major}" "$((minor + 1))" ;;
    patch) printf '%s.%s.%s\n' "${major}" "${minor}" "$((patch + 1))" ;;
  esac
}

cmd_notes() {
  local version="${1:?version required}" rev="${2:-HEAD}"
  local current repo owner log
  current="$(latest_version "${rev}")"
  repo="${GITHUB_REPOSITORY:-opsarrayllc/data-platform-operators}"
  owner="${repo%%/*}"

  cat <<EOF
## v${version}

Images (\`linux/amd64\`, \`linux/arm64\`):

* \`ghcr.io/${owner}/data-lake-operator:${version}\`
* \`ghcr.io/${owner}/data-analytics-operator:${version}\`
* \`ghcr.io/${owner}/data-orchestration-operator:${version}\`

Helm charts: \`oci://ghcr.io/${owner}/charts/<chart> --version ${version}\`

## Changes

EOF
  log="$(git log "$(commit_range "${current}" "${rev}")" --pretty=format:'* %s (%h)' -- "${operator_paths[@]}")"
  if [ -z "${log}" ]; then
    echo "No operator changes; release cut by hand."
  else
    printf '%s\n' "${log}"
  fi
}

usage() {
  echo "usage: hack/operator-release.sh <version|notes> [args]" >&2
  exit 2
}

command="${1:-}"
shift || true
case "${command}" in
  version) cmd_version "$@" ;;
  notes) cmd_notes "$@" ;;
  *) usage ;;
esac
