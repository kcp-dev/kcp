// This file is a subset of https://github.com/coredns/coredns/blob/v1.10.0/plugin/rewrite/name.go
//
// The following changes have been applied compare to the original code:
// - remove code not related to exact matching
// - export some structs and functions

package coredns

import (
	"github.com/miekg/dns"
)

// RemapStringRewriter maps a dedicated string to another string
// it also maps a domain of a subdomain.
type RemapStringRewriter struct {
	orig        string
	replacement string
}

func NewRemapStringRewriter(orig, replacement string) *RemapStringRewriter {
	return &RemapStringRewriter{orig, replacement}
}

func (r *RemapStringRewriter) rewriteString(src string) string {
	if src == r.orig {
		return r.replacement
	}
	return src
}

// NameRewriterResponseRule maps a record name according to a stringRewriter.
type NameRewriterResponseRule struct {
	*RemapStringRewriter
}

func (r *NameRewriterResponseRule) RewriteResponse(rr dns.RR) {
	rr.Header().Name = r.rewriteString(rr.Header().Name)
}

// ValueRewriterResponseRule maps a record value according to a stringRewriter.
type ValueRewriterResponseRule struct {
	*RemapStringRewriter
}

func (r *ValueRewriterResponseRule) RewriteResponse(rr dns.RR) {
	value := getRecordValueForRewrite(rr)
	if value != "" {
		new := r.rewriteString(value)
		if new != value {
			setRewrittenRecordValue(rr, new)
		}
	}
}

func getRecordValueForRewrite(rr dns.RR) (name string) {
	switch rr.Header().Rrtype {
	case dns.TypeSRV:
		return rr.(*dns.SRV).Target
	case dns.TypeMX:
		return rr.(*dns.MX).Mx
	case dns.TypeCNAME:
		return rr.(*dns.CNAME).Target
	case dns.TypeNS:
		return rr.(*dns.NS).Ns
	case dns.TypeDNAME:
		return rr.(*dns.DNAME).Target
	case dns.TypeNAPTR:
		return rr.(*dns.NAPTR).Replacement
	case dns.TypeSOA:
		return rr.(*dns.SOA).Ns
	default:
		return ""
	}
}

func setRewrittenRecordValue(rr dns.RR, value string) {
	switch rr.Header().Rrtype {
	case dns.TypeSRV:
		rr.(*dns.SRV).Target = value
	case dns.TypeMX:
		rr.(*dns.MX).Mx = value
	case dns.TypeCNAME:
		rr.(*dns.CNAME).Target = value
	case dns.TypeNS:
		rr.(*dns.NS).Ns = value
	case dns.TypeDNAME:
		rr.(*dns.DNAME).Target = value
	case dns.TypeNAPTR:
		rr.(*dns.NAPTR).Replacement = value
	case dns.TypeSOA:
		rr.(*dns.SOA).Ns = value
	}
}
