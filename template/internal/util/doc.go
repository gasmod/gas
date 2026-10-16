// Package util holds helpers shared by the template stores. RegisterFS
// walks an fs.FS and registers every file with a given extension into a
// gas.TemplateProvider, named by its slash-separated path, and stops at the
// first read or registration error.
package util
