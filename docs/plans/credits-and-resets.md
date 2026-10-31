# Credits and banked resets

## Goal / accepted UI
Provider cards (option A) in macOS and terminal. Benefit summary shows total available and nearest expiration; expand multiple grants ordered soonest first, unknown last. Warn in existing low-quota orange at <=48h. Keep recurring quota resets separate from benefit expiration. No redeem/purchase actions. Preserve unrelated Kimi changes.

## Acceptance
- Current Claude Max/Ultra labels including legacy tier mapping.
- Codex credits/reset grants from installed app-server schema; never infer missing counts/expiry as zero.
- Claude benefits only from verified supported response fields, otherwise explicit unavailable state; no guessed billing endpoints or credential extraction from browsers.
- Summary, partial details, expiration ordering, warning boundaries, expired grants, expansion persistence and small terminal layouts tested.
- Go tests/race/vet; Swift tests; fresh-context read-only review; inspect diff.

## Evidence / progress
- Baseline `go test ./...` passed.
- Public Claude docs confirm Max 20x -> Ultra; Max 5x -> Max. Verified Ultra has no five-hour window.
- Installed codex 0.166.0 generated schemas in /tmp/aiusage-codex-schema: GetAccountRateLimitsResponse.rateLimitResetCredits contains availableCount and optional/capped credits. Each credit includes id/status/expiresAt/title; expiresAt:null explicitly means no expiration. CreditsSnapshot has balance string and unlimited flag, no expiration.
- Implemented additive cache/JSON benefits model, shared expiration/summary rules, expandable terminal and macOS rows, exact dates/timezones, retained disclosure state, short-terminal scrolling, and demonstration fixtures. Existing unrelated Kimi changes preserved.
- Claude Code 2.1.314 embedded code confirms read-only `GET /api/oauth/usage?cedar_ember=1&skip_spend=1` and `cedar_ember` inventory schema (eligible, grants with resets_left, paused, starts_at, ends_at). Added separate benefit success/failure cache state; status-line ingestion preserves it. No reset/purchase mutation requests.
- Console API promo balance source remains unverified/unconnected. UI explicitly marks it Not reported and links to billing; fixture support does not claim live promo collection.
- Live `go run . collect-claude` and `go run . collect-codex` succeeded. Cached snapshot check verified Codex credits and a banked reset with expiry; Claude inventory was not reported. No credentials or raw account responses retained in task files.
- Independent read-only review (run 66083f2b-d066-4a4d-8f64-e1efe7751ab9) found malformed Codex benefits could erase good cached balances. Fixed parser to distinguish absent/null from malformed fields and propagate sanitized collection failures, preserving last good cache/timestamps. Regression test covers malformed balances and counts alongside valid quotas. Targeted re-review (5af1c120-a44e-4ab3-8248-ad0169c8f54f) confirmed fix, no remaining findings.

## Final validation
- `go test ./...`: passed, including malformed-benefit preservation, additive snapshot contract, independent Claude failure cache, expiration boundaries, partial lists, disclosure persistence and small-terminal scrolling.
- `go test -race ./...`: passed.
- `go vet ./...`: passed.
- `GOOS=linux GOARCH=amd64 go build -o /tmp/aiusage-benefits-linux .`: passed.
- `GOOS=windows GOARCH=amd64 go build -o /tmp/aiusage-benefits-windows.exe .`: passed.
- `swift test --package-path macos`: 19 tests passed on final source.
- `AIUSAGE_BENEFIT_PREVIEW=/tmp/aiusage-benefits-preview.png swift test --package-path macos`: 19 tests passed. Inspected offscreen collapsed/expanded macOS preview; warning color, alignment and exact dates visible. This was not a live-app click-through test.
- `./scripts/macos.sh build && codesign --verify --deep --strict dist/AiUsage.app`: passed on final source. Built app remains in dist; did not install/launch it.
- `AIUSAGE_TERMINAL_PREVIEW=1 go test -run TestDemoBenefitLayout -v`: passed; inspected final terminal layout. Corrected date indentation and tested scrolling to the end at widths down to one column. Initial scroll-test iteration ceiling was insufficient for one-column wrapping; final test waits for bounded offset stabilization with a generous safety ceiling.
- `gofmt -d benefits*.go claude*.go codex*.go main.go main_test.go`: no differences.
- `git diff --check`: passed; final tracked diff inspected alongside new feature files. No staging, commits, installation, reset redemption, credit claims or purchases.

