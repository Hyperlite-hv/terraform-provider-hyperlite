package provider

import (
	"context"
	"net/netip"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// The server stores some values in a normal form (app/core/cloudinit_edit.py): a host name or a domain lowercased and
// without its final dot, an address as Python's ipaddress writes it. A value written otherwise would come back
// different, which Terraform reports as an error of the provider after the apply: such values are refused at plan,
// with the form to write.

// labelRe is cloudinit_edit.LABEL_RE, lowercase only.
var labelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

type normalForm struct {
	what  string
	check func(string) (problem string)
}

func (v normalForm) Description(context.Context) string { return v.what }

func (v normalForm) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v normalForm) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := v.check(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid "+v.what, problem)
	}
}

func hostLabel() validator.String {
	return normalForm{"host name", func(s string) string {
		if !labelRe.MatchString(s) {
			return "Lowercase letters, digits and '-', up to 63 characters, as a DNS label (Hyperlite stores a host name lowercased)."
		}
		return ""
	}}
}

func domainName() validator.String {
	return normalForm{"domain name", func(s string) string {
		if len(s) > 253 {
			return "At most 253 characters."
		}
		for _, label := range strings.Split(s, ".") {
			if !labelRe.MatchString(label) {
				return "Lowercase DNS labels separated by dots, without a final dot (Hyperlite stores a domain so): `example.org`."
			}
		}
		return ""
	}}
}

func ipAddress() validator.String {
	return normalForm{"IP address", func(s string) string {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			return "An IPv4 or IPv6 address is expected: `10.0.20.1`, `2001:db8::1`."
		}
		if a.Is4In6() {
			return "Write an IPv4-mapped address as its IPv4 address."
		}
		if a.String() != s {
			return "Write it as `" + a.String() + "`, the form Hyperlite stores."
		}
		return ""
	}}
}

func ipWithPrefix() validator.String {
	return normalForm{"address", func(s string) string {
		p, err := netip.ParsePrefix(s)
		if err != nil || p.Addr().Zone() != "" {
			return "An address with its prefix length is expected: `10.0.20.11/24`, `2001:db8::11/64`."
		}
		if p.Addr().Is4In6() {
			return "Write an IPv4-mapped address as its IPv4 address."
		}
		if p.String() != s {
			return "Write it as `" + p.String() + "`, the form Hyperlite stores."
		}
		return ""
	}}
}
