#!/usr/bin/env bash

# SPDX-FileCopyrightText: 2025 Christoph Mewes, https://codeberg.org/xrstf/uget
# SPDX-License-Identifier: MIT
#
# µget 0.2.0 – your friendly downloader
# -------------------------------------
#
# µget can download software as binaries, archives or Go modules.
#
# Usage: ./uget.sh URL_PATTERN VERSION
#
# µget supports a large range of environment variables to customize its
# behaviour.

set -euo pipefail

###############################################################################
# Configuration

# UGET_UPDATE can be set to true when the VERSION parameter of a program has
# been updated and you want µget to update all known variants (based on the
# checksums file). When UGET_CHECKSUMS is not in use, this variable has no
# effect.
UGET_UPDATE=${UGET_UPDATE:-false}

# UGET_DIRECTORY is where downloaded binaries will be placed.
UGET_DIRECTORY="${UGET_DIRECTORY:-_tools}"

# UGET_CHECKSUMS is an optional path to a checksum file that µget should use to
# ensure subsequent downloads match previously known checksums to prevent
# tampering on the server side.
UGET_CHECKSUMS="${UGET_CHECKSUMS:-}"

# UGET_TEMPDIR is the root directory to use when creating new temporary dirs.
UGET_TEMPDIR="${UGET_TEMPDIR:-/tmp}"

# UGET_PRINT_PATH can be set to "relative" to make µget only omit log output
# and only print the relative path to the binary, and set to "absolute" to
# output the absolute path.
UGET_PRINT_PATH="${UGET_PRINT_PATH:-no}"

# UGET_HASHFUNC is the hashing function used to calculate file checksums. The
# output of this program is processed with awk to only print the first column.
UGET_HASHFUNC="${UGET_HASHFUNC:-sha256sum}"

# UGET_GO_BUILD_CMD overwrites the default call to "go build" when installing
# a Go module. Use this to inject custom Go flags or toolchains.
# The given command is called with "-o BINARY_NAME MODULE_URL" with the pwd
# being inside a fake module that depends on the given module.
UGET_GO_BUILD_CMD="${UGET_GO_BUILD_CMD:-go build}"

# UGET_GO_CHECKSUMS can be set to true to force checksums even for Go modules.
# This is disabled by default because the exact binaries being built depend on
# a lot of factors and usually it's a hassle to ensure everyone in a project
# has the *exact* same build environment. Go modules already make use of Google's
# GOSUMDB and should be "safe enough" by default with µget checksums.
UGET_GO_CHECKSUMS=${UGET_GO_CHECKSUMS:-false}

###############################################################################
# Function library

uget::mktemp() {
  # --tmpdir does not work on MacOS
  mktemp -d -p "$ABS_UGET_TEMPDIR"
}

uget::log() {
  if [[ "$UGET_PRINT_PATH" == "no" ]]; then
    echo "$@" >&2
  fi
}

uget::error() {
  echo "$@" >&2
}

uget::lowercase() {
  cat | tr '[:upper:]' '[:lower:]'
}

uget::checksum::enabled() {
  [[ -n "$UGET_CHECKSUMS" ]]
}

uget::checksum::check() {
  local kvString="$1"
  local downloadedBinary="$2"

  if ! uget::checksum::enabled; then return; fi

  local newChecksum
  newChecksum="$(uget::checksum::calculate "$downloadedBinary")"

  local oldChecksum
  oldChecksum="$(uget::checksum::read "$kvString")"

  if [[ -n "$oldChecksum" ]] && [[ "$oldChecksum" != "$newChecksum" ]]; then
    uget::error
    uget::error "  *************************************************************************"
    uget::error "  SECURITY ERROR"
    uget::error
    uget::error "  The downloaded file $downloadedBinary does not have the expected checksum."
    uget::error
    uget::error "  Expected: $oldChecksum"
    uget::error "  Actual  : $newChecksum"
    uget::error
    uget::error "  If you are updating $BINARY, this error is expected."
    uget::error "  Re-run this command with the environment variable UGET_UPDATE=true to make"
    uget::error "  µget update the checksums for all known variants of $BINARY."
    uget::error "  *************************************************************************"
    uget::error

    return 1
  fi

  uget::checksum::write "$kvString" "$newChecksum"
}

