package api

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Component validation for the documented message-component and modal surfaces.
// The numbers follow the component object table: 1 action row, 2 button, 3 string
// select, 4 text input, 5 user select, 6 role select, 7 mentionable select,
// 8 channel select.

const (
	componentActionRow         = 1
	componentButton            = 2
	componentStringSelect      = 3
	componentTextInput         = 4
	componentUserSelect        = 5
	componentRoleSelect        = 6
	componentMentionableSelect = 7
	componentChannelSelect     = 8
)

type componentInput struct {
	Type          int              `json:"type"`
	ComponentType int              `json:"component_type"`
	CustomID      *string          `json:"custom_id"`
	Label         *string          `json:"label"`
	Style         *int             `json:"style"`
	URL           *string          `json:"url"`
	Disabled      *bool            `json:"disabled"`
	Placeholder   *string          `json:"placeholder"`
	MinValues     *int             `json:"min_values"`
	MaxValues     *int             `json:"max_values"`
	Required      *bool            `json:"required"`
	Value         *string          `json:"value"`
	Values        []string         `json:"values"`
	Options       []map[string]any `json:"options"`
	Components    []componentInput `json:"components"`
}

func decodeComponents(raw any) ([]componentInput, string) {
	if raw == nil {
		return nil, ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, "components are not serialisable"
	}
	var out []componentInput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, "components must be an array of component objects"
	}
	return out, ""
}

// validateMessageComponents enforces the shape accepted on message create/edit.
func validateMessageComponents(raw any) string {
	rows, msg := decodeComponents(raw)
	if msg != "" {
		return msg
	}
	if len(rows) == 0 {
		return ""
	}
	if len(rows) > 5 {
		return "at most 5 action rows are allowed"
	}
	for _, row := range rows {
		if row.Type != componentActionRow {
			return "top-level components must be action rows (type 1)"
		}
		if len(row.Components) == 0 || len(row.Components) > 5 {
			return "an action row must contain 1-5 components"
		}
		selects := 0
		for _, child := range row.Components {
			switch child.Type {
			case componentButton:
				if child.Style == nil || *child.Style < 1 || *child.Style > 5 {
					return "button style must be 1-5"
				}
				if child.Style != nil && *child.Style == 5 {
					if child.URL == nil || !validHTTPURL(*child.URL) {
						return "link buttons require a valid url"
					}
				} else {
					if child.CustomID == nil || *child.CustomID == "" || len(*child.CustomID) > 100 {
						return "buttons require a custom_id of 1-100 characters"
					}
				}
				if (child.Label == nil || *child.Label == "") && child.Type == componentButton {
					return "buttons require a label"
				}
				if child.Label != nil && len([]rune(*child.Label)) > 80 {
					return "button label must be 80 characters or fewer"
				}
				selects = 0
			case componentStringSelect, componentUserSelect, componentRoleSelect,
				componentMentionableSelect, componentChannelSelect:
				selects++
				if selects > 1 {
					return "an action row can hold only one select"
				}
				if child.CustomID == nil || *child.CustomID == "" || len(*child.CustomID) > 100 {
					return "selects require a custom_id of 1-100 characters"
				}
				if child.Type == componentStringSelect {
					if len(child.Options) == 0 || len(child.Options) > 25 {
						return "string selects require 1-25 options"
					}
				}
				if child.MinValues != nil && *child.MinValues < 0 {
					return "min_values cannot be negative"
				}
				if child.MinValues != nil && child.MaxValues != nil && *child.MinValues > *child.MaxValues {
					return "min_values cannot exceed max_values"
				}
				if child.MaxValues != nil && *child.MaxValues < 1 {
					return "max_values must be at least 1"
				}
			case componentTextInput:
				return "text inputs are only valid inside modals"
			default:
				return "unknown component type"
			}
		}
	}
	return ""
}

// validateComponentInteraction checks a type-3 interaction's data object.
func validateComponentInteraction(data json.RawMessage) (componentInput, string) {
	var c componentInput
	if len(data) == 0 || json.Unmarshal(data, &c) != nil {
		return c, "component interaction data must be an object"
	}
	if c.CustomID == nil || *c.CustomID == "" || len(*c.CustomID) > 100 {
		return c, "custom_id is required (1-100 characters)"
	}
	ct := c.ComponentType
	if ct == 0 {
		ct = c.Type
	}
	switch ct {
	case componentButton, componentStringSelect, componentUserSelect,
		componentRoleSelect, componentMentionableSelect, componentChannelSelect:
	default:
		return c, "component_type is not a valid interactive component"
	}
	if c.Values != nil && len(c.Values) > 25 {
		return c, "values must contain 25 or fewer entries"
	}
	return c, ""
}

// modalField extracts one submitted field.
type modalField struct {
	CustomID string
	Values   []string
}

// validateModalSubmit checks a type-5 interaction's data object and returns the
// submitted fields.
func validateModalSubmit(data json.RawMessage) (string, []modalField, string) {
	var payload struct {
		CustomID   *string          `json:"custom_id"`
		Components []componentInput `json:"components"`
	}
	if len(data) == 0 || json.Unmarshal(data, &payload) != nil {
		return "", nil, "modal submit data must be an object"
	}
	if payload.CustomID == nil || *payload.CustomID == "" || len(*payload.CustomID) > 100 {
		return "", nil, "custom_id is required (1-100 characters)"
	}
	if len(payload.Components) == 0 {
		return "", nil, "modal submit must include components"
	}
	if len(payload.Components) > 5 {
		return "", nil, "modal submit allows at most 5 action rows"
	}
	fields := []modalField{}
	for _, row := range payload.Components {
		if row.Type != componentActionRow {
			return "", nil, "modal rows must be action rows (type 1)"
		}
		if len(row.Components) == 0 || len(row.Components) > 5 {
			return "", nil, "modal action rows must contain 1-5 components"
		}
		for _, child := range row.Components {
			if child.CustomID == nil || *child.CustomID == "" || len(*child.CustomID) > 100 {
				return "", nil, "modal fields require custom_id (1-100 characters)"
			}
			switch child.Type {
			case componentTextInput:
				f := modalField{CustomID: *child.CustomID}
				if child.Value != nil {
					f.Values = []string{*child.Value}
				}
				fields = append(fields, f)
			case componentStringSelect, componentUserSelect, componentRoleSelect,
				componentMentionableSelect, componentChannelSelect:
				if len(child.Values) > 25 {
					return "", nil, "modal select values must contain 25 or fewer entries"
				}
				fields = append(fields, modalField{CustomID: *child.CustomID, Values: child.Values})
			default:
				return "", nil, "modal fields must be text inputs or selects"
			}
		}
	}
	return *payload.CustomID, fields, ""
}

func validHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}

// summarizeModal renders submitted fields for the in-process callback.
func summarizeModal(fields []modalField) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		value := strings.Join(f.Values, ",")
		parts = append(parts, f.CustomID+"="+value)
	}
	return strings.Join(parts, "; ")
}
