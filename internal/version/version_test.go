package version

import "testing"

func TestParseAcceptsOnlyReleaseVersions(t *testing.T) {
	for _, valid := range []string{"0.1.0", "1.2.3", "10.20.30", "1.0.0-rc.1", "1.0.0-alpha", "1.0.0-0.3.7", "1.0.0-x-y.7"} {
		if _, ok := Parse(valid); !ok {
			t.Errorf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{"", "dev", "v1.0.0", "1.0", "01.0.0", "1.00.0", "1.0.0-", "1.0.0-01", "1.0.0+build", "1.0.0-rc..1", "1.0.0 ", "99999999999999999999.0.0"} {
		if _, ok := Parse(invalid); ok {
			t.Errorf("accepted %q", invalid)
		}
	}
}

func TestCompareUsesSemVerPrecedence(t *testing.T) {
	// Each version is lower than the next (SemVer 2.0 §11 example plus extras).
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range order {
		for j := range order {
			a, _ := Parse(order[i])
			b, _ := Parse(order[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", order[i], order[j], got, want)
			}
		}
	}
}

func TestStableAndDevelopment(t *testing.T) {
	if v, _ := Parse("1.0.0"); !v.Stable() {
		t.Error("1.0.0 not stable")
	}
	if v, _ := Parse("1.0.0-rc.1"); v.Stable() {
		t.Error("prerelease reported stable")
	}
	previous := Version
	t.Cleanup(func() { Version = previous })
	Version = "dev"
	if !Development() {
		t.Error("dev is not a development build")
	}
	Version = "0.3.0"
	if Development() {
		t.Error("release reported as development build")
	}
}
