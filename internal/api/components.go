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
	SKU           *string          `json:"sku_id"`
	Disabled      *bool            `json:"disabled"`
	Placeholder   *string          `json:"placeholder"`
	MinValues     *int             `json:"min_values"`
	MaxValues     *int             `json:"max_values"`
	Required      *bool            `json:"required"`
	Value         *string          `json:"value"`
	Values        []string         `json:"values"`
	Options       []map[string]any `json:"options"`
	Components    []componentInput `json:"components"`
	// Components v2 fields.
	ID          *int            `json:"id"`
	Content     *string         `json:"content"`
	Items       []componentItem `json:"items"`
	Accessory   *componentInput `json:"accessory"`
	Media       *componentMedia `json:"media"`
	File        *componentMedia `json:"file"`
	AccentColor *int            `json:"accent_color"`
	Spoiler     *bool           `json:"spoiler"`
	Divider     *bool           `json:"divider"`
	Spacing     *int            `json:"spacing"`
	Description *string         `json:"description"`
}

// componentItem is one media-gallery entry.
type componentItem struct {
	Media       *componentMedia `json:"media"`
	Description *string         `json:"description"`
	Spoiler     *bool           `json:"spoiler"`
}

// componentMedia is the unfurled media item; only url is client-settable.
type componentMedia struct {
	URL string `json:"url"`
}

// ComponentTypeISComponentsV2 is the message flag enabling the v2 system.
const flagComponentsV2 = 1 << 15

// Component types introduced by components v2.
const (
	componentSection      = 9
	componentTextDisplay  = 10
	componentThumbnail    = 11
	componentMediaGallery = 12
	componentFile         = 13
	componentSeparator    = 14
	componentContainer    = 17
)

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

// validateMessageComponents enforces the legacy (non-v2) message shape.
func validateMessageComponents(raw any) string {
	return validateMessageComponentsWithFlags(raw, 0)
}

// validateMessageComponentsWithFlags dispatches on the IS_COMPONENTS_V2 flag: the
// legacy action-row rules apply without it, and the v2 layout rules apply with it.
func validateMessageComponentsWithFlags(raw any, flags int) string {
	if flags&flagComponentsV2 != 0 {
		return validateComponentsV2(raw)
	}
	return validateLegacyComponents(raw)
}