uget::checksum::read() {
  local kvString="$1"

  if [[ -f "$UGET_CHECKSUMS" ]]; then
    awk -F'|' "{ if (\$1 == \"$BINARY\" && \$2 == \"$kvString\") print \$3 }" "$UGET_CHECKSUMS"
  fi
}

uget::checksum::calculate() {
  "$UGET_HASHFUNC" "$1" | awk '{ print $1 }'
}

uget::checksum::write() {
  local kvString="$1"
  local checksum="$2"

  if [[ -f "$UGET_CHECKSUMS" ]]; then
    local tempDir
    tempDir="$(uget::mktemp)"

    # use awk to drop any existing hash for this binary/keyvalue combo
    # (for better readability, do not invert the condition here);
    # grep will drop any empty lines
    awk \
      -F'|' \
      "{ if (\$1 == \"$BINARY\" && \$2 == \"$kvString\") {} else print }" \
      "$UGET_CHECKSUMS" | grep . > "$tempDir/checksums.txt"

    # add our new checksum
    echo "$BINARY|$kvString|$checksum" >> "$tempDir/checksums.txt"

    # sort the file because it looks nicer and prevents ugly git diffs
    cat "$tempDir/checksums.txt" | sort > "$UGET_CHECKSUMS"

    rm -rf -- "$tempDir"
  else
    # start a new file
    echo "$BINARY|$kvString|$checksum" >> "$UGET_CHECKSUMS"
  fi
}

uget::url::placeholder() {
  case "$1" in
    GOARCH) go env GOARCH ;;
    GOOS)   go env GOOS ;;
    UARCH)  uname -m | uget::lowercase ;;
    UOS)    uname -s | uget::lowercase ;;
    *)      uget::error "Unexpected placeholder $1."; return 1 ;;
  esac
}

# valueFromPair returns "foo" for ";myvalue=foo;myothervalue=bar;" when called with
# "myvalue" as the key. The kv string must already be surrounded with semicolons.
uget::url::valueFromPair() {
  local kvString="$1"
  local key="$2"

  (echo "$kvString" | grep -oE ";$key=([^;]+)" || true) | cut -f2 -d'='
}

uget::url::setKeyInPairs() {
  local kvString="$1"
  local key="$2"
  local value="$3"

  # first take the existing pairs but without the one for the given key,
  # then add a new pair and sort it all together, strip empty lines in case
  # kvstring as empty, then join the multiple lines back into a single line
  # and drop the trailing ';'
  (
    echo "$kvString" | tr ';' "\n" | (grep -vE "^$key=" || true)
    echo "$key=$value"
  ) | sort | sed '/^[[:space:]]*$/d' | tr "\n" ';' | sed 's/;$//'
}

uget::url::findPlaceholders() {
  # match all {...},
  # then sort and return only unique values (no need to replace the
  # same placeholder multiple times) (this is important to allow for consistent
  # matches when awk'ing through the checksum file),
  # then remove braces and
  # finally turn into a singleline string
  echo "$1" |
    grep -oE '\{[A-Z0-9_]+\}' |
    sort -u |
    tr -d '{}' |
    awk '{printf("%s ", $0)}'
}

# replaceLive() use live system-data to replace placeholders in the given pattern.
# It returns a string of form "KVSTRING|URL".
uget::url::replaceLive() {
  local urlPattern="$1"
  local usedPlaceholders=""

  for placeholder in $(uget::url::findPlaceholders "$urlPattern"); do
    # version is treated specially
    [[ "$placeholder" == "VERSION" ]] && continue

    replacement="$(uget::url::placeholder "$placeholder")"
    urlPattern="${urlPattern//\{$placeholder\}/$replacement}"

    # remember this placeholder and its value
    usedPlaceholders="$usedPlaceholders;$placeholder=$replacement"
  done

  # trim leading ";" (this is safe even for zero-length strings)
  usedPlaceholders="${usedPlaceholders:1}"

  echo "$usedPlaceholders|$urlPattern"
}