## User-requested installation
- Rebuilt and installed with `./scripts/macos.sh install` into `~/Applications/AiUsage.app`; gracefully quit the old process first.
- `codesign --verify --deep --strict ~/Applications/AiUsage.app` passed; installed host/helper match dist via `cmp`.
- Updated existing terminal command using `GOBIN="$HOME/.local/bin" go install .`; cached JSON responded successfully.
- Initial caches had no benefit rows (from the prior installed collector); refreshed Claude and Codex using the installed CLI. Both collections succeeded and the snapshot now contains four benefit rows.
- Relaunched installed app. No preferences or usage caches deleted.

## Follow-up: actual Claude Platform promotional credits
- User screenshots confirm purchased balance $0 and promotional balance $200, expiring 2026-12-01 (billing page dates UTC). Installed UI correctly identifies source as unconnected, but this does not fulfill the original promotional-credit collection request.
- Public source verified: https://raw.githubusercontent.com/steipete/CodexBar/main/docs/claudeplatform.md documents Console-session authentication and read-only GET `/api/organizations`, then `/api/organizations/{uuid}/prepaid/credits` for an API-capable organization. Promotional grants are `promo_tranches` (separate from purchased balance/tranches); balance minor units use exponent metadata. Exact grant schema still needs source inspection before implementation.
- Existing Claude Code OAuth is not the Console billing session. No browser credential stores accessed, no cookies requested in chat, no balance hard-coded from screenshots.
- Next decision: separate user-authorized Claude Platform sign-in/connection. Recommend explicit in-app Connect billing rather than silently importing browser cookies. Once selected, implement authenticated billing collection, verify actual promotional grants/expiry, review and reinstall.

## Antigravity extension (completed and installed)
- User requested Google Antigravity usage in both dashboards. Claude billing browser-session import remains unapproved/pending; do not read browser credentials.
- Found installed `~/.local/bin/agy` 1.4.12; no Antigravity app in standard Applications paths. Prefer supported non-interactive `/usage` JSON command over private Google OAuth/token extraction or desktop process scraping.
- Acceptance: measured per-family/session/weekly quota rows with provider-supplied resets; no assumed 100% for missing data; strict bounded process/output, disable automatic browser login/update and startup tools if supported; private cache with last-good preservation; refresh/cancellation support in CLI and macOS; fixture/process tests, live read-only verification where safe, independent review, build/install after validation if authorized.
- Implemented `collect-antigravity`, fifth-provider dashboard scheduling/refresh/reset handling, private cache preserving last success, dynamic measured group/bucket parser, and two-line macOS / wider terminal quota labels. No new dependency or credential store access.
- Read-only scout verified `/usage` JSON shape against upstream fixtures/static agy tags and official settings schema. Global MCP/plugins/hooks cannot be proven disabled; documented inherited-configuration and detached-descendant limitations instead of inventing disabling flags. Existing CLI login is used; updates disabled, BROWSER/PATH launch guards, private cwd/log file, strict permissions settings, min-version gate, bounded output/time and own process group.
- `go run . collect-antigravity` succeeded against installed1.4.12; observed five measured rows (Gemini weekly/5-hour, Claude+GPT weekly/5-hour, Gemini4Argon weekly), all with reset times. No identity or raw report retained in plan.
- Final `go test ./...`, `go test -race ./...`, and `go vet ./...` passed. Tests cover unknown/malformed quotas, last-good preservation, cancellation without writes, older-attempt preservation, reset scheduling, snapshot/terminal layout, minimum-version gate, launcher guards, output limits, sanitized failures and owned-child cleanup. Fixed test-only /var vs /private/var equivalence and synchronized cancellation on child readiness.
- `GOOS=linux GOARCH=amd64 go build -o /tmp/aiusage-antigravity-linux .` and `GOOS=windows GOARCH=amd64 go build -o /tmp/aiusage-antigravity-windows.exe .` passed; Windows is cache-only for this collector.
- `swift test --package-path macos`: 20 tests passed, including Antigravity snapshot/label coverage. `./scripts/macos.sh build` and `codesign --verify --deep --strict dist/AiUsage.app` passed. `git diff --check` passed; gofmt clean; final integration diff inspected with unrelated changes preserved.
- Independent read-only review 00e2b8ed-08ce-4585-b0b8-0fcfc5e18758 completed with no actionable findings. Source inspection only; tests and live collection were parent-run.
- Updated local installations with `./scripts/macos.sh install` and `GOBIN="$HOME/.local/bin" go install .`, gracefully quitting the old app first. Installed signature verified; host/helper match dist via cmp. Installed CLI `collect-antigravity` succeeded and cached `dashboard-json` contains five Antigravity rows with no collection failures. Relaunched `~/Applications/AiUsage.app` and confirmed its process running. No preferences/caches deleted; no commit or push.
- Antigravity task complete. Claude billing connection remains a separate unresolved authorization/design decision; no browser credentials accessed.

