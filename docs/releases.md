# macOS releases

The release workflow builds an **Apple Silicon, macOS 13+** installer containing
the desktop app, CLI, pinned native runtime and third-party notices. It does not
bundle model weights, initialize collections, or install a background service.

## Credentials

Configure these GitHub Actions organization secrets and grant **this repository**
access. Private-repository access also depends on the organization's GitHub plan.

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
The secrets are mapped into just one signing step, not workflow/job-wide env.

Organization secrets themselves are repository-scoped, not event-scoped: this
boundary is enforced by the workflow, not by an organization-secret event filter.
Protect changes to release scripts/workflows and configure a GitHub tag ruleset
restricting `v*` tag creation, updates and deletion to trusted release maintainers.
Only tag reviewed commits. Anyone able to change release code and push an eligible
tag can run code with the signing credentials. Normal CI must remain credential-free.

The installer job has read-only repository permission. It runs tests and a
production desktop build before importing credentials. Certificates and the
notarization profile live in a temporary keychain; cleanup runs on failure too.
Build subprocesses do not inherit certificate/password environment variables.
An unconditional workflow cleanup also covers interrupted Python execution.
GitHub-hosted runners are disposable; a force-terminated VM is discarded.

The pipeline validates the upstream runtime archive pin, signs its Mach-O files,
and notarizes that runtime in a supported ZIP container. Re-signing changes bytes:
a new runtime archive checksum/size is generated in disposable source staging and
embedded in **both** binaries; the repository's upstream pin remains unchanged.
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
to prove organization-secret access, certificate validity and Apple notarization;
local credential-free tests cannot establish those facts. Signing errors fail
closed and intentionally omit password-bearing command arguments/output.
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