# replaceWithArgs() does not ask the current system for the values when replacing
# a placeholder, but uses a given key-value pair string as the source. It also
# only returns the resulting string, since the used placeholders are known to
# the caller already.
uget::url::replaceWithArgs() {
  local urlPattern="$1"
  local kvString="$2"

  # make matching easier
  kvString=";$kvString;"

  for placeholder in $(uget::url::findPlaceholders "$urlPattern"); do
    # version is treated specially
    [[ "$placeholder" == "VERSION" ]] && continue

    replacement="$(uget::url::valueFromPair "$kvString" "$placeholder")"
    if [[ -z "$replacement" ]]; then
      uget::error "Found no replacement string for placeholder $placeholder."
      exit 1
    fi

    urlPattern="${urlPattern//\{$placeholder\}/$replacement}"
  done

  echo "$urlPattern"
}

# returns "KVSTRING URL"
uget::url::build() {
  local kvString="${1:-}"
  local pattern

  if [[ -z "$kvString" ]]; then
    result="$(uget::url::replaceLive "$URL_PATTERN")"
    kvString="$(echo "$result" | cut -d'|' -f1)"
    pattern="$(echo "$result" | cut -d'|' -f2)"
  else
    pattern="$(uget::url::replaceWithArgs "$URL_PATTERN" "$kvString")"
  fi

  pattern="${pattern//\{VERSION\}/$VERSION}"

  echo "$kvString|$pattern"
}

# uget::fetch() detects whether wget or curl is available for downloading something.
uget::fetch() {
  local url="$1"

  if command -v curl &> /dev/null; then
    curl --fail -LO "$url"
  elif command -v wget &> /dev/null; then
    wget "$url"
  else
    uget::error "Neither curl nor wget are available."
    return 1
  fi
}

# uget::download performs the actual download
# and places the binary in a given directory.
uget::download() {
  local destinationDir="$1"
  local kvString="$2"
  local url="$3"

  local startDir
  startDir="$(pwd)"

  local tempDir
  tempDir="$(uget::mktemp)"

  cd "$tempDir"

  if $GO_MODULE; then
    # make sure GOARCH and GOOS are set correctly
    os="$(uget::url::valueFromPair ";$kvString;" "GOOS")"
    arch="$(uget::url::valueFromPair ";$kvString;" "GOARCH")"

    # since we crosscompile, we cannot do "GOBIN=(somewhere) go install url@version",
    # because Go considers this to be dangerous behaviour:
    # https://github.com/golang/go/issues/57485
    # Instead we create a dummy module and use the desired program as a dependency,
    # *then* we're allowed to crosscompile it anywhere using "go build". Go figure.

    go mod init temp 2>/dev/null
    go get "$url@$VERSION"

    GOFLAGS=-trimpath GOARCH="$arch" GOOS="$os" GOBIN=$(realpath .) $UGET_GO_BUILD_CMD -o "$BINARY" "$url"

    mv "$BINARY" "$destinationDir/$BINARY"
  else
    uget::fetch "$url"
    archive="$(ls)"

    if ! $UNCOMPRESSED; then
      case "$archive" in
        *.tar.gz | *.tgz)
          tar xzf "$archive"
          ;;
        *.tar.bz2 | *.tbz2)
          tar xjf "$archive"
          ;;
        *.tar.xz | *.txz)
          tar xJf "$archive"
          ;;
        *.zip)
          unzip "$archive"
          ;;
        *)
          uget::error "Unknown file type: $archive"
          return 1
      esac
    fi

    # pattern is explicitly meant to be interpreted by the shell
    # shellcheck disable=SC2086
    mv $BINARY_PATTERN "$destinationDir/$BINARY"
    chmod +x "$destinationDir/$BINARY"
  fi

  cd "$startDir"
  rm -rf -- "$tempDir"
}

# ready() checks if the desired binary already exists in the desired version.
uget::ready() {
  local fullFinalPath="$ABS_UGET_DIRECTORY/$BINARY"
  local versionFile="$fullFinalPath.version"

  [ -f "$fullFinalPath" ] && [ -f "$versionFile" ] && [ "$VERSION" == "$(cat "$versionFile")" ]
}

