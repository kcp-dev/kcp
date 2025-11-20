#!/bin/sh

# SPDX-FileCopyrightText: 2025 Christoph Mewes, https://codeberg.org/xrstf/uget
# SPDX-License-Identifier: MIT
#
# µget 0.4.0 – your friendly downloader
# -------------------------------------
#
# µget can download software as binaries, archives or Go modules.
#
# Usage: ./uget.sh URL_PATTERN BINARY_NAME VERSION [EXTRACT_PATTERN=**/$BINARY_NAME]

set -eu

###############################################################################
# Configuration

# µget supports a large range of environment variables to customize its
# behaviour. Global configuration settings are prefixed with UGET_, settings
# usually meant for a single tool only have no prefix (like GO_MODULE).

# GO_MODULE can be set to true to create a dummy Go module, add the given
# URL and version as a dependency and then go build the desired binary. Note
# that Go modules do not use checksums by default, see $UGET_GO_CHECKSUMS.
GO_MODULE=${GO_MODULE:-false}

# UNCOMPRESSED can be set to true if the downloaded file is not an archive,
# but the binary itself and doesn't need decompressing.
UNCOMPRESSED=${UNCOMPRESSED:-false}

# UGET_UPDATE can be set to true when the VERSION parameter of a program has
# been updated and you want µget to update all known variants (based on the
# checksums file) before installing the correct variant for the current system.
# When UGET_CHECKSUMS is not in use, this variable has no effect.
# Use UGET_UPDATE_ONLY if you want to just update the checksums without also
# installing the binary.
UGET_UPDATE=${UGET_UPDATE:-false}

# UGET_UPDATE_ONLY is like UGET_UPDATE, but does not install the binary on
# the current system (i.e. it only touches the checksum file).
UGET_UPDATE_ONLY=${UGET_UPDATE_ONLY:-false}

# UGET_DIRECTORY is where downloaded binaries will be placed.
UGET_DIRECTORY="${UGET_DIRECTORY:-_tools}"

# UGET_CHECKSUMS is an optional path to a checksum file that µget should use to
# ensure subsequent downloads match previously known checksums to prevent
# tampering on the server side.
UGET_CHECKSUMS="${UGET_CHECKSUMS:-}"

# UGET_VERSIONED_BINARIES can be set to true to append the binary's version to
# its filename, leading to final paths like "_tools/mytool-v1.2.3". This can
# help when µget is being used in Makefiles to improve staleness detection.
# Note that µget never deletes any installed binaries, so enabling this can
# lead to leftover binaries that you can cleanup at your own convenience.
UGET_VERSIONED_BINARIES=${UGET_VERSIONED_BINARIES:-false}

# UGET_TEMPDIR is the root directory to use when creating new temporary dirs.
UGET_TEMPDIR="${UGET_TEMPDIR:-/tmp}"

# UGET_CACHE is an optional path to a directory where binaries are stored/read
# from instead of being downloaded from the internet. This can be useful if you
# have many projects using µget and do not want to re-download the same binary
# for all of them. This does not apply for Go modules since those are already
# effectively cached by Go itself.
UGET_CACHE="${UGET_CACHE:-}"

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

uget_mktemp() {
  set -e
  # --tmpdir does not work on MacOS
  mktemp -d -p "$ABS_UGET_TEMPDIR"
}

uget_log() {
  if [ "$UGET_PRINT_PATH" = "no" ]; then
    echo "$@" >&2
  fi
}

uget_error() {
  echo "$@" >&2
}

uget_lowercase() {
  set -e
  cat | tr '[:upper:]' '[:lower:]'
}

uget_checksum_enabled() {
  set -e
  [ -n "$UGET_CHECKSUMS" ]
}

uget_cache_enabled() {
  set -e
  [ -n "$UGET_CACHE" ]
}

