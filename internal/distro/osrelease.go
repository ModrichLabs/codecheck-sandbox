// Package distro identifies the Linux distribution family and knows how each
// family installs the packages setup needs.
package distro

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/ModrichLabs/codecheck-sandbox/internal/system"
)

// Family groups distributions by package manager. The names match the
// telemetry enum, so a family needs no translation before it goes out.
type Family string

const (
	Arch     Family = "arch"
	Debian   Family = "debian"
	Fedora   Family = "fedora"
	OpenSUSE Family = "opensuse"
	Unknown  Family = "unknown"
)

// familyOf maps an os-release ID or ID_LIKE entry to its family.
var familyOf = map[string]Family{
	"arch": Arch, "manjaro": Arch, "endeavouros": Arch,
	"debian": Debian, "ubuntu": Debian,
	"fedora": Fedora, "rhel": Fedora, "centos": Fedora, "rocky": Fedora, "almalinux": Fedora,
	"opensuse": OpenSUSE, "suse": OpenSUSE, "sles": OpenSUSE,
	"opensuse-tumbleweed": OpenSUSE, "opensuse-leap": OpenSUSE,
}

// OSRelease holds the fields setup uses from os-release(5).
type OSRelease struct {
	ID         string
	IDLike     []string
	VersionID  string
	PrettyName string
}

// Family maps ID, then each ID_LIKE entry in order, to a family.
func (r OSRelease) Family() Family {
	for _, id := range append([]string{r.ID}, r.IDLike...) {
		if f, ok := familyOf[id]; ok {
			return f
		}
	}
	return Unknown
}

// Detect reads /etc/os-release, falling back to /usr/lib/os-release as
// os-release(5) asks.
func Detect(fsys system.FS) (OSRelease, error) {
	var firstErr error
	for _, p := range []string{"/etc/os-release", "/usr/lib/os-release"} {
		data, err := fsys.ReadFile(p)
		if err == nil {
			return ParseOSRelease(data)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return OSRelease{}, err
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return OSRelease{}, firstErr
}

// ParseOSRelease reads os-release(5) lines: KEY=value, with the value bare or
// in single or double quotes, and \", \\, \$ and \` escapes inside double
// quotes. Comments, blank lines and malformed lines get skipped.
func ParseOSRelease(data []byte) (OSRelease, error) {
	vars := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.ContainsAny(key, " \t") {
			continue
		}
		vars[key] = unquote(val)
	}
	if err := sc.Err(); err != nil {
		return OSRelease{}, err
	}
	rel := OSRelease{
		ID:         vars["ID"],
		IDLike:     strings.Fields(vars["ID_LIKE"]),
		VersionID:  vars["VERSION_ID"],
		PrettyName: vars["PRETTY_NAME"],
	}
	if rel.ID == "" {
		return OSRelease{}, fmt.Errorf("os-release: no ID")
	}
	return rel, nil
}

func unquote(v string) string {
	switch {
	case len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'':
		return v[1 : len(v)-1]
	case len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"':
		v = v[1 : len(v)-1]
		var b strings.Builder
		for i := 0; i < len(v); i++ {
			if v[i] == '\\' && i+1 < len(v) && strings.IndexByte("\"\\$`", v[i+1]) >= 0 {
				i++
			}
			b.WriteByte(v[i])
		}
		return b.String()
	}
	return v
}