# install() downloads the binary, checks the checksum and places it in UGET_DIRECTORY.
uget::install() {
  local kvString="$1"
  local url="$2"
  local fullFinalPath="$ABS_UGET_DIRECTORY/$BINARY"
  local versionFile="$fullFinalPath.version"

  local startDir
  startDir="$(pwd)"

  local tempDir
  tempDir="$(uget::mktemp)"

  uget::log "Downloading $BINARY version $VERSION ..."
  uget::download "$tempDir" "$kvString" "$url"

  fullBinaryPath="$tempDir/$BINARY"
  uget::checksum::check "$kvString" "$fullBinaryPath"

  # if everything is fine, place the binary in its final location
  mv "$fullBinaryPath" "$fullFinalPath"
  echo -n "$VERSION" > "$versionFile"

  uget::log "Installed at $UGET_DIRECTORY/$BINARY."

  cd "$startDir"
  rm -rf -- "$tempDir"
}

# update() downloads the binary, updates the checksums and discards the binary.
uget::update() {
  local kvString="$1"
  local url="$2"

  local startDir
  startDir="$(pwd)"

  local tempDir
  tempDir="$(uget::mktemp)"

  uget::log "  ~> $kvString"
  uget::download "$tempDir" "$kvString" "$url"

  fullBinaryPath="$tempDir/$BINARY"

  local checksum
  checksum="$(uget::checksum::calculate "$fullBinaryPath")"

  uget::checksum::write "$kvString" "$checksum"

  cd "$startDir"
  rm -rf -- "$tempDir"
}

###############################################################################
# General Setup Logic

# get CLI flags
export URL_PATTERN="$1"
export BINARY="$2"
export VERSION="$3"
BINARY_PATTERN="${4:-**/$BINARY}"

# additional per-tool configuration, which is not prefixed with UGET_ because
# it's scoped to single tools only
GO_MODULE=${GO_MODULE:-false}
UNCOMPRESSED=${UNCOMPRESSED:-false}

# ensure target directory exists
mkdir -p "$UGET_DIRECTORY" "$UGET_TEMPDIR"

ABS_UGET_DIRECTORY="$(realpath "$UGET_DIRECTORY")"
ABS_UGET_TEMPDIR="$(realpath "$UGET_TEMPDIR")"

if $GO_MODULE && ! $UGET_GO_CHECKSUMS; then
  UGET_CHECKSUMS=""
fi

if uget::checksum::enabled; then
  touch "$UGET_CHECKSUMS"
  UGET_CHECKSUMS="$(realpath "$UGET_CHECKSUMS")"
else
  if $UGET_UPDATE && $GO_MODULE; then
    uget::error "Checksums are disabled for Go modules, cannot update them."
    exit 1
  fi

  UGET_UPDATE=false
fi

###############################################################################
# Main application logic

# When in update mode, we do not download the binary for the current system
# only, but instead for all known variants based on the checksums file,
# recalculate the checksums and then discard the temporary binaries.

if $UGET_UPDATE; then
  uget::log "Updating checksums for $BINARY ..."

  # Find and process all known variants...
  while read -r kvString; do
    result="$(uget::url::build "$kvString")"
    url="$(echo "$result" | cut -d'|' -f2)"

    # download binary into tempdir, update checksums, but then delete it again
    uget::update "$kvString" "$url"
  done < <(awk -F'|' "{ if (\$1 == \"$BINARY\") print \$2 }" "$UGET_CHECKSUMS")

  uget::log "All checksums were updated."
else
  if ! uget::ready; then
    # Replace placeholders in the URL with system-specific data, like arch or OS
    result="$(uget::url::build)"
    kvString="$(echo "$result" | cut -d'|' -f1)"
    url="$(echo "$result" | cut -d'|' -f2)"

    # Go modules usually do not have arch/os infos in their module names, but
    # the resulting binary still depends on the local system. To support checksums
    # for different machines, we always assume GOARCH and GOOS are "used placeholders".
    if $GO_MODULE; then
      kvString="$(uget::url::setKeyInPairs "$kvString" "GOARCH" "$(go env GOARCH)")"
      kvString="$(uget::url::setKeyInPairs "$kvString" "GOOS" "$(go env GOOS)")"
    fi

    uget::install "$kvString" "$url"
  fi

  case "$UGET_PRINT_PATH" in
    absolute) echo "$ABS_UGET_DIRECTORY/$BINARY" ;;
    relative) echo "$UGET_DIRECTORY/$BINARY" ;;
  esac
fi
