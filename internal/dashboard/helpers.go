package dashboard

import (
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

func itoa(v int) string { return strconv.Itoa(v) }

// CIDR lists are sets in the appliance model. Use one representation for
// desired fields, option dictionaries and plain-string readbacks.
func canonicalCIDRs(values []string) string {
	set := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if prefix, err := netip.ParsePrefix(value); err == nil {
			value = prefix.String()
		}
		if value != "" {
			set[value] = true
		}
	}
	ordered := make([]string, 0, len(set))
	for value := range set {
		ordered = append(ordered, value)
	}
	sort.Strings(ordered)
	return strings.Join(ordered, ",")
}
