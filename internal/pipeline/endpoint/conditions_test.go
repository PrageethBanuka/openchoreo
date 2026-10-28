// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package endpointpipeline

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

// fullMatch evaluates re against s the way Gateway API, Istio and Envoy do:
// RE2, anchored to the whole header value.
func fullMatch(re, s string) bool {
	return regexp.MustCompile("^(?:" + re + ")$").MatchString(s)
}

func TestCookieRegex(t *testing.T) {
	re := CookieRegex("exp_cart", "1.0")

	for _, header := range []string{
		"exp_cart=1.0",
		"session=abc; exp_cart=1.0",
		"exp_cart=1.0; theme=dark",
		"a=1;exp_cart=1.0;b=2",
	} {
		assert.True(t, fullMatch(re, header), header)
	}
	for _, header := range []string{
		"exp_cart=1x0",       // the value's dot is literal
		"exp_cart=1.01",      // no prefix match on the value
		"my_exp_cart=1.0",    // no suffix match on the name
		"session=exp_cart=1", // not a cookie of its own
	} {
		assert.False(t, fullMatch(re, header), header)
	}
}

func TestNewMatch(t *testing.T) {
	m := NewMatch(
		&PathMatch{Value: "/next"},
		[]HeaderMatch{
			{Name: "X-Client", Value: "ios"},
			{Name: "User-Agent", Type: HeaderMatchRegularExpression, Value: ".*Mobi.*"},
		},
		[]CookieMatch{{Name: "exp_cartrecs", Value: "b"}},
	)

	assert.Equal(t, []HeaderMatch{
		{Name: "x-client", Type: HeaderMatchExact, Value: "ios"},
		{Name: "user-agent", Type: HeaderMatchRegularExpression, Value: ".*Mobi.*"},
		{Name: "cookie", Type: HeaderMatchRegularExpression, Value: CookieRegex("exp_cartrecs", "b")},
	}, m.Conditions)
	assert.Equal(t, "x-client", m.Headers[0].Name)
	assert.Equal(t, []CookieMatch{{Name: "exp_cartrecs", Value: "b"}}, m.Cookies)
	assert.Equal(t, &PathMatch{Type: PathMatchPathPrefix, Value: "/next"}, m.Path)

	empty := NewMatch(nil, nil, nil)
	assert.Nil(t, empty.Path)
	assert.NotNil(t, empty.Conditions)
	assert.NotNil(t, empty.Headers)
	assert.NotNil(t, empty.Cookies)
	assert.NotNil(t, empty.QueryParams)
}
