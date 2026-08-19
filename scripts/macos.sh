#!/bin/sh
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
MACOS="$ROOT/macos"
BUILD="$ROOT/.build"
DIST="$ROOT/dist"
FINAL_APP="$DIST/AiUsage.app"
APP="$FINAL_APP"
ACTION=${1:-build}
ADHOC_SIGN=${ADHOC_SIGN:-1}
BUNDLE_ID=com.plopezlpz.aiusage
cd "$ROOT"

fail() {
    echo "error: $*" >&2
    exit 1
}

if [ -e "$DIST" ] || [ -L "$DIST" ]; then
    if [ -L "$DIST" ] || [ ! -d "$DIST" ]; then fail "dist path is not a real directory: $DIST"; fi
else
    mkdir "$DIST"
fi

safe_remove_generated() {
    case "$1" in
        "$INSTALL_DIR"/.AiUsage.install.*|"$INSTALL_DIR"/.AiUsage.backup.*) rm -rf "$1" ;;
        *) fail "refusing to remove unexpected path: $1" ;;
    esac
}

safe_remove_dist_generated() {
    case "$1" in
        "$DIST"/.AiUsage.build.*|"$DIST"/.AiUsage.previous.*) rm -rf "$1" ;;
        *) fail "refusing to remove unexpected build path: $1" ;;
    esac
}

copy_go_licenses() {
    GO_LICENSE_ROOT="$APP/Contents/Resources/Licenses/Go"
    GO_MODULES="$BUILD/go-modules.txt"
    GO_LICENSE_FILES="$BUILD/go-license-files.txt"
    GO_MANIFEST="$APP/Contents/Resources/Licenses/Go-MODULES.txt"
    mkdir -p "$GO_LICENSE_ROOT"
    : > "$GO_MANIFEST"
    GOOS=darwin GOARCH=arm64 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}}|{{.Dir}}{{end}}{{end}}' . | LC_ALL=C sort -u | sed '/^$/d' > "$GO_MODULES"
    [ -s "$GO_MODULES" ] || fail "go list returned no external linked modules"

    while IFS='|' read -r MODULE_ID MODULE_DIR; do
        if [ -z "$MODULE_ID" ] || [ ! -d "$MODULE_DIR" ]; then fail "invalid linked Go module metadata: $MODULE_ID"; fi
        MODULE_PATH=${MODULE_ID%% *}
        MODULE_LICENSE_DIR="$GO_LICENSE_ROOT/$MODULE_PATH"
        mkdir -p "$MODULE_LICENSE_DIR"
        : > "$GO_LICENSE_FILES"
        for LICENSE_FILE in \
            "$MODULE_DIR"/[Ll][Ii][Cc][Ee][Nn][Ss][Ee]* \
            "$MODULE_DIR"/[Cc][Oo][Pp][Yy][Ii][Nn][Gg]* \
            "$MODULE_DIR"/[Nn][Oo][Tt][Ii][Cc][Ee]*; do
            [ -f "$LICENSE_FILE" ] || continue
            printf '%s\n' "$LICENSE_FILE" >> "$GO_LICENSE_FILES"
        done
        [ -s "$GO_LICENSE_FILES" ] || fail "linked Go module lacks a root license or notice: $MODULE_ID"
        while IFS= read -r LICENSE_FILE; do
            ditto "$LICENSE_FILE" "$MODULE_LICENSE_DIR/$(basename "$LICENSE_FILE")"
        done < "$GO_LICENSE_FILES"
        printf '%s\n' "$MODULE_ID" >> "$GO_MANIFEST"
    done < "$GO_MODULES"
}

