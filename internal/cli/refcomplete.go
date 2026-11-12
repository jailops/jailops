package cli

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Completion of a firewall reference, in two levels. Where a reference is expected, Tab offers the kinds (jail:, set:,
// tag:, any, host, as far as the place takes them); after a kind it offers the names that exist in the project. A
// reference is always kind:name: there is no sigil to type.

// refPlace says which references a place takes.
type refPlace struct {
	tag, any, host bool
	// self is a name that is left out of its own kind: the address set being made, the app being allowed.
	selfKind string
	selfName func(args []string) string
}

var (
	// placeRule is a from or to of a firewall rule.
	placeRule = refPlace{tag: true, any: true}
	// placeMember is a member of an address set.
	placeMember = refPlace{selfKind: "set", selfName: firstArg}
	// placeSource is the source of app allow and app deny.
	placeSource = refPlace{host: true, selfKind: "jail", selfName: firstArg}
	// placeEnd is one end of a firewall test.
	placeEnd = refPlace{host: true}
)

func firstArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

// refNames are the names a reference can use in the project.
type refNames struct{ jails, sets, tags []string }

// loadRefNames asks the server for the jails, the address sets and the firewall tags of the project; nothing is
// offered when no server answers.
func (a *app) loadRefNames(cmd *cobra.Command, kinds ...string) refNames {
	var rn refNames
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	c, err := a.connect(ctx)
	if err != nil {
		return rn
	}
	if slices.Contains(kinds, "jail") || slices.Contains(kinds, "tag") {
		tags := map[string]bool{}
		for j, err := range c.Jails().List(ctx) {
			if err != nil {
				break
			}
			rn.jails = append(rn.jails, j.Metadata.Name)
			for _, t := range j.Spec.Firewall.Tags {
				tags[t] = true
			}
		}
		for t := range tags {
			rn.tags = append(rn.tags, t)
		}
		slices.Sort(rn.tags)
	}
	if slices.Contains(kinds, "set") {
		for s, err := range c.Firewall().AddressSets().List(ctx) {
			if err != nil {
				break
			}
			rn.sets = append(rn.sets, s.Metadata.Name)
		}
	}
	return rn
}

// refWords are the references that complete word in a place: the kinds while no kind is typed, the names after one.
func refWords(word string, p refPlace, rn refNames, self string) []string {
	kinds := []string{"jail:", "set:"}
	if p.tag {
		kinds = append(kinds, "tag:")
	}
	if p.any {
		kinds = append(kinds, "any")
	}
	if p.host {
		kinds = append(kinds, "host")
	}
	for _, k := range []struct {
		prefix string
		names  []string
	}{{"jail:", rn.jails}, {"set:", rn.sets}, {"tag:", rn.tags}} {
		if !strings.HasPrefix(word, k.prefix) || !slices.Contains(kinds, k.prefix) {
			continue
		}
		var out []string
		for _, n := range k.names {
			if self != "" && k.prefix == p.selfKind+":" && n == self {
				continue
			}
			if strings.HasPrefix(k.prefix+n, word) {
				out = append(out, k.prefix+n)
			}
		}
		return out
	}
	var out []string
	for _, k := range kinds {
		if strings.HasPrefix(k, word) {
			out = append(out, k)
		}
	}
	return out
}

// kindNames are the kinds of a place that need the server's names.
func (p refPlace) kindNames() []string {
	k := []string{"jail", "set"}
	if p.tag {
		k = append(k, "tag")
	}
	return k
}

// noSpaceIfKind keeps the cursor next to a kind that was just completed (jail:), where a name follows.
func noSpaceIfKind(words []string) cobra.ShellCompDirective {
	d := cobra.ShellCompDirectiveNoFileComp
	for _, w := range words {
		if strings.HasSuffix(w, ":") {
			d |= cobra.ShellCompDirectiveNoSpace
			break
		}
	}
	return d
}

// refCompleter completes one reference where a flag value or an argument is that reference.
func (a *app) refCompleter(p refPlace) completer {
	return func(cmd *cobra.Command, args []string, word string) ([]string, cobra.ShellCompDirective) {
		self := ""
		if p.selfName != nil {
			self = p.selfName(args)
		}
		words := refWords(word, p, a.loadRefNames(cmd, p.kindNames()...), self)
		return words, noSpaceIfKind(words)
	}
}

// ruleWords are the words of a --rule that Tab offers, for the part of the rule typed so far: ACTION and DIRECTION,
// then the protocol and the keywords, and a reference list after from and to. Each candidate is the whole text of the
// rule up to and including the word, so that the shell replaces the string typed so far.
func (a *app) completeRule() completer {
	return func(cmd *cobra.Command, args []string, typed string) ([]string, cobra.ShellCompDirective) {
		head, word := splitLastWord(typed)
		fields := strings.Fields(head)
		var words []string
		switch {
		case len(fields) == 0:
			words = []string{"allow", "deny"}
		case len(fields) == 1:
			words = []string{"in", "out"}
		default:
			prev := fields[len(fields)-1]
			switch {
			case ruleInComment(fields):
				return nil, cobra.ShellCompDirectiveNoFileComp
			case prev == "from" || prev == "to":
				// a reference list: the part after the last comma is the word
				return a.completeRuleRef(cmd, head, word)
			case prev == "port" || prev == "name" || prev == "rdr":
				return nil, cobra.ShellCompDirectiveNoFileComp
			case len(fields) == 2:
				words = append(slices.Clone(ruleProtos), ruleKeywords...)
			default:
				words = slices.Clone(ruleKeywords)
			}
		}
		var out []string
		for _, w := range words {
			if strings.HasPrefix(w, word) {
				out = append(out, head+w)
			}
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

var (
	ruleProtos   = []string{"tcp", "udp", "icmp", "icmp6"}
	ruleKeywords = []string{"from", "to", "port", "name", "rdr", "log", "disabled", "comment"}
)

// completeRuleRef completes a reference list of a rule: head is the rule up to the list, word the part typed of it.
func (a *app) completeRuleRef(cmd *cobra.Command, head, word string) ([]string, cobra.ShellCompDirective) {
	// word may be a comma list; the references before the last comma stay as they are
	done, cur := "", word
	if i := strings.LastIndex(word, ","); i >= 0 {
		done, cur = word[:i+1], word[i+1:]
	}
	words := refWords(cur, placeRule, a.loadRefNames(cmd, placeRule.kindNames()...), "")
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, head+done+w)
	}
	return out, noSpaceIfKind(words)
}

// ruleInComment says whether the rule has reached its comment, which takes the rest of the rule.
func ruleInComment(fields []string) bool { return slices.Contains(fields, "comment") }

// splitLastWord splits the text typed so far into what is complete (with its trailing space) and the word being typed.
func splitLastWord(s string) (head, word string) {
	i := strings.LastIndexAny(s, " \t")
	if i < 0 {
		return "", s
	}
	return s[:i+1], s[i+1:]
}
