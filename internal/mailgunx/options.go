package mailgunx

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/devrfc83/mailgun-passport/internal/parser"
)

// options adds the Mailgun options of a message to form: the defaults from the
// configuration (MAILGUN_TAGS, MAILGUN_TRACKING_*, MAILGUN_REQUIRE_TLS), then
// the X-Mailgun-* header fields the client set, which take precedence. A
// header field with an invalid value is ignored and logged, since an option
// is not worth rejecting the message for. The test mode flag is never taken
// from a header: only the operator decides it.
func (c *Client) options(form *formData, cfg *settings, msg *parser.Message) {
	opts := map[string][]string{}
	if len(cfg.tags) > 0 {
		opts["o:tag"] = cfg.tags
	}
	if cfg.clicks != "" {
		opts["o:tracking-clicks"] = []string{cfg.clicks}
	}
	if cfg.opens != "" {
		opts["o:tracking-opens"] = []string{cfg.opens}
	}
	if cfg.requireTLS {
		opts["o:require-tls"] = []string{"yes"}
	}

	var vars map[string]string
	for _, name := range parser.MailgunHeaderNames {
		value, ok := msg.MailgunHeaders[name]
		if !ok {
			continue
		}
		field, v, err := headerOption(name, value)
		if err != nil {
			c.log.Warn("ignoring an X-Mailgun header field", "header", name, "err", err)
			continue
		}
		if name == "x-mailgun-variables" {
			vars, err = parseVariables(value)
			if err != nil {
				c.log.Warn("ignoring an X-Mailgun header field", "header", name, "err", err)
			}
			continue
		}
		opts[field] = v
	}

	for _, field := range slices.Sorted(maps.Keys(opts)) {
		for _, v := range opts[field] {
			form.field(field, v)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		form.field("v:"+k, vars[k])
	}
}

// headerOption maps an X-Mailgun-* header field to the API field and values.
func headerOption(name, value string) (field string, values []string, err error) {
	switch name {
	case "x-mailgun-tag":
		if len(value) > 128 {
			return "", nil, fmt.Errorf("tag is longer than 128 characters")
		}
		return "o:tag", []string{value}, nil
	case "x-mailgun-track":
		return yesNo("o:tracking", value)
	case "x-mailgun-track-opens":
		return yesNo("o:tracking-opens", value)
	case "x-mailgun-dkim":
		return yesNo("o:dkim", value)
	case "x-mailgun-require-tls":
		return yesNo("o:require-tls", value)
	case "x-mailgun-skip-verification":
		return yesNo("o:skip-verification", value)
	case "x-mailgun-track-clicks":
		if v := strings.ToLower(value); v == "htmlonly" {
			return "o:tracking-clicks", []string{v}, nil
		}
		return yesNo("o:tracking-clicks", value)
	case "x-mailgun-variables":
		return "", nil, nil // read by parseVariables
	}
	return "", nil, fmt.Errorf("unsupported header")
}

func yesNo(field, value string) (string, []string, error) {
	switch strings.ToLower(value) {
	case "yes", "true":
		return field, []string{"yes"}, nil
	case "no", "false":
		return field, []string{"no"}, nil
	}
	return "", nil, fmt.Errorf("expected yes or no, got %q", sanitize(value, 40))
}

// parseVariables reads the JSON object of X-Mailgun-Variables. String values
// are sent as they are and any other value as its JSON text.
func parseVariables(value string) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if k == "" {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
		} else {
			out[k] = string(v)
		}
	}
	return out, nil
}
