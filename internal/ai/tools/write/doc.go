// Package write holds every PermissionWrite tool in the catalogue: the
// CloudFormation mutators, ECS deploy actions, manifest/file mutators, the
// shell exec passthrough, and the /run-command bridge. Importing this
// package (directly or transitively via ai/tools/all) wires the write half
// of the catalogue.
//
// Write tools never run without consulting the consent Gate (see
// tools.Registry.Call → resolveGate). PR-03 ships with a deny-everything
// default Gate so importing this package is safe even before PR-04 wires the
// real consent flow.
package write
