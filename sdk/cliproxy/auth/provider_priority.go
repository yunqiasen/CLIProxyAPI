package auth

import "strconv"

const (
	// AttributeProviderPriority keeps supplier preference separate from key priority.
	AttributeProviderPriority = "provider_priority"
	// AttributeProviderGroup identifies keys belonging to one configured supplier.
	AttributeProviderGroup = "provider_group"
)

// authProviderPriority retains the legacy single-priority contract for credentials
// that do not originate from a configured supplier group (including OAuth files).
func authProviderPriority(auth *Auth) int {
	if auth != nil {
		if priority, err := strconv.Atoi(auth.Attributes[AttributeProviderPriority]); err == nil {
			return priority
		}
	}
	return authPriority(auth)
}

// groupKeyPriorities only compares keys within the same supplier. Equal-priority
// suppliers remain peers even when their internal key priorities differ.
type groupKeyPriorities map[string]int

func (priorities groupKeyPriorities) observe(auth *Auth) {
	if auth == nil {
		return
	}
	group := auth.Attributes[AttributeProviderGroup]
	if group == "" {
		return
	}
	priority := authPriority(auth)
	if previous, ok := priorities[group]; !ok || priority > previous {
		priorities[group] = priority
	}
}

func (priorities groupKeyPriorities) allows(auth *Auth) bool {
	if auth == nil {
		return true
	}
	priority, grouped := priorities[auth.Attributes[AttributeProviderGroup]]
	return !grouped || authPriority(auth) == priority
}

func highestKeyPriorityAuths(auths []*Auth) []*Auth {
	priorities := make(groupKeyPriorities)
	for _, auth := range auths {
		priorities.observe(auth)
	}
	if len(priorities) == 0 {
		return auths
	}
	selected := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if priorities.allows(auth) {
			selected = append(selected, auth)
		}
	}
	return selected
}

// highestKeyPriorityPredicate accounts for retry exclusions and request-specific
// eligibility before choosing the key tier, so an excluded key never hides backups.
func highestKeyPriorityPredicate(entries []*scheduledAuth, predicate func(*scheduledAuth) bool) func(*scheduledAuth) bool {
	priorities := make(groupKeyPriorities)
	for _, entry := range entries {
		if entry != nil && (predicate == nil || predicate(entry)) {
			priorities.observe(entry.auth)
		}
	}
	if len(priorities) == 0 {
		return predicate
	}
	return func(entry *scheduledAuth) bool {
		return entry != nil && (predicate == nil || predicate(entry)) && priorities.allows(entry.auth)
	}
}