uget_checksum_check() {
  set -e

  local kvString="$1"
  local downloadedBinary="$2"

  if ! uget_checksum_enabled; then return; fi

  local newChecksum
  newChecksum="$(uget_checksum_calculate "$downloadedBinary")"

  local oldChecksum
  oldChecksum="$(uget_checksum_read "$kvString")"

  if [ -n "$oldChecksum" ] && [ "$oldChecksum" != "$newChecksum" ]; then
    uget_error
    uget_error "  *************************************************************************"
    uget_error "  SECURITY ERROR"
    uget_error
    uget_error "  The downloaded file $downloadedBinary does not have the expected checksum."
    uget_error
    uget_error "  Expected: $oldChecksum"
    uget_error "  Actual  : $newChecksum"
    uget_error
    uget_error "  If you are updating $IDENTIFIER, this error is expected."
    uget_error "  Re-run this command with the environment variable UGET_UPDATE=true to make"
    uget_error "  µget update the checksums for all known variants of $IDENTIFIER."
    uget_error "  Use UGET_UPDATE_ONLY=true if you want to just update the checksums and not"
    uget_error "  install the given binary on this machine."
    uget_error "  *************************************************************************"
    uget_error

    return 1
  fi

  if [ -z "$oldChecksum" ]; then
    uget_checksum_write "$kvString" "$newChecksum"
  fi
}

uget_checksum_read() {
  set -e

  local kvString="$1"

  if [ -f "$UGET_CHECKSUMS" ]; then
    awk -F'|' -v "binary=$IDENTIFIER" -v "kv=$kvString" '{ if ($1 == binary && $2 == kv) print $3 }' "$UGET_CHECKSUMS"
  fi
}

uget_checksum_calculate() {
  set -e
  "$UGET_HASHFUNC" "$1" | awk '{ print $1 }'
}

# hash_string includes a trailing newline in the hashed data
# (because of echo), but since *all* hashes contain it, it
# doesn't matter.
uget_hash_string() {
  set -e
  echo "$1" | "$UGET_HASHFUNC" | awk '{ print $1 }'
}

uget_checksum_write() {
  set -e

  local kvString="$1"
  local checksum="$2"

  if [ -f "$UGET_CHECKSUMS" ]; then
    local tempDir
    tempDir="$(uget_mktemp)"

    # use awk to drop any existing hash for this binary/keyvalue combo
    # (for better readability, do not invert the condition here);
    # checking for NF (number of fields) to drop empty lines
    awk \
      -F'|' -v "binary=$IDENTIFIER" -v "kv=$kvString" \
      '{ if (NF == 0 || ($1 == binary && $2 == kv)) {} else print }' \
      "$UGET_CHECKSUMS" > "$tempDir/checksums.txt"

    # add our new checksum
    echo "$IDENTIFIER|$kvString|$checksum" >> "$tempDir/checksums.txt"

    # sort the file because it looks nicer and prevents ugly git diffs
    sort "$tempDir/checksums.txt" > "$UGET_CHECKSUMS"

    rm -rf -- "$tempDir"
  else
    # start a new file
    echo "$IDENTIFIER|$kvString|$checksum" > "$UGET_CHECKSUMS"
  fi
}

uget_url_placeholder() {
  set -e

  case "$1" in
    GOARCH) go env GOARCH ;;
    GOOS)   go env GOOS ;;
    UARCH)  uname -m | uget_lowercase ;;
    UOS)    uname -s | uget_lowercase ;;
    *)      uget_error "Unexpected placeholder $1."; return 1 ;;
  esac
}

# valueFromPair returns "foo" for "myvalue=foo;myothervalue=bar" when called with
# "myvalue" as the key.
uget_url_valueFromPair() {
  set -e

  local kvString="$1"
  local key="$2"

  # adding semicolons makes matching full keys easier
  echo ";$kvString;" | sed -E "s/.*;$key=([^;]+).*/\\1/"
}

