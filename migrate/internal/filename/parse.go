package filename

import (
	"path"
	"strings"
)

// Kind is what a migration file holds.
type Kind int

const (
	// Up is a file of up SQL, named `<stem>.up.sql`.
	Up Kind = iota + 1
	// Down is a file of down SQL, named `<stem>.down.sql`.
	Down
)

// The suffixes of the two kinds of file.
const (
	// upSuffix ends the name of an up file.
	upSuffix = ".up.sql"
	// downSuffix ends the name of a down file.
	downSuffix = ".down.sql"
)

// Name is what the name of a migration file says, as described in the
// package documentation.
type Name struct {
	// Stem is the base name without its suffix.
	Stem string
	// Version is the stem up to its first underscore.
	Version string
	// Description is the rest of the stem, underscores turned to spaces.
	Description string
	// Kind is what the file holds.
	Kind Kind
}

// Parse returns what the file named name says, and false when name is not
// the name of a migration file, as described in the package documentation.
func Parse(name string) (Name, bool) {
	base := path.Base(name)

	var n Name

	switch {
	case strings.HasSuffix(base, upSuffix):
		n.Stem, n.Kind = strings.TrimSuffix(base, upSuffix), Up
	case strings.HasSuffix(base, downSuffix):
		n.Stem, n.Kind = strings.TrimSuffix(base, downSuffix), Down
	default:
		return Name{}, false
	}

	version, desc, _ := strings.Cut(n.Stem, "_")

	n.Version = version
	n.Description = strings.ReplaceAll(desc, "_", " ")

	return n, true
}
