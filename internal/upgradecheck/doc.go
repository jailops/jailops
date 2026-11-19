// Package upgradecheck holds the tests that keep the promise of docs/upgrade.md: a host of any release since 0.8.0
// upgrades in place. Every directory under internal/store/testdata/upgrade is what that release itself wrote for a small
// fleet (scripts/make-upgrade-fixture.sh makes it from the tag); the tests open each one with the current code.
package upgradecheck