uget_url_setKeyInPairs() {
  set -e

  local kvString="$1"
  local key="$2"
  local value="$3"

  # first take the existing pairs and turn it into a multiline string; then
  # awk out the existing pair for $key,
  # then add a new pair and sort it all together, strip empty lines in case
  # kvstring as empty, then join the multiple lines back into a single line
  # and drop the trailing ';'
  (
    echo "$kvString" | tr ';' "\n" | awk -F'=' -v "key=$key" '{ if ($1 != key) print }'
    echo "$key=$value"
  ) | sort | sed '/^[[:space:]]*$/d' | tr "\n" ';' | sed 's/;$//'
}

uget_url_findPlaceholders() {
  set -e

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
uget_url_replaceLive() {
  set -e

  local urlPattern="$1"
  local usedPlaceholders=""

  for placeholder in $(uget_url_findPlaceholders "$urlPattern"); do
    # version is treated specially
    [ "$placeholder" = "VERSION" ] && continue

    local replacement
    replacement="$(uget_url_placeholder "$placeholder")"
    urlPattern="$(echo "$urlPattern" | sed "s|{$placeholder}|$replacement|g")"

    # remember this placeholder and its value
    usedPlaceholders="$usedPlaceholders;$placeholder=$replacement"
  done

  # trim leading ";"
  usedPlaceholders="$(echo "$usedPlaceholders" | sed 's/^;//')"

  echo "$usedPlaceholders|$urlPattern"
}

# replaceWithArgs() does not ask the current system for the values when replacing
# a placeholder, but uses a given key-value pair string as the source. It also
# only returns the resulting string, since the used placeholders are known to
# the caller already.
uget_url_replaceWithArgs() {
  set -e

  local urlPattern="$1"
  local kvString="$2"

  for placeholder in $(uget_url_findPlaceholders "$urlPattern"); do
    # version is treated specially
    [ "$placeholder" = "VERSION" ] && continue

    local replacement
    replacement="$(uget_url_valueFromPair "$kvString" "$placeholder")"
    if [ -z "$replacement" ]; then
      uget_error "Found no replacement string for placeholder $placeholder."
      exit 1
    fi

    urlPattern="$(echo "$urlPattern" | sed "s|{$placeholder}|$replacement|g")"
  done

  echo "$urlPattern"
}

# returns "KVSTRING URL"
uget_url_build() {
  set -e

  local kvString="${1:-}"
  local pattern

  if [ -z "$kvString" ]; then
    local result
    result="$(uget_url_replaceLive "$URL_PATTERN")"
    kvString="$(echo "$result" | cut -d'|' -f1)"
    pattern="$(echo "$result" | cut -d'|' -f2)"
  else
    pattern="$(uget_url_replaceWithArgs "$URL_PATTERN" "$kvString")"
  fi

  pattern="$(echo "$pattern" | sed "s|{VERSION}|$VERSION|g")"

  echo "$kvString|$pattern"
}

# uget_http_download() uses either curl or wget to download the given URL into
# the current directory.
uget_http_download() {
  set -e

  local url="$1"

  if command -v curl >/dev/null 2>&1; then
    curl --fail -LO "$url"
  elif command -v wget >/dev/null 2>&1; then
    wget "$url"
  else
    uget_error "Neither curl nor wget are available."
    return 1
  fi
}

# uget_go_install creates a temporary Go module, adds the given URL as a
# dependency and then builds $BINARY.
uget_go_install() {
  set -e

  local destinationDir="$1"
  local kvString="$2"
  local url="$3"

  # make sure GOARCH and GOOS will be set correctly
  local os
  local arch
  os="$(uget_url_valueFromPair "$kvString" "GOOS")"
  arch="$(uget_url_valueFromPair "$kvString" "GOARCH")"

  # since we crosscompile, we cannot do "GOBIN=(somewhere) go install url@version",
  # because Go considers this to be dangerous behaviour:
  # https://github.com/golang/go/issues/57485
  # Instead we create a dummy module and use the desired program as a dependency,
  # *then* we're allowed to crosscompile it anywhere using "go build". Go figure.

  go mod init temp 2>/dev/null
  go get "$url@$VERSION"

  local tmpFilename="__tmp.bin"

  # go build command is meant to be expanded
  # shellcheck disable=SC2086
  GOFLAGS=-trimpath GOARCH="$arch" GOOS="$os" $UGET_GO_BUILD_CMD -o "$tmpFilename" "$url"

  mv "$tmpFilename" "$destinationDir/$BINARY"
}

# uget_extract_archive extracts a downloaded archive and moves the one interesting
# file out of it.
uget_extract_archive() {
  set -e

  local destinationDir="$1"
  local archive="$2"

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
        uget_error "Unknown file type: $archive"
        return 1
    esac
  fi

  # pattern is explicitly meant to be interpreted by the shell
  # shellcheck disable=SC2086
  mv $BINARY_PATTERN "$destinationDir/$BINARY"
  chmod +x "$destinationDir/$BINARY"
}

# uget_download performs the actual download or reads a binary
# from the configured cache (if any), and finally places it
# in a given directory.
uget_download() {
  set -e

  local destinationDir="$1"
  local kvString="$2"
  local url="$3"

  local startDir
  startDir="$(pwd)"

  local tempDir
  tempDir="$(uget_mktemp)"

  cd "$tempDir"

  if $GO_MODULE; then
    uget_go_install "$destinationDir" "$kvString" "$url"
  else
    local urlHash
    urlHash="$(uget_hash_string "$url")"

    cacheFile="$UGET_CACHE/$IDENTIFIER-$VERSION-$urlHash"

    if uget_cache_enabled && [ -f "$cacheFile" ]; then
      cp "$cacheFile" "$destinationDir/$BINARY"
      uget_log "Copied $BINARY from µget cache."
    else
      uget_http_download "$url"

      local archive
      archive="$(ls)"

      uget_extract_archive "$destinationDir" "$archive"

      if uget_cache_enabled; then
        cp "$destinationDir/$BINARY" "$cacheFile"
      fi
    fi
  fi

  cd "$startDir"
  rm -rf -- "$tempDir"
}

# ready() checks if the desired binary already exists in the desired version.
uget_ready() {
  local fullFinalPath="$ABS_UGET_DIRECTORY/$BINARY"
  if ! [ -f "$fullFinalPath" ]; then
    return 1
  fi

  # skip further checks if we're using versioned binaries
  if $UGET_VERSIONED_BINARIES; then
    return 0
  fi

  local versionFile="$fullFinalPath.version"
  [ -f "$versionFile" ] && [ "$VERSION" = "$(cat "$versionFile")" ]
}

# install() downloads the binary, checks the checksum and places it in UGET_DIRECTORY.
uget_install() {
  set -e

  local kvString="$1"
  local url="$2"
  local fullFinalPath="$ABS_UGET_DIRECTORY/$BINARY"
  local versionFile="$fullFinalPath.version"

  local startDir
  startDir="$(pwd)"

  local tempDir
  tempDir="$(uget_mktemp)"

  uget_log "Downloading $IDENTIFIER version $VERSION ..."
  uget_download "$tempDir" "$kvString" "$url"

  local fullTempBinary="$tempDir/$BINARY"
  uget_checksum_check "$kvString" "$fullTempBinary"

  # if everything is fine, place the binary in its final location
  mv "$fullTempBinary" "$fullFinalPath"

  if ! $UGET_VERSIONED_BINARIES; then
    echo "$VERSION" > "$versionFile"
  fi

  uget_log "Installed at $UGET_DIRECTORY/$BINARY."

  cd "$startDir"
  rm -rf -- "$tempDir"
}

# update() downloads the binary, updates the checksums and discards the binary.
uget_update() {
  set -e

  local kvString="$1"
  local url="$2"

  local startDir
  startDir="$(pwd)"

  local tempDir
  tempDir="$(uget_mktemp)"

  uget_log "  ~> $kvString"
  uget_download "$tempDir" "$kvString" "$url"

  local fullBinaryPath="$tempDir/$BINARY"

  local checksum
  checksum="$(uget_checksum_calculate "$fullBinaryPath")"

  uget_checksum_write "$kvString" "$checksum"

  cd "$startDir"
  rm -rf -- "$tempDir"
}

###############################################################################
# General Setup Logic

# get CLI flags
export URL_PATTERN="$1"
export IDENTIFIER="$2"
export VERSION="$3"
BINARY_PATTERN="${4:-**/$IDENTIFIER}"

export BINARY="$IDENTIFIER"
if $UGET_VERSIONED_BINARIES; then
  BINARY="$IDENTIFIER-$VERSION"
fi

# ensure target directory exists
mkdir -p "$UGET_DIRECTORY" "$UGET_TEMPDIR"

ABS_UGET_DIRECTORY="$(realpath "$UGET_DIRECTORY")"
ABS_UGET_TEMPDIR="$(realpath "$UGET_TEMPDIR")"

if uget_cache_enabled; then
  mkdir -p "$UGET_CACHE"
  UGET_CACHE="$(realpath "$UGET_CACHE")"
fi

if $GO_MODULE && ! $UGET_GO_CHECKSUMS; then
  UGET_CHECKSUMS=""
fi

if uget_checksum_enabled; then
  touch "$UGET_CHECKSUMS"
  UGET_CHECKSUMS="$(realpath "$UGET_CHECKSUMS")"
else
  if $GO_MODULE; then
    if $UGET_UPDATE || $UGET_UPDATE_ONLY; then
      uget_error "Checksums are disabled for Go modules, cannot update $IDENTIFIER checksums."
      # This is not an error because in complex Makefiles, there might be 3 binaries required
      # for one make target, and if one of them is a Go module and you have no direct way
      # to just update a single binary, then running "UGET_UPDATE make complex-task" would
      # fail at the Go module. It's simply more convenient to just warn and move on to the next
      # binary.
    fi
  fi

  UGET_UPDATE=false
  UGET_UPDATE_ONLY=false
fi

###############################################################################
# Main application logic

# When in update-only mode, we do not download the binary for the current system
# only, but instead for all known variants based on the checksums file,
# recalculate the checksums and then discard the temporary binaries. Otherwise
# after updating the checksums we continue with the regular install logic.

if $UGET_UPDATE || $UGET_UPDATE_ONLY; then
  uget_log "Updating checksums for $IDENTIFIER ..."

  # Find and process all known variants...
  awk -F'|' -v "binary=$IDENTIFIER" '{ if ($1 == binary) print $2 }' "$UGET_CHECKSUMS" | while IFS= read -r kvString; do
    result="$(uget_url_build "$kvString")"
    url="$(echo "$result" | cut -d'|' -f2)"

    # download binary into tempdir, update checksums, but then delete it again
    uget_update "$kvString" "$url"
  done

  uget_log "All checksums were updated."
fi

if ! $UGET_UPDATE_ONLY; then
  if ! uget_ready; then
    # Replace placeholders in the URL with system-specific data, like arch or OS
    result="$(uget_url_build)"
    kvString="$(echo "$result" | cut -d'|' -f1)"
    url="$(echo "$result" | cut -d'|' -f2)"

    # Go modules usually do not have arch/os infos in their module names, but
    # the resulting binary still depends on the local system. To support checksums
    # for different machines, we always assume GOARCH and GOOS are "used placeholders".
    if $GO_MODULE; then
      kvString="$(uget_url_setKeyInPairs "$kvString" "GOARCH" "$(uget_url_placeholder GOARCH)")"
      kvString="$(uget_url_setKeyInPairs "$kvString" "GOOS" "$(uget_url_placeholder GOOS)")"
    fi

    uget_install "$kvString" "$url"
  fi

  case "$UGET_PRINT_PATH" in
    absolute) echo "$ABS_UGET_DIRECTORY/$BINARY" ;;
    relative) echo "$UGET_DIRECTORY/$BINARY" ;;
  esac
fi
