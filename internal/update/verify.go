package update

import (
	"bytes"
	"context"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	checksumLine  = regexp.MustCompile(`^([0-9a-f]{64})  (\S+)\n$`)
	installerLeaf = regexp.MustCompile(`^1\. Developer ID Installer: .+ \(([A-Z0-9]{10})\)$`)
)

const (
	distributionStatus = "Status: signed by a developer certificate issued by Apple for distribution"
	notarizedStatus    = "Notarization: trusted by the Apple notary service"
)

// verifyChecksum checks the published "<sha256>  <name>\n" line. It detects
// only corruption: the checksum comes from the same release as the package.
func verifyChecksum(file []byte, name string, got []byte) error {
	m := checksumLine.FindSubmatch(file)
	if m == nil || string(m[2]) != name {
		return unverified("the published installer checksum is malformed")
	}
	want, err := hex.DecodeString(string(m[1]))
	if err != nil || !bytes.Equal(want, got) {
		return unverified("the downloaded installer does not match its published checksum")
	}
	return nil
}

// verifyPackage establishes authenticity: a notarized Developer ID Installer
// signature from the running copy's team, accepted by Gatekeeper.
func (u *Updater) verifyPackage(ctx context.Context, pkg, team string) error {
	out, _, err := u.Run(ctx, "/usr/sbin/pkgutil", "--check-signature", pkg)
	if err != nil {
		return unverified("the downloaded installer is not validly signed")
	}
	distribution, notarized, leaf := false, false, ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == distributionStatus:
			distribution = true
		case line == notarizedStatus:
			notarized = true
		case leaf == "" && strings.HasPrefix(line, "1. "):
			leaf = "unexpected certificate"
			if m := installerLeaf.FindStringSubmatch(line); m != nil {
				leaf = m[1]
			}
		}
	}
	if !distribution || !notarized {
		return unverified("the downloaded installer is not signed and notarized for distribution")
	}
	if leaf != team {
		return unverified("the downloaded installer was not signed by the developer of this copy of Recall")
	}
	if _, _, err := u.Run(ctx, "/usr/sbin/spctl", "--assess", "--type", "install", pkg); err != nil {
		return unverified("Gatekeeper rejected the downloaded installer")
	}
	return nil
}
