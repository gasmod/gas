// Package providers holds [FSProvider], which reads the file behind the
// config module's JSON and `.env` providers.
//
// # Usage
//
// The zero value of FSProvider reads through the default file system.
// [FSProvider.SetFS] makes it read through another one, such as an
// [embed.FS]:
//
//	var files providers.FSProvider
//	files.SetFS(fsys) // optional
//	data, err := files.ReadFile("config.json")
//
// SetFS is for setting an FSProvider up. It must not be called while
// another goroutine is in ReadFile on the same FSProvider.
//
// # File systems
//
// The default file system is [sysfs.SysFS]. It reads a file only when it
// lies inside the working directory, symlinks included, is a regular file,
// and is at most 1 MiB; package sysfs describes the checks. A file system
// passed to SetFS is used as it is, without those checks, so an [os.DirFS]
// reads any file under its root, of any size.
//
// A nil file system means the default, so SetFS(nil) undoes an earlier
// SetFS. Only a nil interface counts: a file system that holds a nil value
// of its type is used, such as a nil fstest.MapFS, which holds no files.
//
// # Reading
//
// [FSProvider.ReadFile] reads the named file whole with [fs.ReadFile]. The
// name goes to the file system as it is, so what it may look like depends
// on the file system. sysfs takes a path relative to the working directory
// or an absolute one. [os.DirFS], [embed.FS] and fstest.MapFS take only a
// name that [fs.ValidPath] accepts: slash-separated and unrooted, with no
// `.` or `..` element, so `config.json` and `conf/app.json` work and
// `./config.json` does not.
//
// # Errors
//
// On an error ReadFile returns nil data and the file system's error as it
// is, so [errors.Is] tells a missing file by [fs.ErrNotExist], and sysfs's
// refusals by its sentinel errors. The `.env` provider relies on the first
// to treat a missing file as empty when asked to.
//
// The error does not always name the file, as sysfs's refusals do not, so
// the caller names it. The JSON and `.env` providers wrap it with their own
// sentinel error and the file name:
//
//	failed to read JSON config file app.json: config file too large
//	failed to read .env file .env: openat .env: no such file or directory
package providers
