package pkg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var semverRe = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Semver is a parsed semantic version. Build metadata ("+...") is not accepted,
// because two versions differing only in metadata would be ambiguous to resolve.
type Semver struct {
	Major, Minor, Patch uint64
	Pre                 []string
}

func ParseSemver(s string) (Semver, error) {
	m := semverRe.FindStringSubmatch(s)
	if m == nil || len(s) > 64 {
		return Semver{}, fmt.Errorf("%q is not a valid version; use MAJOR.MINOR.PATCH, optionally with -prerelease", s)
	}
	var v Semver
	var err error
	for i, dst := range []*uint64{&v.Major, &v.Minor, &v.Patch} {
		if *dst, err = strconv.ParseUint(m[i+1], 10, 32); err != nil {
			return Semver{}, fmt.Errorf("version number %q is too large", m[i+1])
		}
	}
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
		for _, id := range v.Pre {
			if isNumeric(id) && (len(id) > 1 && id[0] == '0' || len(id) > 9) {
				return Semver{}, fmt.Errorf("invalid prerelease identifier %q", id)
			}
		}
	}
	return v, nil
}

func (v Semver) IsPrerelease() bool { return len(v.Pre) > 0 }

func (v Semver) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	return s
}

func isNumeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// Compare returns -1, 0 or 1 following the semver precedence rules.
func (a Semver) Compare(b Semver) int {
	for _, p := range [][2]uint64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a.Pre) == 0 && len(b.Pre) == 0:
		return 0
	case len(a.Pre) == 0:
		return 1 // a release is newer than its prereleases
	case len(b.Pre) == 0:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		if x == y {
			continue
		}
		xn, yn := isNumeric(x), isNumeric(y)
		switch {
		case xn && yn:
			xi, _ := strconv.ParseUint(x, 10, 64)
			yi, _ := strconv.ParseUint(y, 10, 64)
			if xi < yi {
				return -1
			}
			return 1
		case xn:
			return -1
		case yn:
			return 1
		case x < y:
			return -1
		default:
			return 1
		}
	}
	switch {
	case len(a.Pre) < len(b.Pre):
		return -1
	case len(a.Pre) > len(b.Pre):
		return 1
	}
	return 0
}