## Content-fitting window follow-up
- Replaced row-height estimates and fixed 330-point detail viewport with intrinsic content measurement, capped at available screen height. Expanded grants, wrapped rows, banners, and details now resize the panel; overflow remains scrollable.
- Added `ContentSizingTests.swift` exercising growth, shrinkage, overflow cap and changing available height against a real NSHostingView.
- `swift test --package-path macos`: 21 passed; `go test ./...` and `git diff --check` passed. Final diff inspected. `./scripts/macos.sh build` / `install` and installed `codesign --verify --deep --strict` passed; relaunched app.
- User reiterated Claude billing is missing. Explicitly requested permission to implement browser-session import with macOS Keychain storage and read-only billing requests, explaining that the session itself grants broader access. Still awaiting authorization; no credential access performed.

## Header-only sizing regression corrected
- User screenshot demonstrated the initial always-scroll measured-height implementation collapsed the full hosted panel to its 65-point header. Component-only NSHostingView tests had missed this.
- Added full DashboardView + NSHostingController + constrained NSVisualEffectView + NSPanel integration test with fake helper snapshots. Confirmed old implementation fails at 65pt. New layout starts with natural intrinsic content, adding a capped scroll view only above available screen height.
- Expanded full-panel coverage 2 -> 8 -> 100 -> 2 rows also exposed nested preference measurement not updating under the real hosting hierarchy. Replaced it with direct GeometryReader onAppear/onChange callbacks; all expansion, cap and shrink assertions now pass. No estimated row heights or fixed detail viewport.
- `AIUSAGE_PANEL_PREVIEW=/tmp/aiusage-panel-sizing.png swift test --package-path macos`: 22 passed. Inspected generated full-panel fixture image. `go test ./...`, `git diff --check`, signed build/install passed. Installed binary matches dist; app relaunched.
- Read-only reviewer 47a38a9d-ce2b-4605-8dfe-3836a97852dc and targeted geometry-callback follow-up 02999f23-f894-4dbb-a05f-ae5ce9afd3af reported no actionable findings (static inspection only).

## Antigravity branding follow-up
- Replaced generic arrow with Antigravity's colored logo from the same pinned MIT-licensed Lobe Icons source as existing icons; retained accurate product name (the allowance includes non-Gemini models). Added attribution and build/install asset checks.
- `AIUSAGE_ICON_PREVIEW=/tmp/aiusage-antigravity-icon.png swift test --package-path macos`: 23 passed, including logo decode/render coverage. Inspected rendered asset. `git diff --check`, `sh -n scripts/macos.sh`, build/install/signature checks passed; installed SVG matches source; relaunched app.

