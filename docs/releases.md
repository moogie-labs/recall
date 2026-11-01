# macOS releases

The release workflow builds an **Apple Silicon, macOS 13+** installer containing
the desktop app, CLI, pinned native runtime and third-party notices. It does not
bundle model weights, initialize collections, or install a background service.

## Credentials

Store these as secrets of the repository's **`release` GitHub environment**, which
both signing jobs reference. Protect that environment with a deployment rule
restricting it to `v*` tags (and, if wanted, a required reviewer; each release
then asks for approval twice, once per signing job). Environment secrets are only
released to jobs that pass those rules. Organization or repository secrets also
work but are available to any workflow, on any branch, that references them.

| Secret | Value |
| --- | --- |
| `APPLE_APPLICATION_CERTIFICATE_P12_BASE64` | Base64 of a Developer ID Application `.p12`, including its private key |
| `APPLE_INSTALLER_CERTIFICATE_P12_BASE64` | Base64 of a Developer ID Installer `.p12`, including its private key |
| `APPLE_APPLICATION_CERTIFICATE_PASSWORD` | Application `.p12` export password |
| `APPLE_INSTALLER_CERTIFICATE_PASSWORD` | Installer `.p12` export password |
| `APPLE_ID` | Apple Account email for notarization |
| `APPLE_TEAM_ID` | Ten-character Apple Developer Team ID |
| `APPLE_APP_SPECIFIC_PASSWORD` | App-specific password, not the account's normal password |

Both certificates must belong to that team and include their private keys. Obtain
them using Xcode's account certificate management or Apple's developer certificate
portal. Export through Keychain Access's **My Certificates** view. To copy an
export as a secret value, use `base64 -i /path/to/certificate.p12 | pbcopy` on macOS.
Base64 is not encryption. Keep exports and passwords out of git, logs and chat.
Create app-specific passwords at <https://account.apple.com/>.

Renew certificates before expiration, export their private keys again, and replace
the corresponding Base64/password secrets together. Replace the notarization
password secret if it is revoked. No personal GitHub token is necessary.

## Release security boundary

`.github/workflows/release.yml` is the **only workflow** that references the Apple
secrets. It triggers only on `v*` **tag pushes**, never branch pushes, pull requests,
manual dispatches or reusable-workflow calls. A job guard additionally checks the
event and tag prefix, and strict version validation runs before credential access.
Secrets are mapped into one step per signing job, not workflow/job-wide env.

The release runs as four jobs on separate, disposable runners:

| Job | Apple secrets | Runs |
| --- | --- | --- |
| `runtime` | Application certificate, notarization | Signs and notarizes the pinned upstream llama.cpp runtime |
| `build` | **none** | Tests, Go modules, npm packages, Wails: compiles the CLI and app against the signed runtime |
| `sign` | Application + Installer certificates, notarization | Signs, notarizes and packages the built payload, then verifies it |
| `publish` | none (`contents: write`) | Checks the checksum and publishes the GitHub release |

**Third-party build code never runs where signing credentials exist.** The two
signing jobs install no Go/Node toolchain. While their temporary keychain exists,
`scripts/release-macos.py` refuses to execute anything except Apple's
`security`, `codesign`, `xcrun`, `ditto`, `pkgbuild` and `productbuild`, so even a
regression in the script cannot run a build tool, the built app or the upstream
runtime with keys loaded. Built code is executed for verification only after the
keychain is deleted. The `build` job refuses to start if any credential variable is
present. A compromised dependency can therefore still affect what it builds (which
is then signed and published by this run, visibly), but cannot use the Developer
ID keys or notarization profile to sign anything else, or steal them.

The desktop app carries the same signed runtime assets in `Contents/Resources/assets`
for first-launch setup. The `sign` job independently checks the handed-off payload:
only regular files and directories with normal permissions, the expected app
identity/version, the model pin from its own checkout, and runtime bytes (the CLI's
and the app's bundled copy) identical to the `runtime` job's signed artifact.

Configure a GitHub tag ruleset restricting `v*` tag creation, updates and deletion
to trusted release maintainers, and protect changes to release scripts/workflows.
Only tag reviewed commits. Anyone able to change release code and push an eligible
tag can run code with the signing credentials. Normal CI must remain credential-free.

Certificates and the notarization profile live in a temporary keychain; cleanup
runs on failure too. The temporary keychain is added to the runner's user search
list for certificate chain lookup, preserving all existing entries; the original
list is restored on success or failure before deleting the temporary keychain.
`codesign --keychain` alone does not make that keychain available for
certificate-chain construction. Application signing selects the imported
certificate's exact SHA-1 fingerprint (only as an identity selector, not an
asset-integrity algorithm); installer signing uses its validated certificate common
name as required by `productbuild`. Subprocesses do not inherit certificate/password
environment variables. An unconditional workflow cleanup also covers interrupted
Python execution. GitHub-hosted runners are disposable; a force-terminated VM is
discarded.