validate_licenses() {
    LICENSE_ROOT="$1/Contents/Resources/Licenses"
    [ -f "$LICENSE_ROOT/aiusage-LICENSE.txt" ] || fail "packaged project license is missing"
    [ -f "$LICENSE_ROOT/lobe-icons-LICENSE.txt" ] || fail "packaged provider icon license is missing"
    [ -s "$LICENSE_ROOT/Go-MODULES.txt" ] || fail "packaged Go module license manifest is missing"
    cmp -s "$ROOT/LICENSE" "$LICENSE_ROOT/aiusage-LICENSE.txt" || fail "packaged project license differs from source"
    cmp -s "$MACOS/Resources/Licenses/lobe-icons-LICENSE.txt" "$LICENSE_ROOT/lobe-icons-LICENSE.txt" || fail "packaged provider icon license differs from source"

    while IFS= read -r MODULE_ID; do
        MODULE_PATH=${MODULE_ID%% *}
        MODULE_LICENSE_DIR="$LICENSE_ROOT/Go/$MODULE_PATH"
        FOUND=0
        for LICENSE_FILE in "$MODULE_LICENSE_DIR"/*; do
            if [ -f "$LICENSE_FILE" ]; then FOUND=1; break; fi
        done
        [ "$FOUND" = 1 ] || fail "packaged license is missing for linked Go module: $MODULE_ID"
    done < "$LICENSE_ROOT/Go-MODULES.txt"
}

validate_app_bundle() {
    BUNDLE="$1"
    if [ -L "$BUNDLE" ] || [ ! -d "$BUNDLE" ]; then fail "app bundle is missing or unsafe: $BUNDLE"; fi
    plutil -lint "$BUNDLE/Contents/Info.plist" >/dev/null || fail "packaged Info.plist is malformed"
    [ "$(plutil -extract CFBundleIdentifier raw "$BUNDLE/Contents/Info.plist")" = "$BUNDLE_ID" ] || fail "CFBundleIdentifier is invalid"
    [ "$(plutil -extract CFBundleExecutable raw "$BUNDLE/Contents/Info.plist")" = AiUsage ] || fail "CFBundleExecutable is invalid"
    [ "$(plutil -extract CFBundleIconFile raw "$BUNDLE/Contents/Info.plist")" = AppIcon ] || fail "CFBundleIconFile is invalid"
    [ "$(plutil -extract LSUIElement raw "$BUNDLE/Contents/Info.plist")" = true ] || fail "LSUIElement must be true"
    [ -x "$BUNDLE/Contents/MacOS/AiUsage" ] || fail "packaged app host is missing"
    [ -s "$BUNDLE/Contents/Resources/AppIcon.icns" ] || fail "packaged app icon is missing"
    [ -x "$BUNDLE/Contents/MacOS/aiusage-cli" ] || fail "packaged aiusage is missing"
    for PROVIDER_ICON in claude openai kimi; do
        [ -s "$BUNDLE/Contents/Resources/ProviderIcons/$PROVIDER_ICON.svg" ] || fail "packaged $PROVIDER_ICON icon is missing"
    done
    [ "$(lipo -archs "$BUNDLE/Contents/MacOS/AiUsage")" = arm64 ] || fail "packaged app host is not arm64-only"
    [ "$(lipo -archs "$BUNDLE/Contents/MacOS/aiusage-cli")" = arm64 ] || fail "packaged aiusage is not arm64-only"
    if find "$BUNDLE/Contents" -iname '*SwiftTerm*' | grep -q .; then
        fail "obsolete SwiftTerm resources must not be packaged"
    fi
    validate_licenses "$BUNDLE"
}

[ "$(uname -s)" = Darwin ] || fail "the macOS app can only be built on macOS"
[ "$(uname -m)" = arm64 ] || fail "the macOS app supports Apple Silicon (arm64) only"
case "$ACTION" in
    build|install) ;;
    *) fail "usage: $0 [build|install]" ;;
esac
case "$ADHOC_SIGN" in
    0|1) ;;
    *) fail "ADHOC_SIGN must be 0 or 1" ;;
esac
for command in go swift plutil lipo file ditto cmp sed grep basename; do
    command -v "$command" >/dev/null 2>&1 || fail "required command not found: $command"
done
if [ "$ACTION" = install ]; then
    command -v pgrep >/dev/null 2>&1 || fail "required command not found: pgrep"
    INSTALL_DIR="$HOME/Applications"
    INSTALL_APP="$INSTALL_DIR/AiUsage.app"
    refuse_if_running() {
        if pgrep -x AiUsage >/dev/null 2>&1; then
            fail "AI Usage is running; quit it from the menu-bar icon, then retry installation"
        fi
    }
    refuse_if_running
fi
[ -f "$MACOS/Package.swift" ] || fail "missing macos/Package.swift"
[ -f "$MACOS/Info.plist" ] || fail "missing macos/Info.plist"
[ -s "$MACOS/Resources/AppIcon.icns" ] || fail "missing app icon"
[ -f "$ROOT/LICENSE" ] || fail "missing project LICENSE"
[ -f "$MACOS/Resources/Licenses/lobe-icons-LICENSE.txt" ] || fail "missing Lobe Icons license"
for PROVIDER_ICON in claude openai kimi; do
    [ -s "$MACOS/Resources/ProviderIcons/$PROVIDER_ICON.svg" ] || fail "missing $PROVIDER_ICON provider icon"
done
plutil -lint "$MACOS/Info.plist" >/dev/null || fail "macos/Info.plist is malformed"
[ "$(plutil -extract CFBundleIdentifier raw "$MACOS/Info.plist")" = "$BUNDLE_ID" ] || fail "macos/Info.plist has an unexpected bundle identifier"

mkdir -p "$BUILD/go"
GOOS=darwin GOARCH=arm64 go build -trimpath -o "$BUILD/go/aiusage-cli" .
swift build --package-path "$MACOS" --configuration release --arch arm64 --product AiUsage
SWIFT_BIN=$(swift build --package-path "$MACOS" --configuration release --arch arm64 --show-bin-path)
HOST="$SWIFT_BIN/AiUsage"

[ -x "$BUILD/go/aiusage-cli" ] || fail "Go build did not produce an executable aiusage-cli"
[ -x "$HOST" ] || fail "Swift build did not produce an executable AiUsage"
[ "$(lipo -archs "$BUILD/go/aiusage-cli")" = arm64 ] || fail "bundled aiusage is not arm64-only"
[ "$(lipo -archs "$HOST")" = arm64 ] || fail "app host is not arm64-only"

if [ -L "$DIST" ] || [ ! -d "$DIST" ]; then fail "dist path is not a real directory: $DIST"; fi
[ ! -L "$FINAL_APP" ] || fail "refusing to replace symlinked app bundle: $FINAL_APP"
STAGED_APP=$(mktemp -d "$DIST/.AiUsage.build.XXXXXX")
DIST_BACKUP=""
APP="$STAGED_APP"
dist_cleanup() {
    STATUS=$?
    trap - EXIT HUP INT TERM
    if [ -n "$DIST_BACKUP" ] && [ -e "$DIST_BACKUP" ]; then
        if [ ! -e "$FINAL_APP" ] && [ ! -L "$FINAL_APP" ]; then
            mv "$DIST_BACKUP" "$FINAL_APP" || echo "error: failed to restore $FINAL_APP from $DIST_BACKUP" >&2
            DIST_BACKUP=""
        else
            safe_remove_dist_generated "$DIST_BACKUP"
            DIST_BACKUP=""
        fi
    fi
    if [ -n "$STAGED_APP" ] && [ -e "$STAGED_APP" ]; then safe_remove_dist_generated "$STAGED_APP"; fi
    exit "$STATUS"
}
trap 'dist_cleanup' EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources/Licenses" "$APP/Contents/Resources/ProviderIcons"
ditto "$MACOS/Info.plist" "$APP/Contents/Info.plist"
ditto "$MACOS/Resources/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
ditto "$HOST" "$APP/Contents/MacOS/AiUsage"
ditto "$BUILD/go/aiusage-cli" "$APP/Contents/MacOS/aiusage-cli"
ditto "$ROOT/LICENSE" "$APP/Contents/Resources/Licenses/aiusage-LICENSE.txt"
ditto "$MACOS/Resources/Licenses/lobe-icons-LICENSE.txt" "$APP/Contents/Resources/Licenses/lobe-icons-LICENSE.txt"
ditto "$MACOS/Resources/ProviderIcons" "$APP/Contents/Resources/ProviderIcons"
copy_go_licenses
chmod 755 "$APP/Contents/MacOS/AiUsage" "$APP/Contents/MacOS/aiusage-cli"
validate_app_bundle "$APP"

if [ "$ADHOC_SIGN" = 1 ]; then
    command -v codesign >/dev/null 2>&1 || fail "codesign is required unless ADHOC_SIGN=0"
    codesign --force --sign - "$APP/Contents/MacOS/aiusage-cli" >/dev/null
    codesign --force --sign - "$APP" >/dev/null
    codesign --verify --deep --strict "$APP"
fi
validate_app_bundle "$APP"

if [ -e "$FINAL_APP" ]; then
    DIST_BACKUP=$(mktemp -d "$DIST/.AiUsage.previous.XXXXXX")
    rmdir "$DIST_BACKUP"
    mv "$FINAL_APP" "$DIST_BACKUP"
fi
if ! mv "$STAGED_APP" "$FINAL_APP"; then
    if [ -n "$DIST_BACKUP" ] && [ -e "$DIST_BACKUP" ]; then
        mv "$DIST_BACKUP" "$FINAL_APP"
        DIST_BACKUP=""
    fi
    fail "failed to replace $FINAL_APP"
fi
STAGED_APP=""
APP="$FINAL_APP"
if [ -n "$DIST_BACKUP" ]; then
    safe_remove_dist_generated "$DIST_BACKUP"
    DIST_BACKUP=""
fi
trap - EXIT HUP INT TERM

if [ "$ACTION" = install ]; then
    [ ! -L "$INSTALL_DIR" ] || fail "refusing to install through symlinked directory: $INSTALL_DIR"
    if [ -e "$INSTALL_DIR" ]; then
        [ -d "$INSTALL_DIR" ] || fail "install location is not a directory: $INSTALL_DIR"
    else
        mkdir -p "$INSTALL_DIR"
    fi
    if [ -e "$INSTALL_APP" ] || [ -L "$INSTALL_APP" ]; then
        if [ -L "$INSTALL_APP" ] || [ ! -d "$INSTALL_APP" ]; then fail "refusing to replace unsafe path: $INSTALL_APP"; fi
        [ -f "$INSTALL_APP/Contents/Info.plist" ] || fail "refusing to replace app without Info.plist: $INSTALL_APP"
        EXISTING_ID=$(plutil -extract CFBundleIdentifier raw "$INSTALL_APP/Contents/Info.plist" 2>/dev/null) || fail "refusing to replace app with unreadable bundle identifier: $INSTALL_APP"
        [ "$EXISTING_ID" = "$BUNDLE_ID" ] || fail "refusing to replace app with bundle identifier $EXISTING_ID"
    fi

    TEMP_APP=$(mktemp -d "$INSTALL_DIR/.AiUsage.install.XXXXXX")
    BACKUP_APP=""
    INSTALL_IN_PROGRESS=0
    remove_installed_candidate() {
        [ ! -L "$INSTALL_APP" ] && [ -d "$INSTALL_APP" ] && [ -f "$INSTALL_APP/Contents/Info.plist" ] || return 1
        CANDIDATE_ID=$(plutil -extract CFBundleIdentifier raw "$INSTALL_APP/Contents/Info.plist" 2>/dev/null) || return 1
        [ "$CANDIDATE_ID" = "$BUNDLE_ID" ] || return 1
        rm -rf "$INSTALL_APP"
    }
    install_cleanup() {
        STATUS=$?
        trap - EXIT HUP INT TERM
        if [ "$INSTALL_IN_PROGRESS" = 1 ] && [ -n "$BACKUP_APP" ] && [ -e "$BACKUP_APP" ]; then
            if [ -e "$INSTALL_APP" ] || [ -L "$INSTALL_APP" ]; then
                remove_installed_candidate || echo "error: refusing to remove an unverified failed install at $INSTALL_APP" >&2
            fi
            if [ ! -e "$INSTALL_APP" ] && [ ! -L "$INSTALL_APP" ]; then
                mv "$BACKUP_APP" "$INSTALL_APP" || echo "error: failed to restore $INSTALL_APP from $BACKUP_APP" >&2
                BACKUP_APP=""
            fi
        fi
        if [ -n "$TEMP_APP" ] && [ -e "$TEMP_APP" ]; then safe_remove_generated "$TEMP_APP"; fi
        exit "$STATUS"
    }
    trap 'install_cleanup' EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM

    ditto "$APP" "$TEMP_APP"
    validate_app_bundle "$TEMP_APP"
    if [ "$ADHOC_SIGN" = 1 ]; then
        codesign --verify --deep --strict "$TEMP_APP" || fail "staged app signature verification failed"
    fi
    if [ -e "$INSTALL_APP" ]; then
        BACKUP_APP=$(mktemp -d "$INSTALL_DIR/.AiUsage.backup.XXXXXX")
        rmdir "$BACKUP_APP"
        INSTALL_IN_PROGRESS=1
        refuse_if_running
        mv "$INSTALL_APP" "$BACKUP_APP"
    fi
    mv "$TEMP_APP" "$INSTALL_APP"
    TEMP_APP=""
    INSTALL_IN_PROGRESS=0
    if [ -n "$BACKUP_APP" ]; then
        safe_remove_generated "$BACKUP_APP"
        BACKUP_APP=""
    fi
    trap - EXIT HUP INT TERM
    echo "Installed $INSTALL_APP"
    echo "The terminal CLI was not installed; from this checkout run: go install ."
    echo "Ensure Go's bin directory is on PATH to use the aiusage command."
else
    echo "Built $APP"
fi

file "$APP/Contents/MacOS/AiUsage" "$APP/Contents/MacOS/aiusage-cli"