## Per-item visibility (completed and installed)
- Accepted UI: macOS quota/benefit context menus hide individual rows, persisted across app restarts. Provider-header eye/count temporarily reveals hidden rows in original positions, muted with an explicit hidden marker and permanent Unhide control. Headers remain when every row is hidden; sizing follows visible content. Collection and JSON are unaffected.
- Implemented RowVisibility local preference model (namespaced stable IDs, UserDefaults sorted string array), VisibilityRow context menu + accessible hide/unhide action, muted rounded revealed cards with hidden annotation and Unhide button, eye/count capsule in provider headers, all-hidden hint and keyboard focus transfer. Saved choices are not pruned when data temporarily disappears. No collector/JSON/terminal changes.
- `AIUSAGE_HIDDEN_PREVIEW=/tmp/aiusage-hidden-rows.png AIUSAGE_HIDDEN_BENEFIT_PREVIEW=/tmp/aiusage-hidden-benefit.png swift test --package-path macos`: 26 passed. Tests cover separate quota/benefit IDs, persistence without persisting reveal, provider isolation, missing-row retention, last-unhide cleanup, malformed preferences, hidden benefit layout, and actual full-panel hide/reveal/all-hidden/refresh/unhide resizing with snapshot unchanged. Inspected both rendered previews.
- `go test ./...`, `git diff --check`, signed native build passed. Final diff inspected. Fresh read-only review 95e9016d-e750-4189-94fa-26cd934b029a reported no actionable findings (source inspection; live context-menu/VoiceOver interactions not independently exercised).
- Installed with `./scripts/macos.sh install`; installed signature verified, host binary matches dist, app relaunched. Existing preferences and caches preserved. README documents controls and presentation-only scope.

## Antigravity window-first presentation
- macOS now orders Antigravity 5-hour rows before Weekly rows, preserving upstream model order within each window. Labels display `5-hour · Gemini`, `5-hour · Claude + GPT`, then `Weekly · Gemini`, etc., in the standard row font. Wider labels retain inline structure; raw snapshot labels/IDs and saved hidden-item keys are unchanged.
- `AIUSAGE_PANEL_PREVIEW=/tmp/aiusage-antigravity-window-order.png swift test --package-path macos`: 27 passed, including exact label/order and unchanged IDs/other-provider order. Inspected rendered full-panel preview. Diff check, signed build/install and installed binary comparison passed; app relaunched.

## Unified dashboard alignment (completed and installed)
- User requested aligned bars and a common right edge for percentages, credits and Not reported, allowing wider panel and two-line labels with equal row heights.
- Added shared DashboardRowColumns for all quota/benefit summaries: label168, bar120, value104, status126, accessory12, spacing8, horizontal padding14 -> 590pt panel. Values stay single-line with monospaced digits; labels may wrap to two lines. All summaries have 36pt content height plus matching vertical padding; expanded details/notes remain below summaries. Benefit rows reserve an empty bar column, not a fake bar.
- `swift test --package-path macos`: 28 passed. New geometry test verifies actual bar x/width, value right edges/single-line heights, status x and equal row heights (including two-line label). Full hosted-panel tests and hidden-row recovery still pass at new width. Go tests/diff check passed. Inspected generated columns, benefits and full-panel previews. Final Swift suite rerun passed all28. Read-only review51512037-0bf0-4bd1-9ad6-ea2f25377e21 found no actionable issues (static inspection). Final diff inspected; native build/install and installed signature/binary comparison passed. App relaunched; hidden preferences and caches preserved.

## Compact single-line rows
- User superseded two-line preference: now wants much smaller, one-line rows. Set shared summary height22pt (previous36+8padding=44pt), removed extra quota/benefit summary vertical padding, and limited label/status/value cells to one line. Columns/590pt width unchanged; label hover help retains full text.
- `AIUSAGE_PANEL_PREVIEW=/tmp/aiusage-compact-panel.png AIUSAGE_COLUMNS_PREVIEW=/tmp/aiusage-compact-columns.png swift test --package-path macos`: all28 passed, including compact geometry/fullpanel/hide behavior. Inspected compact panel image. Diff check, build/install, installed signature/binary comparison passed; app relaunched.