Release builds use the Go toolchain pinned by the `toolchain` line in both `go.mod`
files; the `build` job fails if `go env GOVERSION` differs, so shipped binaries
cannot silently use an older, unpatched Go release. Bump that line for Go security
releases. Third-party notice files are pinned by SHA-256 like the runtime.

The pipeline validates the upstream runtime archive pin, signs its Mach-O files,
and notarizes that runtime in a supported ZIP container. Re-signing changes bytes:
a new runtime archive checksum/size is generated by the `runtime` job; the `build`
job adds the signed payload's `tree_sha256` (computed by Recall's own extraction
code) and embeds that lock in **both** binaries. The repository's upstream pin
remains unchanged. Before every inference-worker launch the binaries rehash the
installed runtime payload against that digest.
The release-local lock also selects `runtime/b11669-<archive-sha256>` inside the
collection. `recall init` installs this immutable release-specific copy alongside
any previous runtime; it does not overwrite/remove old copies or rewrite the
collection's logical `runtime_id`, configuration or authored sources. Repeating
initialization with the same installer is idempotent; a different signed release
gets a separate directory. Unknown changes inside a matching directory still fail
closed. Legacy development locks continue to use `runtime/b11669`.
The model pin stays unchanged. The app is signed, notarized and stapled separately;
the final Developer ID Installer-signed package is notarized and stapled too.
Each notarization submission waits up to 20 minutes; a timeout fails the release.

Signature, payload, notarization, staple and Gatekeeper checks must all pass before
the package/checksum artifact is uploaded. A separate publish job has write
permission but **no Apple secrets**. It verifies the checksum, creates a draft
GitHub release, uploads both assets, then publishes it. Any failure stops publication;
there is no unsigned fallback. Actions are pinned to commit revisions.

## Publish a version

After the workflow and reviewed implementation have reached your intended release
commit, create and push a new tag explicitly:

```sh
git tag -a v0.1.0 -m 'Recall 0.1.0'
git push origin v0.1.0
```

Accepted tags are `vMAJOR.MINOR.PATCH`, optionally with a SemVer prerelease suffix,
for example `v0.1.0-rc.1`. Leading-zero numeric identifiers and build-metadata
suffixes are not accepted. Prerelease tags create GitHub prereleases. The numeric
version is used in macOS package/app metadata; the full version names the assets.
Quit Recall before installing/upgrading, then run `recall init` as your user after
each upgrade to verify/install that release's runtime. Existing workers may finish
their current lifetime using their previous runtime; they normally exit after two
idle minutes. No running worker's executable files are replaced. The installer always targets
`/Applications/Recall.app`, rather than relocating to another copy of the app.
It never edits user collections; initialize or retry initialization as your user.

The published assets are:

- `Recall-0.1.0-darwin-arm64.pkg`
- `Recall-0.1.0-darwin-arm64.pkg.sha256`

Watch **Actions → Release macOS installer**. The first authorized tag run is needed
to prove environment-secret access, certificate validity and Apple notarization;
local credential-free tests cannot establish those facts. Signing errors fail
closed. Password-bearing command arguments/output from credential-management tools
remain withheld. `codesign` failures report whether signing or verification failed,
the target basename and a bounded, credential-redacted diagnostic; terminal controls
are escaped. Redaction remains active after credentials are removed from the build
environment. Use that diagnostic to distinguish certificate-chain, key-access and
payload problems before changing certificates or secrets.
For notarization rejection, the log reports the submission ID; a maintainer can
retrieve Apple's log using `xcrun notarytool log` with their own credentials.

If publication fails after draft creation, inspect the draft and run before doing
anything else. Re-running against an existing release fails rather than replacing
published assets. Do not force-move a published version tag; fix code and release
a new version. Remove an incomplete draft deliberately before retrying the same
tag's publish job, if no version was actually published.

## Credential-free checks

```sh
python3 -m unittest discover -s scripts -p 'test_release_macos.py'
go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.7 .github/workflows/release.yml
go test ./...
sh scripts/build-desktop.sh
(cd desktop && go test ./...)
python3 scripts/verify-desktop.py build/Recall.app
```

Build the desktop before running its Go tests: its Go package embeds the generated
`desktop/frontend/dist`, which is intentionally absent in a clean checkout.

Run release contract tests on macOS; they use Python's standard library and the
macOS-provided Ruby YAML parser and tools. They build and expand an **unsigned fixture installer**
to verify actual payload paths; they do not install it, use Apple credentials,
contact notarization or publish anything. Development app builds remain ad-hoc
signed. A local test pass is not evidence of successful Apple notarization.
