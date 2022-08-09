/*
Copyright 2022 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package nsmap

import (
	"github.com/miekg/dns"
)

// stringRewriter rewrites a string
type stringRewriter interface {
	rewriteString(src string) string
}

// remapStringRewriter maps a dedicated string to another string
// it also maps a domain of a subdomain.
type remapStringRewriter struct {
	orig        string
	replacement string
}

func newRemapStringRewriter(orig, replacement string) stringRewriter {
	return &remapStringRewriter{orig, replacement}
}

func (r *remapStringRewriter) rewriteString(src string) string {
	if src == r.orig {
		return r.replacement
	}
	return src
}

// nameRewriterResponseRule maps a record name according to a stringRewriter.
type nameRewriterResponseRule struct {
	stringRewriter
}

func (r *nameRewriterResponseRule) RewriteResponse(rr dns.RR) {
	rr.Header().Name = r.rewriteString(rr.Header().Name)
}

// valueRewriterResponseRule maps a record value according to a stringRewriter.
type valueRewriterResponseRule struct {
	stringRewriter
}

func (r *valueRewriterResponseRule) RewriteResponse(rr dns.RR) {
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