## Narrower layout exploration (HTML only)
- User requested alternative structures after 590pt panel felt too wide. Created `docs/previews/dashboard-layout-options.html`, an offline local comparison using the repository's provider SVG assets. No app changes/install.
- A:430px compact aligned table, shortened units/times. B:390px integrated percentage+meter column (recommended). C:350px 5-hour/Weekly/Balances tabs. Includes demo reveal/hide, row-detail dialog, keyboard tabs, filter controls, and explicit unknown/unconnected states; fixture only, no account connections.
- HTML parsed/static IDs checked; extracted JavaScript passed `node --check`. Tested in ephemeral nonpersistent WKWebView: exact430/390/350 widths,15loadedicons, reveal/conceal, tab filtering, detail dialog/reset, no row overflow. Inspected `/tmp/aiusage-layout-options.png` rendering. Installed app remains unchanged pending user's choice.
- Follow-up refinement to A only: `Not reported` -> `N/A`, reset counts -> number only; full meaning retained in hover/accessibility/detail text. Reduced value column84->54px and expanded bars78->108px (+38%), preserving430px panel width and label space. B/C benefit values unchanged. WKWebView verified abbreviations, all108px bars, unchanged widths, loaded icons and interaction/overflow checks; revised preview inspected.
- Further HTML-only revisions: A label column now136px (longest current label measured131.15px plus3px padding), value44px/time42px/gaps3px, with bars taking remaining width (173px at430px panel). Removed excess label stretch at narrow viewport sizes too. Percentages use whole-number rounding in all previews/details while bars retain exact values. Restored right-aligned per-provider `Updated 2m ago` fixture labels. WKWebView confirmed173px bars, whole-percent labels, five A timestamps, no clipped A label/value/time cells, interactions and icon loads. Inspected final rendering; app unchanged.

## Implement refined Option A / publish (implemented and installed)
- User approved implementation, commit, push and installation. Target:430pt native panel,173pt aligned bars,22pt single-line rows, whole rounded percentages, N/A/number-only benefit summaries, compact reset/expiration timing, right-aligned provider update text. Preserve full detail/hover/accessibility meanings, unknown-versus-zero, expiry warnings, row hiding, exact bar values and intrinsic resizing.
- Acceptance: format edge-case tests; native column geometry/no-clipping fixtures; full hosted-panel expansion/cap/hide tests; Go/Swift tests and signed build; fresh read-only review; inspect/stage only in-scope previous features plus this layout. Preserve unrelated Kimi files and the Kimi-only hunks in README/main.go. Verify the staged snapshot independently before commit/push to origin/master; install without deleting preferences/caches.
- Implemented the430pt grid (label136/bar173/value44/status42/gaps3/padding13),22pt single-line summaries,8pt bars, whole rounded percentage labels, N/A/number-only benefit values, compact dates/durations and explicit unknown/non-expiring states. Full amounts/units/dates remain in hover/accessibility/expanded grants; partial expirations have an asterisk. Provider update headings unchanged. Left-margin overlays retain per-row warnings and benefit disclosure without consuming chart width.
- Added CompactFormattingTests and full five-provider CompactDashboardTests; strengthened geometry tests for full longest labels, fixed bar width, aligned value/status edges. Native columns/benefits/full-panel previews inspected. Existing sizing/hide/reveal tests passed.
- Fresh read-only review fc30d180-d819-4d93-b44f-4ef98376d5c3 found thin-bar marker padding would erase the marker at1x. Fixed with shared8pt bar height and2/scale inset; regression asserts4pt marker at1x,6pt at2x. Targeted re-review f5403ea7-a944-475d-bcdd-87bedf61ad51 confirmed fix, no remaining actionable findings (source inspection only).
- Working-tree `go test ./...`, `go test -race ./...`, `go vet ./...`, Swift tests and signed build passed. Exact staged snapshot exported via `git checkout-index --all --prefix=/tmp/aiusage-stage.6HCdNo/`: `go test ./...`, `go test -race ./...`, `go vet ./...`, Linux/Windows cross-builds, and `swift test --package-path macos` all passed (34Swift tests). This verifies the publication builds without the excluded Kimi edits. `git diff --cached --check` passed.
- Gracefully quit old app, ran `./scripts/macos.sh install`; installed `codesign --verify --deep --strict` and host/helper `cmp` against dist passed. Updated CLI with `GOBIN="$HOME/.local/bin" go install .`; relaunched app and confirmed running. Install uses the working tree so the existing local Kimi behavior is preserved; preferences/caches untouched.
- Publication scope includes earlier requested benefits, Antigravity and presentation improvements plus refined A. Unrelated Kimi source/tests/plan and Kimi-only README/main.go hunks excluded from staging, retained locally. User authorized commit/push to origin/master; remote matched starting HEAD after fetch. No further implementation work remains.

## Remaining limitations
Claude Platform API promotional-credit collection remains unconnected. Claude reset inventory was not reported in the live check (not proof of zero). Codex credit expiration is unavailable through its app-server, while banked-reset expiration was returned successfully. Do not infer dates or balances from plan allowances.