func validateLegacyComponents(raw any) string {
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

// validateComponentsV2 enforces the documented layout rules: total component count,
// which types may be top-level, section children/accessory, container children and
// text length.
func validateComponentsV2(raw any) string {
	rows, msg := decodeComponents(raw)
	if msg != "" {
		return msg
	}
	if len(rows) == 0 {
		return "components v2 messages require at least one component"
	}
	total := 0
	var count func(c componentInput) int
	count = func(c componentInput) int {
		n := 1
		for _, ch := range c.Components {
			n += count(ch)
		}
		if c.Accessory != nil {
			n += count(*c.Accessory)
		}
		return n
	}
	ids := map[int]bool{}
	checkID := func(c componentInput) string {
		if c.ID == nil {
			return ""
		}
		if *c.ID < 0 {
			return "component id cannot be negative"
		}
		if ids[*c.ID] {
			return "component ids must be unique within a message"
		}
		ids[*c.ID] = true
		return ""
	}
	textTotal := 0
	for _, top := range rows {
		total += count(top)
		if msg := checkID(top); msg != "" {
			return msg
		}
	}
	if total > 40 {
		return "a components v2 message allows at most 40 components"
	}
	for _, top := range rows {
		if msg := validateV2Component(top, false, &textTotal); msg != "" {
			return msg
		}
	}
	if textTotal > 4000 {
		return "text display content totals more than 4000 characters"
	}
	return ""
}

// validateV2Component validates one component, its children and accessory.
func validateV2Component(c componentInput, inContainer bool, textTotal *int) string {
	switch c.Type {
	case componentContainer:
		if inContainer {
			return "containers cannot be nested"
		}
		if c.AccentColor != nil && (*c.AccentColor < 0 || *c.AccentColor > 0xFFFFFF) {
			return "accent_color must be between 0x000000 and 0xFFFFFF"
		}
		if len(c.Components) == 0 {
			return "a container must contain at least one component"
		}
		for _, child := range c.Components {
			switch child.Type {
			case componentActionRow, componentTextDisplay, componentSection,
				componentMediaGallery, componentSeparator, componentFile:
			default:
				return "containers allow action rows, text displays, sections, media galleries, separators and files"
			}
			if msg := validateV2Component(child, true, textTotal); msg != "" {
				return msg
			}
		}
		return ""
	case componentSection:
		if len(c.Components) == 0 || len(c.Components) > 3 {
			return "a section must contain 1-3 text displays"
		}
		for _, child := range c.Components {
			if child.Type != componentTextDisplay {
				return "sections only allow text display children"
			}
			if msg := validateV2Component(child, inContainer, textTotal); msg != "" {
				return msg
			}
		}
		if c.Accessory == nil {
			return "a section requires an accessory"
		}
		switch c.Accessory.Type {
		case componentButton:
			if msg := validateButton(*c.Accessory); msg != "" {
				return msg
			}
		case componentThumbnail:
			if msg := validateThumbnail(*c.Accessory); msg != "" {
				return msg
			}
		default:
			return "a section accessory must be a button or thumbnail"
		}
		return ""
	case componentTextDisplay:
		if c.Content == nil {
			return "text displays require content"
		}
		n := len([]rune(*c.Content))
		if n == 0 {
			return "text display content cannot be empty"
		}
		if n > 4000 {
			return "text display content must be 4000 characters or fewer"
		}
		*textTotal += n
		return ""
	case componentThumbnail:
		return validateThumbnail(c)
	case componentMediaGallery:
		if len(c.Items) == 0 || len(c.Items) > 10 {
			return "media galleries require 1-10 items"
		}
		for _, it := range c.Items {
			if it.Media == nil || it.Media.URL == "" {
				return "media gallery items require media.url"
			}
			if it.Description != nil && len([]rune(*it.Description)) > 1024 {
				return "media descriptions must be 1024 characters or fewer"
			}
		}
		return ""
	case componentFile:
		if c.File == nil || !strings.HasPrefix(c.File.URL, "attachment://") {
			return "file components require an attachment:// url"
		}
		if c.File.URL == "attachment://" {
			return "file components require a filename after attachment://"
		}
		return ""
	case componentSeparator:
		if c.Spacing != nil && (*c.Spacing < 1 || *c.Spacing > 2) {
			return "separator spacing must be 1 or 2"
		}
		return ""
	case componentActionRow:
		return validateLegacyRow(c)
	default:
		return "unknown component type"
	}
}

// validateLegacyRow applies action-row rules to one already-decoded row.
func validateLegacyRow(row componentInput) string {
	rows, msg := decodeComponents([]componentInput{row})
	if msg != "" {
		return msg
	}
	if len(rows) != 1 {
		return "invalid action row"
	}
	return validateLegacyComponents([]componentInput{row})
}

func validateThumbnail(c componentInput) string {
	if c.Media == nil || c.Media.URL == "" {
		return "thumbnails require media.url"
	}
	if c.Description != nil && len([]rune(*c.Description)) > 1024 {
		return "thumbnail description must be 1024 characters or fewer"
	}
	return ""
}

func validateButton(child componentInput) string {
	if child.Style == nil || *child.Style < 1 || *child.Style > 6 {
		return "button style must be 1-6"
	}
	if *child.Style == 5 {
		if child.URL == nil || !validHTTPURL(*child.URL) {
			return "link buttons require a valid url"
		}
		return ""
	}
	if *child.Style == 6 {
		if child.SKU == nil || *child.SKU == "" {
			return "premium buttons require a sku_id"
		}
		return ""
	}
	if child.CustomID == nil || *child.CustomID == "" || len(*child.CustomID) > 100 {
		return "buttons require a custom_id of 1-100 characters"
	}
	if child.Label == nil || *child.Label == "" {
		return "buttons require a label"
	}
	if len([]rune(*child.Label)) > 80 {
		return "button label must be 80 characters or fewer"
	}
	if child.URL != nil || child.SKU != nil {
		return "non-link buttons cannot set url or sku_id"
	}
	return ""
}
