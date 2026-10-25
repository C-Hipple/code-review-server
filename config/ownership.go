package config

import "strings"

// ownershipKeySeparator joins a user's login to a workflow name in an
// ownership key. The ASCII unit separator cannot appear in a GitHub login
// (letters, digits and hyphens), so splitting at the first one recovers the
// login exactly, whatever the workflow name holds.
const ownershipKeySeparator = "\x1f"

// OwnershipKey is the name a workflow claims items under: the entry it adds to
// items.workflows, and what every ownership comparison matches on. Workflow
// names are only unique within one user's list — two users may both name a
// workflow "review_requests" — so a user's workflow is keyed by login and name
// together. The server's own workflows (user "", the config file's) keep their
// bare name, which is what every item written before users existed holds, so
// those rows keep matching with no migration.
//
// user is the login as the user registered it, the case their rows carry.
func OwnershipKey(user, name string) string {
	if user == "" {
		return name
	}
	return user + ownershipKeySeparator + name
}

// SplitOwnershipKey undoes OwnershipKey. A key without a separator is one of
// the server's own workflows, user "".
func SplitOwnershipKey(key string) (user, name string) {
	if login, workflow, found := strings.Cut(key, ownershipKeySeparator); found {
		return login, workflow
	}
	return "", key
}
