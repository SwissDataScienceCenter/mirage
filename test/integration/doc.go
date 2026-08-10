// Package integration holds Mirage's integration tier: the suite that runs it
// against a real etcd and kube-apiserver started by envtest. See ADR 0007, and
// suite_test.go for the suite itself.
//
// This file carries no build tag and declares nothing. Every other file in the
// directory is behind `//go:build integration`, and a package whose files are all
// excluded by a constraint is an error rather than a silent skip under a `./...`
// pattern — so `go vet ./...` and `go build ./...` would both fail without one
// unconditionally buildable file here.
//
// The suite is external (package integration_test), so nothing imports this.
package integration
