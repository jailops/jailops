package service

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"jailops.net/jailops/internal/render"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// policyRows are the rows of the Internet that a policy rule gives a jail (a pass on the uplink): what an exposure
// adds to, and what stays when an exposure goes. An exposure's own rules are not among them.
func (s *Service) policyRows(ctx context.Context, j api.Jail) []api.ReachRow {
	refs, err := s.allRefs(j.Metadata.Project)
	if err != nil {
		return nil
	}
	r := s.reachOf(ctx, j, refs, false)
	if r.Error != "" {
		return nil
	}
	var out []api.ReachRow
	for _, row := range r.Rows {
		if row.Source == api.ReachInternet && row.Decision == "pass" && row.Policy != "" && row.Policy != render.ExposeOrigin {
			out = append(out, row)
		}
	}
	return out
}

// rowCovers returns the ports of the request that a row's port list ("80,443", "8000-8100", "all") reaches.
func rowCovers(rowPorts string, ports []string) []string {
	var out []string
	for _, p := range ports {
		lo, hi, err := validate.ParsePortSpec(p)
		if err != nil {
			continue
		}
		for _, r := range strings.Split(rowPorts, ",") {
			if r = strings.TrimSpace(r); r == "all" {
				out = append(out, p)
				break
			}
			rlo, rhi, err := validate.ParsePortSpec(r)
			if err == nil && lo <= rhi && rlo <= hi {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// famWords says the families of a row in words.
func famWords(f string) string {
	switch f {
	case "4":
		return "IPv4"
	case "6":
		return "IPv6"
	}
	return "IPv4 and IPv6"
}

// openedByPolicy says, for an exposure about to be made, which policy rules already let the same protocol and ports in
// from outside, and what the exposure adds to them.
func (s *Service) openedByPolicy(ctx context.Context, j api.Jail, protos, ports []string, routes []exposeRoute, fams []string) []string {
	var notes []string
	seen := map[string]bool{}
	for _, row := range s.policyRows(ctx, j) {
		if row.Proto != "all" && !slices.Contains(protos, row.Proto) {
			continue
		}
		covered := rowCovers(row.Ports, ports)
		if len(covered) == 0 {
			continue
		}
		key := fmt.Sprintf("%s/%d", row.Policy, row.Rule)
		if seen[key] {
			continue
		}
		seen[key] = true
		ps := protos
		if row.Proto != "all" {
			ps = []string{row.Proto}
		}
		tail := "this exposure only records it"
		if natIPv4(routes) && slices.Contains(fams, "4") && !strings.Contains(row.Family, "4") {
			tail = "this exposure adds the IPv4 forward"
		}
		notes = append(notes, fmt.Sprintf("policy %s rule %d already lets %s in over %s; %s", row.Policy, row.Rule, exposureText(covered, ps), famWords(row.Family), tail))
	}
	return notes
}

// stillOpenedByPolicy says, for the jail as it is after an unexpose, which policy rules still let in what went.
func (s *Service) stillOpenedByPolicy(ctx context.Context, after api.Jail, gone []api.ExposeEntry, routes []exposeRoute) []string {
	var protos, ports []string
	for _, e := range gone {
		if !slices.Contains(protos, e.Protocol()) {
			protos = append(protos, e.Protocol())
		}
		for _, p := range e.Ports {
			if !slices.Contains(ports, p) {
				ports = append(ports, p)
			}
		}
	}
	var notes []string
	seen := map[string]bool{}
	for _, row := range s.policyRows(ctx, after) {
		if row.Proto != "all" && !slices.Contains(protos, row.Proto) || len(rowCovers(row.Ports, ports)) == 0 {
			continue
		}
		key := fmt.Sprintf("%s/%d", row.Policy, row.Rule)
		if seen[key] {
			continue
		}
		seen[key] = true
		notes = append(notes, fmt.Sprintf("still reachable over %s: policy %s rule %d", famWords(row.Family), row.Policy, row.Rule))
	}
	return notes
}

// insideRange says why an unexpose of a single port found nothing: the port is inside an exposed range.
func insideRange(entries []api.ExposeEntry, protos, ports []string) []string {
	var notes []string
	for _, p := range ports {
		if strings.Contains(p, "-") {
			continue
		}
		n, _, err := validate.ParsePortSpec(p)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !slices.Contains(protos, e.Protocol()) {
				continue
			}
			for _, item := range e.Ports {
				lo, hi, err := validate.ParsePortSpec(item)
				if err == nil && strings.Contains(item, "-") && lo <= n && n <= hi {
					notes = append(notes, fmt.Sprintf("%s is inside the exposed range %s: unexpose the range to remove it", p, item))
				}
			}
		}
	}
	return notes
}
