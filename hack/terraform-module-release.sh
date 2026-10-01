#!/usr/bin/env bash
# Plan, version, and package one Terraform module for a GitHub release.
#
# Each module under terraform/modules/<name> has its own tag series,
# terraform-data-lake-<cloud>-v<major>.<minor>.<patch>. Those tags deliberately do not
# match the operator's v* tags, which publish the image and Helm charts.
#
# The first release is 1.0.0. After that, commits that touch the module since
# its latest tag choose the bump:
#   feat!: or a BREAKING CHANGE footer -> major
#   feat:                               -> minor
#   anything else                       -> patch
#
# Requires a full (non-shallow) clone so every module tag is visible.
#
#   hack/terraform-module-release.sh plan
#   hack/terraform-module-release.sh version <module>
#   hack/terraform-module-release.sh notes <module> <version>
#   hack/terraform-module-release.sh package <module> <version> <outdir>

set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "${root}"

if [ "$(git rev-parse --is-shallow-repository)" = "true" ]; then
  echo "full git history is required to version terraform modules" >&2
  exit 1
fi

tag_prefix() {
  printf 'terraform-data-lake-%s-v' "$1"
}

module_dir() {
  printf 'terraform/modules/%s' "$1"
}

list_modules() {
  find terraform/modules -mindepth 1 -maxdepth 1 -type d -printf '%f\n' | sort
}

require_module() {
  local module="$1"
  if [[ ! "${module}" =~ ^[a-z0-9-]+$ ]]; then
    echo "invalid module name: ${module}" >&2
    exit 1
  fi
  if [ ! -d "$(module_dir "${module}")" ]; then
    echo "no such module: ${module}" >&2
    exit 1
  fi
}

latest_version() {
  local module="$1"
  local prefix found
  prefix="$(tag_prefix "${module}")"
  # grep exits 1 when the module has never been released. That is not a failure.
  found="$(git tag -l "${prefix}*" | grep -E "^${prefix}[0-9]+\\.[0-9]+\\.[0-9]+$" || true)"
  if [ -z "${found}" ]; then
    return 0
  fi
  printf '%s\n' "${found}" \
    | sed "s/^${prefix}//" \
    | sort -t. -k1,1n -k2,2n -k3,3n \
    | tail -n 1
}

module_changed() {
  local module="$1"
  local version="$2"
  local dir
  dir="$(module_dir "${module}")"
  if [ -z "${version}" ]; then
    return 0
  fi
  # A module with no diff against its latest release does not need another one.
  if git diff --quiet "$(tag_prefix "${module}")${version}" HEAD -- "${dir}"; then
    return 1
  fi
  return 0
}

next_version() {
  local module="$1"
  local current log major minor patch
  current="$(latest_version "${module}")"
  if ! module_changed "${module}" "${current}"; then
    return 0
  fi
  if [ -z "${current}" ]; then
    printf '1.0.0\n'
    return 0
  fi

  log="$(git log "$(tag_prefix "${module}")${current}..HEAD" --format='%s%n%b' -- "$(module_dir "${module}")")"
  IFS=. read -r major minor patch <<<"${current}"
  if printf '%s\n' "${log}" | grep -Eq 'BREAKING CHANGE|^[[:alnum:]]+(\([^)]*\))?!:'; then
    printf '%s.0.0\n' "$((major + 1))"
  elif printf '%s\n' "${log}" | grep -Eq '^feat(\([^)]*\))?:'; then
    printf '%s.%s.0\n' "${major}" "$((minor + 1))"
  else
    printf '%s.%s.%s\n' "${major}" "${minor}" "$((patch + 1))"
  fi
}

json_names() {
  if [ "$#" -eq 0 ]; then
    printf '[]\n'
    return 0
  fi
  printf '%s\n' "$@" | jq -R . | jq -sc .
}

cmd_plan() {
  local module version
  local -a pending=()
  while IFS= read -r module; do
    version="$(next_version "${module}")"
    if [ -n "${version}" ]; then
      pending+=("${module}")
    fi
  done < <(list_modules)
  json_names "${pending[@]}"
}

cmd_version() {
  local module="${1:?module name required}"
  require_module "${module}"
  next_version "${module}"
}

cmd_notes() {
  local module="${1:?module name required}"
  local version="${2:?version required}"
  require_module "${module}"
  local current repo dir log
  current="$(latest_version "${module}")"
  repo="${GITHUB_REPOSITORY:-opsarrayllc/data-platform-operators}"
  dir="$(module_dir "${module}")"

  cat <<EOF
## terraform-data-lake-${module}-v${version}

\`\`\`hcl
module "warehouse" {
  source = "git::https://github.com/${repo}.git//${dir}?ref=$(tag_prefix "${module}")${version}"
}
\`\`\`

The attached zip is the module directory.

## Changes

EOF
  if [ -z "${current}" ]; then
    echo "Initial release."
    return 0
  fi
  log="$(git log "$(tag_prefix "${module}")${current}..HEAD" --pretty=format:'* %s (%h)' -- "${dir}")"
  if [ -z "${log}" ]; then
    echo "Initial release."
  else
    printf '%s\n' "${log}"
  fi
}

cmd_package() {
  local module="${1:?module name required}"
  local version="${2:?version required}"
  local outdir="${3:?output directory required}"
  require_module "${module}"
  local dest
  mkdir -p "${outdir}"
  dest="${outdir}/terraform-data-lake-${module}-v${version}.zip"
  # Archive the module tree at the zip root so it can be used as a Terraform module.
  git archive --format=zip --output="${dest}" "HEAD:$(module_dir "${module}")"
  if [ ! -s "${dest}" ]; then
    echo "package for ${module} is empty" >&2
    exit 1
  fi
  printf '%s\n' "${dest}"
}

usage() {
  echo "usage: hack/terraform-module-release.sh <plan|version|notes|package> [args]" >&2
  exit 2
}

command="${1:-}"
shift || true
case "${command}" in
  plan) cmd_plan ;;
  version) cmd_version "$@" ;;
  notes) cmd_notes "$@" ;;
  package) cmd_package "$@" ;;
  *) usage ;;
esac
