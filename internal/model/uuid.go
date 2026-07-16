package model

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func normalizeStableUUID(value *string) error {
	if value == nil {
		return fmt.Errorf("UUID destination is nil")
	}
	if strings.TrimSpace(*value) == "" {
		*value = uuid.NewString()
		return nil
	}
	parsed, err := uuid.Parse(*value)
	if err != nil {
		return fmt.Errorf("invalid UUID %q: %w", *value, err)
	}
	*value = parsed.String()
	return nil
}

func (c *Channel) BeforeCreate(_ *gorm.DB) error { return normalizeStableUUID(&c.UUID) }

func (k *ChannelKey) BeforeCreate(_ *gorm.DB) error { return normalizeStableUUID(&k.UUID) }

func (g *Group) BeforeCreate(_ *gorm.DB) error { return normalizeStableUUID(&g.UUID) }

func (i *GroupItem) BeforeCreate(_ *gorm.DB) error { return normalizeStableUUID(&i.UUID) }

func (k *APIKey) BeforeCreate(_ *gorm.DB) error { return normalizeStableUUID(&k.UUID) }
